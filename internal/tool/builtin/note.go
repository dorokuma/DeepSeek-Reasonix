package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/tool"
)

const (
	// maxNoteContentBytes caps a single note's body. Larger evidence should go
	// through write_file directly — `note`'s job is to keep audit-trail text out
	// of the conversation context, not to be a general-purpose file writer.
	maxNoteContentBytes = 256 * 1024
	// maxNoteFileBytes caps the total size of the notes file to prevent
	// unbounded growth from a runaway model.
	maxNoteFileBytes = 10 * 1024 * 1024

	// noteDefaultBasename is the sidecar file the tool appends to when the
	// caller doesn't pass `path`. Sits at the workspace root so a user can
	// `cat .notes.md` next to the project files.
	noteDefaultBasename = ".notes.md"

	// maxNotesRetained is the maximum number of note blocks kept by
	// cleanupNotes (sorted by descending ID). When the file overflows, only
	// the N most recent notes survive, unless they are still within the
	// retention window.
	maxNotesRetained = 30
	// noteRetentionDays is the age window for unconditional retention: any
	// note written within this many days before now is kept regardless of
	// the total count.
	noteRetentionDays = 7
)

// noteHeaderRe matches the heading `note` writes for each entry; nextNoteID
// scans the existing file for the highest number to make the id sequence
// restart-safe (the file is the source of truth, not an in-process counter).
var noteHeaderRe = regexp.MustCompile(`(?m)^## note #(\d+) ·`)

// noteBlockRe matches a note header and captures ID and timestamp for
// cleanup decisions. The timestamp sits between two interpunct (·) separators.
var noteBlockRe = regexp.MustCompile(`(?m)^## note #(\d+) · ([^·]+) · kind=\w+$`)

// noteMu protects each notes file path from concurrent writes.
var noteMu sync.Map

func init() { tool.RegisterBuiltin(note{}) }

// note appends a long-form text entry to the session's sidecar file and
// returns a stable `note_id` the caller can cite (e.g. in a follow-up
// conversation history. The default file is <workdir>/.notes.md; the
// confined instance registered by ConfineWriters inherits the same workspace
// roots as the other writer tools, so the file always lives inside the
// workspace.
type note struct {
	workDir string
}

func (note) Name() string { return "note" }

