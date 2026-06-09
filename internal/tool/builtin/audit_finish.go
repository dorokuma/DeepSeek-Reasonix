package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"reasonix/internal/tool"
)

// audit_finish is the host-enforced contract that prevents the model from
// "completing" an audit with a vague final answer. Two failure modes drove
// this design:
//
//  1. Model writes a long note via the `note` tool, then never re-loads it
//     into context and produces a one-line final answer ("see .notes.md").
//     The user gets nothing usable.
//
//  2. Model skips the audit_finish entirely, ends the turn after the last
//     (which by design is short — that's the point of the cap).
//
// audit_finish solves this by validating a substantive `summary` (the
// user-facing report) and writing it to a sidecar file the user can
// `cat` if Telegram truncates. The summary is what the model should also
// include in its final assistant message — but the host doesn't enforce
// that, because assistant text isn't a tool call we can validate. The
// 500-byte floor + 200 KB ceiling is the strongest mechanical signal we
// can give the model: "if you can't write 500 bytes of report, you
// haven't actually finished."
const (
	minAuditSummaryBytes = 500     // 500 B forces real prose, not "done"
	maxAuditSummaryBytes = 200000 // 200 KB forces chunking if audit is huge
)

// auditReportBasename is the report file the tool writes next to the notes
// file. The user can `cat` it independently of the model.
const auditReportBasename = ".audit-report.md"

var auditNoteRefRe = regexp.MustCompile(`(?m)note\s*#\s*(\d+)`)

func init() { tool.RegisterBuiltin(auditFinish{}) }

// auditFinish enforces a substantive end-of-audit report. The summary is
// what the model SHOULD include in its final assistant message to the user
// (the bridge auto-splits long messages into multiple Telegram bubbles).
// This tool itself is a writer, so it joins the workspace confinement and
// can cite the report file in a follow-up note as proof the
// audit was formally wrapped.
type auditFinish struct {
	roots   []string
	workDir string
}

func (auditFinish) Name() string { return "audit_finish" }

func (auditFinish) Description() string {
	return "End-of-audit report tool. Call this once all audit notes are written and the audit is finished. The `summary` you pass IS the report the user sees — write the full findings inline (P0/P1/P2 risks with file:line, remediation steps, etc). It must be 500–200,000 bytes; 'done' or 'see .notes.md' is rejected because the user needs the substance. The summary is also appended to `<workdir>/.audit-report.md` for archival. After this call, your final assistant message should contain the same content (or a tight paraphrase) so Telegram shows the full audit — not a pointer to a file. The flow: (1) call `note` for each long piece of evidence during the audit, (2) write short 'see note#N' cite-summaries inline, (3) when done, re-read the sidecar via `read_file`, (4) call this `audit_finish` with the write-up, (5) include the report in your final assistant message."
}

func (auditFinish) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "summary":{"type":"string","description":"The user-facing audit report. Must be 500–200,000 bytes. Write the full findings inline (risks, file:line, remediation). 'done' or 'see .notes.md' is rejected."},
  "path":{"type":"string","description":"Override the report file path. Default: <workdir>/.audit-report.md. Must stay inside the workspace."}
},
"required":["summary"]
}`)
}

// ReadOnly is false: audit_finish writes the report to disk, so it joins the
// same permission / confinement class as write_file.
func (auditFinish) ReadOnly() bool { return false }

func (a auditFinish) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Summary string `json:"summary"`
		Path    string `json:"path,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	summary := strings.TrimSpace(p.Summary)
	if summary == "" {
		return "", fmt.Errorf("summary is required and must be non-empty — write the actual report, not 'done'")
	}
	if len(summary) < minAuditSummaryBytes {
		return "", fmt.Errorf("summary is %d bytes, min %d — that is not a real report. Write the full findings inline; the user is reading this in Telegram. (If the audit is genuinely tiny, inline notes are enough — don't bother with audit_finish.)", len(summary), minAuditSummaryBytes)
	}
	if len(summary) > maxAuditSummaryBytes {
		return "", fmt.Errorf("summary is %d bytes, max %d — split into multiple audit_finish calls (each ≤ 200 KB). The bridge auto-splits the final assistant message into multiple Telegram bubbles too, so this is fine.", len(summary), maxAuditSummaryBytes)
	}

	path, err := a.resolveAuditPath(p.Path)
	if err != nil {
		return "", err
	}

	// Compose the report file: header + summary + appended note dump (if any).
	// Putting the raw notes at the bottom lets a human (or another agent in
	// a later turn) verify the report's claims against the original evidence
	// without opening a second file.
	notesPath := strings.TrimSuffix(path, filepath.Base(path)) + noteDefaultBasename
	var b strings.Builder
	ts := time.Now().UTC().Format(time.RFC3339)
	b.WriteString("# Audit Report\n\n")
	fmt.Fprintf(&b, "**Generated:** %s\n\n", ts)
	b.WriteString("## Summary\n\n")
	b.WriteString(summary)
	b.WriteString("\n")

	// Best-effort: append the .notes.md content as the evidence appendix.
	// Don't fail the tool call if the notes file is missing or unreadable —
	// the summary is the contract; the appendix is a nice-to-have.
	if data, err := os.ReadFile(notesPath); err == nil && len(data) > 0 {
		b.WriteString("\n## Evidence (raw notes from ")
		b.WriteString(notesPath)
		b.WriteString(")\n\n")
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	// Reference counts help the model (and the user) verify the report
	// actually cited the underlying notes instead of fabricating from memory.
	noteRefs := uniqueInts(auditNoteRefRe.FindAllStringSubmatch(summary, -1))

	return fmt.Sprintf("audit_finish ok report=%s summary=%d bytes note_refs=%d", path, len(summary), len(noteRefs)), nil
}

// resolveAuditPath defaults an empty `path` to <workDir>/.audit-report.md.
// Used by Execute (which returns error) and PostCallGuidance (which returns
// empty string on failure).
func (a auditFinish) resolveAuditPath(raw string) (string, error) {
	return resolveWorkspacePath(a.workDir, auditReportBasename, raw, a.roots)
}

// PostCallGuidance teaches the model to include the audit report in its final
// assistant message — the user sees the final answer, not the tool result.
func (a auditFinish) PostCallGuidance(args json.RawMessage) string {
	var p struct {
		Summary string `json:"summary"`
		Path    string `json:"path,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}
	summary := strings.TrimSpace(p.Summary)
	path, err := a.resolveAuditPath(p.Path)
	if err != nil {
		return ""
	}
	// Only remind when the summary was substantive enough to matter.
	if len(summary) < minAuditSummaryBytes {
		return ""
	}
	return "Include the report in your final assistant message — the user sees THAT, not this tool result.\n" +
		"The full audit report was written to `" + path + "` if you want to reference it."
}

// uniqueInts extracts the captured integers from regex submatches and dedupes
// (preserving first-seen order). Returns nil for no matches.
func uniqueInts(matches [][]string) []int {
	if len(matches) == 0 {
		return nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		var n int
		if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil {
			continue
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
