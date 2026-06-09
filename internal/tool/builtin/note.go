package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"reasonix/internal/tool"
)

const (
	// maxNoteContentBytes caps a single note's body. Larger evidence should go
	// through write_file directly — `note`'s job is to keep audit-trail text out
	// of the conversation context, not to be a general-purpose file writer.
	maxNoteContentBytes = 256 * 1024

	// noteDefaultBasename is the sidecar file the tool appends to when the
	// caller doesn't pass `path`. Sits at the workspace root so a user can
	// `cat .notes.md` next to the project files.
	noteDefaultBasename = ".notes.md"
)

// noteHeaderRe matches the heading `note` writes for each entry; nextNoteID
// scans the existing file for the highest number to make the id sequence
// restart-safe (the file is the source of truth, not an in-process counter).
var noteHeaderRe = regexp.MustCompile(`(?m)^## note #(\d+) ·`)

func init() { tool.RegisterBuiltin(note{}) }

// note appends a long-form text entry to the session's sidecar file and
// returns a stable `note_id` the caller can cite (e.g. in a follow-up
// conversation history. The default file is <workdir>/.notes.md; the
// confined instance registered by ConfineWriters inherits the same workspace
// roots as the other writer tools, so the file always lives inside the
// workspace.
type note struct {
	roots   []string
	workDir string
}

func (note) Name() string { return "note" }

func (note) Description() string {
	return "Append a long-form note (audit evidence, command output, file diffs) to the session's sidecar notes file and return a `note_id` you can cite in follow-up summaries. This keeps long evidence OUT of the conversation history (preserves the model context window) while keeping it on disk for the user to review. Default file is `<workdir>/.notes.md`; override with `path`. `kind` is `evidence` | `summary` | `scratch` (default `scratch`). Single content > 256 KiB is rejected — use `write_file` for that size.\n\n**Final-answer contract**: after writing notes you MUST (a) call `read_file` on the sidecar to re-load the content into context, (b) call `audit_finish(summary=...)` with a substantive summary, and (c) include the full audit findings in your final assistant message — the user sees THAT, not the file. The tool's return value includes a reminder so you don't forget."
}

func (note) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "content":{"type":"string","description":"The note body. Markdown is fine."},
  "kind":{"type":"string","enum":["evidence","summary","scratch"],"description":"Tag for the note; surfaces in the file heading (default scratch)."},
  "path":{"type":"string","description":"Override the sidecar file path. Default: <workdir>/.notes.md. Must stay inside the workspace."}
},
"required":["content"]
}`)
}

// ReadOnly is false: `note` writes to disk, so it joins the same permission
// class as write_file / edit_file and is confined to the workspace roots by
// ConfineWriters.
func (note) ReadOnly() bool { return false }

func (n note) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Content string `json:"content"`
		Kind    string `json:"kind,omitempty"`
		Path    string `json:"path,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Content) == "" {
		return "", fmt.Errorf("content is required — pass a non-empty string")
	}
	if len(p.Content) > maxNoteContentBytes {
		return "", fmt.Errorf("content is %d bytes, max %d — use write_file for larger payloads", len(p.Content), maxNoteContentBytes)
	}
	kind := strings.TrimSpace(p.Kind)
	switch kind {
	case "", "evidence", "summary", "scratch":
	default:
		return "", fmt.Errorf("invalid kind %q (want evidence|summary|scratch)", kind)
	}
	if kind == "" {
		kind = "scratch"
	}

	path, err := n.resolveNotePath(p.Path)
	if err != nil {
		return "", err
	}

	// nextID is derived from the file's existing entries — restart-safe and
	// works correctly even if the tool instance is replaced mid-session.
	nextID, err := nextNoteID(path)
	if err != nil {
		return "", err
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	block := formatNoteBlock(nextID, kind, p.Content)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("note_id=%d path=%s kind=%s bytes=%d", nextID, path, kind, len(p.Content)), nil
}

// resolveNotePath defaults an empty `path` to <workDir>/.notes.md. On success
// the returned path is ready for confine() and I/O. Used by Execute (which
// returns error) and PostCallGuidance (which returns empty string on failure).
func (n note) resolveNotePath(raw string) (string, error) {
	return resolveWorkspacePath(n.workDir, noteDefaultBasename, raw, n.roots)
}

// PostCallGuidance teaches the model what to do after writing a note: re-load
// the sidecar, call audit_finish, and include the content in the final reply.
func (n note) PostCallGuidance(args json.RawMessage) string {
	var p struct {
		Path string `json:"path,omitempty"`
		Kind string `json:"kind,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}
	path, err := n.resolveNotePath(p.Path)
	if err != nil {
		return ""
	}
	kind := strings.TrimSpace(p.Kind)
	// For non-evidence notes, just remind to include in final answer.
	// Note: Execute defaults empty kind to "scratch", so non-evidence
	// and unset both get the simplified guidance.
	if kind == "" || kind == "scratch" || kind == "summary" {
		return "Include this content (or a tight paraphrase) in your final assistant message — the user sees THAT, not this tool result."
	}
	// Full workflow for evidence notes (default).
	return "You MUST:\n" +
		"1. Call `read_file(\"" + path + "\")` to re-load the notes into context.\n" +
		"2. Call `audit_finish(summary=...)` with the user-facing report.\n" +
		"3. Include the full report in your final assistant message — the user sees THAT, not this tool result."
}

// formatNoteBlock produces the human-readable entry the user can grep / cat
// for. The format is stable: a regex on `## note #N ·` is the file's index.
func formatNoteBlock(id int, kind, content string) string {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	// Trim a single trailing newline so the file doesn't grow blank lines when
	// the caller adds one; we always close the block with exactly one "\n".
	c := strings.TrimRight(content, "\n")
	return fmt.Sprintf("\n## note #%d · %s · kind=%s\n\n%s\n", id, ts, kind, c)
}

// nextNoteID reads the existing file (if any) and returns one more than the
// highest `## note #N` heading it finds. A fresh file starts at 1.
func nextNoteID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, err
	}
	max := 0
	for _, m := range noteHeaderRe.FindAllSubmatch(data, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		if n > max {
			max = n
		}
	}
	return max + 1, nil
}