func (note) Description() string {
	return "Append a long-form note (audit evidence, command output, file diffs) to the session's sidecar notes file and return a `note_id` you can cite in follow-up summaries. This keeps long evidence OUT of the conversation history (preserves the model context window) while keeping it on disk for the user to review. Default file is `<workdir>/.notes.md`; override with `path`. `kind` is `evidence` | `summary` | `scratch` (default `scratch`). Single content > 256 KiB is rejected — use the file writing tool for larger payloads.\n\n**Final-answer contract**: after writing notes you MUST (a) re-read the sidecar file to load the content into context, (b) call `audit_finish(summary=...)` with a substantive summary, and (c) include the full audit findings in your final assistant message — the user sees THAT, not the file. The tool's return value includes a reminder so you don't forget."
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
		return "", fmt.Errorf("content is %d bytes, max %d — too large, use a file instead", len(p.Content), maxNoteContentBytes)
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

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	// Per-path lock ensures multiple goroutines don't corrupt the file.
	mu := getMutex(path)
	mu.Lock()
	defer mu.Unlock()

	// Read existing content once (TOCTOU-safe) for both ID derivation and
	// cleanup. Check file size before reading to avoid loading huge files.
	var oldData []byte
	if fi, err := os.Stat(path); err == nil {
		if fi.Size() > maxNoteFileBytes {
			return "", fmt.Errorf("notes file is %d bytes, exceeds max %d bytes (%d MB) — archive or clear old notes", fi.Size(), maxNoteFileBytes, maxNoteFileBytes>>20)
		}
		oldData, err = os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}

	nextID, err := nextNoteID(oldData)
	if err != nil {
		return "", err
	}

	block := formatNoteBlock(nextID, kind, p.Content)
	keptOld := cleanupNotes(oldData, time.Now())

	// Check total file size before writing.
	totalBytes := int64(len(block)) + int64(len(keptOld))
	if totalBytes > maxNoteFileBytes {
		return "", fmt.Errorf("notes file would be %d bytes, exceeds max %d bytes (%d MB) — archive or clear old notes", totalBytes, maxNoteFileBytes, maxNoteFileBytes>>20)
	}

	// Build new content: new block first (prepend), then cleaned old notes.
	newContent := []byte(block)
	if len(keptOld) > 0 {
		newContent = append(newContent, keptOld...)
	}

	// Atomic write via temp file + rename.
	tmpPath := path + ".tmp." + strconv.Itoa(os.Getpid())
	// Clean up any stale temp file from a previous crash.
	if _, err := os.Stat(tmpPath); err == nil {
		os.Remove(tmpPath)
	}

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create temp file %s: %w", tmpPath, err)
	}

	if _, err := f.Write(newContent); err != nil {
		f.Close() // best-effort cleanup
		os.Remove(tmpPath)
		return "", fmt.Errorf("write %s: %w", tmpPath, err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("sync %s: %w", tmpPath, err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}

	return fmt.Sprintf("note_id=%d path=%s kind=%s bytes=%d", nextID, path, kind, len(p.Content)), nil
}

// resolveNotePath defaults an empty `path` to <workDir>/.notes.md. On success
// the returned path is ready for confine() and I/O. Used by Execute (which
// returns error) and PostCallGuidance (which returns empty string on failure).
func (n note) resolveNotePath(raw string) (string, error) {
	return resolveWorkspacePath(n.workDir, noteDefaultBasename, raw)
}

// PostCallGuidance teaches the model what to do after writing a note: call
// audit_finish and include the content in the final reply.
func (n note) PostCallGuidance(args json.RawMessage) string {
	var p struct {
		Path string `json:"path,omitempty"`
		Kind string `json:"kind,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
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
		"1. Call `audit_finish(summary=...)` with the user-facing report.\n" +
		"2. Include the full report in your final assistant message — the user sees THAT, not this tool result."
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

// cleanupNotes removes old notes from data, keeping only the most recent 30
// entries (by note ID) OR any notes written within the last 7 days. If every
// existing note is already retained, the original slice is returned unchanged.
// Bytes before the first `## note #` heading (the file prefix, e.g. a document
// title) are preserved verbatim in the output.
func cleanupNotes(data []byte, now time.Time) []byte {
	type blockInfo struct {
		start, end int // byte range of this block (includes leading \n if present)
		id         int
		ts         time.Time
	}

	locs := noteBlockRe.FindAllSubmatchIndex(data, -1)
	if len(locs) == 0 {
		return data
	}

	// Everything before the first `## note #` is the file prefix, preserved
	// verbatim. If the first match is preceded by '\n', that '\n' belongs to
	// the first block (as its leading separator), not to the prefix.
	prefixEnd := locs[0][0]
	if prefixEnd > 0 && data[prefixEnd-1] == '\n' {
		prefixEnd--
	}
	prefix := data[:prefixEnd]

	var blocks []blockInfo
	for _, loc := range locs {
		// loc[0] = start of `## note #...`, loc[1] = end of match
		// loc[2] = start of id,   loc[3] = end of id
		// loc[4] = start of ts,   loc[5] = end of ts
		id, _ := strconv.Atoi(string(data[loc[2]:loc[3]]))
		tsStr := strings.TrimSpace(string(data[loc[4]:loc[5]]))
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			ts = time.Time{} // zero — don't apply age-based retention
		}

		start := loc[0] // default: block starts at the `##`
		if loc[0] > 0 && data[loc[0]-1] == '\n' {
			start = loc[0] - 1 // include the leading \n
		}
		blocks = append(blocks, blockInfo{
			start: start,
			end:   -1, // filled in next loop
			id:    id,
			ts:    ts,
		})
	}

	// Set end positions: each block spans from its start to the start of the
	// next block (or EOF for the last block).
	for i := 0; i < len(blocks); i++ {
		if i+1 < len(blocks) {
			blocks[i].end = blocks[i+1].start
		} else {
			blocks[i].end = len(data)
		}
	}

	// Fast path: if there are ≤ maxNotesRetained notes and every parseable
	// timestamp is within noteRetentionDays, nothing needs to be cleaned up.
	cutoff := now.Add(-noteRetentionDays * 24 * time.Hour)
	allRecent := true
	for _, b := range blocks {
		if !b.ts.IsZero() && b.ts.Before(cutoff) {
			allRecent = false
			break
		}
	}
	if len(blocks) <= maxNotesRetained && allRecent {
		return data
	}

	// Build keep set: top maxNotesRetained by ID (descending) OR within
	// noteRetentionDays.
	sorted := append([]blockInfo(nil), blocks...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].id > sorted[j].id
	})

	keep := make(map[int]bool, len(blocks))
	n := maxNotesRetained
	if len(sorted) < n {
		n = len(sorted)
	}
	for i := 0; i < n; i++ {
		keep[sorted[i].id] = true
	}
	for _, b := range blocks {
		if b.ts.IsZero() || b.ts.After(cutoff) {
			keep[b.id] = true
		}
	}

	if len(keep) == len(blocks) {
		return data
	}

	// Rebuild with prefix + kept blocks in file order.
	var buf bytes.Buffer
	buf.Write(prefix)
	for _, b := range blocks {
		if keep[b.id] {
			buf.Write(data[b.start:b.end])
		}
	}
	return buf.Bytes()
}

// nextNoteID scans data for the highest `## note #N` heading and returns N+1.
// A nil or empty slice (empty file) yields 1.
func nextNoteID(data []byte) (int, error) {
	max := 0
	for _, m := range noteHeaderRe.FindAllSubmatch(data, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		if n > max {
			max = n
		}
	}
	return max + 1, nil
}

// getMutex returns or creates a per-path mutex for coordinating writes to the
// same notes file from concurrent goroutines.
func getMutex(path string) *sync.Mutex {
	v, _ := noteMu.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}
