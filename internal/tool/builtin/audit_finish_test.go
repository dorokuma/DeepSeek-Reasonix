package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditFinish_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	_, err := a.Execute(context.Background(), json.RawMessage(`{"summary":""}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty summary should be rejected, got %v", err)
	}
}

func TestAuditFinish_RejectsWhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	_, err := a.Execute(context.Background(), json.RawMessage(`{"summary":"   \n\t  \n"}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("whitespace-only summary should be rejected, got %v", err)
	}
}

func TestAuditFinish_RejectsTooShort(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	// 499 bytes — one short of the 500-byte floor.
	short := strings.Repeat("a", minAuditSummaryBytes-1)
	args, _ := json.Marshal(map[string]string{"summary": short})
	_, err := a.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("sub-500-byte summary should be rejected")
	}
	for _, want := range []string{"500", "not a real report", "Telegram"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestAuditFinish_RejectsTooLarge(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	huge := strings.Repeat("a", maxAuditSummaryBytes+1)
	args, _ := json.Marshal(map[string]string{"summary": huge})
	_, err := a.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("over-200KB summary should be rejected")
	}
	if !strings.Contains(err.Error(), "200 KB") && !strings.Contains(err.Error(), "200,000") {
		t.Fatalf("error should mention the 200 KB cap, got: %v", err)
	}
}

func TestAuditFinish_AcceptsAtFloor(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	body := strings.Repeat("a", minAuditSummaryBytes)
	args, _ := json.Marshal(map[string]string{"summary": body})
	out, err := a.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("at-floor summary should pass, got: %v", err)
	}
	if !strings.Contains(out, "audit_finish ok") {
		t.Fatalf("ack should report ok, got: %q", out)
	}
}

func TestAuditFinish_AcceptsAtCeiling(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	body := strings.Repeat("a", maxAuditSummaryBytes)
	args, _ := json.Marshal(map[string]string{"summary": body})
	if _, err := a.Execute(context.Background(), args); err != nil {
		t.Fatalf("at-ceiling summary should pass, got: %v", err)
	}
}

func TestAuditFinish_WritesReport(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	// Use a unique marker so the test doesn't false-positive on the report's
	// own boilerplate ("# Audit Report", "## Summary" etc). The marker
	// itself must be > 500 bytes to pass audit_finish's minAuditSummaryBytes
	// floor — otherwise the test setup fails the call. TrimSpace in
	// Execute drops trailing whitespace, so use a marker that doesn't
	// have any (period instead of trailing space).
	marker := "ZZZ_UNIQUE_TEST_MARKER_42_" + strings.Repeat("body.", 200)
	args, _ := json.Marshal(map[string]string{"summary": marker})
	out, err := a.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// ack mentions the report path
	if !strings.Contains(out, dir) || !strings.Contains(out, ".audit-report.md") {
		t.Fatalf("ack should mention report path, got: %q", out)
	}
	// report file exists, contains the marker, has the boilerplate
	data, err := os.ReadFile(filepath.Join(dir, ".audit-report.md"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "# Audit Report") {
		t.Fatalf("report missing header, got: %q", body)
	}
	if !strings.Contains(body, "## Summary") {
		t.Fatalf("report missing Summary section, got: %q", body)
	}
	if !strings.Contains(body, marker) {
		t.Fatalf("report missing the summary text (marker %q not found)", marker)
	}
	// Execute no longer appends the reminder — that is handled by the agent
	// via PostCallGuidance. The ack is pure data.
	if strings.Contains(out, "REMINDER") {
		t.Fatalf("Execute should NOT include the reminder anymore; it's handled by PostCallGuidance, got: %q", out)
	}
}

func TestAuditFinish_AppendsNotesAsAppendix(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate .notes.md as if a previous audit step wrote it.
	notesPath := filepath.Join(dir, noteDefaultBasename)
	if err := os.WriteFile(notesPath, []byte("## note #1 · 2026-01-01T00:00:00Z · kind=evidence\n\nraw evidence here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := auditFinish{workDir: dir}
	summary := strings.Repeat("summary text. ", 50)
	args, _ := json.Marshal(map[string]string{"summary": summary})
	if _, err := a.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".audit-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "## Evidence") {
		t.Fatalf("report missing Evidence section (notes appendix), got: %q", body)
	}
	if !strings.Contains(body, "raw evidence here") {
		t.Fatalf("report should embed the raw note content, got: %q", body)
	}
}

func TestAuditFinish_NoNotesStillWorks(t *testing.T) {
	// If .notes.md doesn't exist (e.g., small audit that didn't use `note`),
	// audit_finish should still succeed and just omit the appendix.
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	summary := strings.Repeat("ok ", 300)
	args, _ := json.Marshal(map[string]string{"summary": summary})
	out, err := a.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute without notes file: %v", err)
	}
	if !strings.Contains(out, "audit_finish ok") {
		t.Fatalf("ack should report ok, got: %q", out)
	}
}

func TestAuditFinish_RejectsStubs(t *testing.T) {
	// The whole point: real failure modes the model might try. Each must be
	// rejected so the user gets a real report, not a hand-wave.
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	stubs := []string{
		"done",
		"Done.",
		"see .notes.md",
		"audit finished",
		"all good",
		"✅ finished",
	}
	for _, stub := range stubs {
		t.Run(stub, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"summary": stub})
			_, err := a.Execute(context.Background(), args)
			if err == nil {
				t.Fatalf("stub %q should be rejected", stub)
			}
		})
	}
}

func TestAuditFinish_CountsNoteRefs(t *testing.T) {
	dir := t.TempDir()
	a := auditFinish{workDir: dir}
	// Summary that references note#3 and note#7 (and again note#3).
	// Padding must push us safely past 500 bytes (3 + 7 + 3 ≈ 13 + 50*8 = 413, too tight).
	summary := "Findings:\n- see note#3 for X\n- see note#7 for Y\n- (also note#3 for context)\n" + strings.Repeat("padding here. ", 100)
	args, _ := json.Marshal(map[string]string{"summary": summary})
	out, err := a.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "note_refs=2") {
		t.Fatalf("ack should report 2 unique note refs (3 and 7), got: %q", out)
	}
}

func TestAuditFinish_ConfinedToRoots(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	a := auditFinish{roots: []string{dir}, workDir: dir}
	leak := filepath.Join(outside, "leak.md")
	summary, _ := json.Marshal(strings.Repeat("a", 1000))
	path, _ := json.Marshal(leak)
	args := []byte(`{"summary":` + string(summary) + `,"path":` + string(path) + `}`)
	_, err := a.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("path outside roots should be rejected, got %v", err)
	}
}

func TestAuditFinish_PathOverride(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	a := auditFinish{workDir: dir}
	override := filepath.Join(sub, "custom-report.md")
	summary, _ := json.Marshal(strings.Repeat("z", 1000))
	path, _ := json.Marshal(override)
	args := []byte(`{"summary":` + string(summary) + `,"path":` + string(path) + `}`)
	out, err := a.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "custom-report.md") {
		t.Fatalf("ack should mention override path, got %q", out)
	}
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("override file should exist: %v", err)
	}
}

func TestAuditFinish_SchemaHasRequiredSummary(t *testing.T) {
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(auditFinish{}.Schema(), &s); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range s.Required {
		if r == "summary" {
			found = true
		}
	}
	if !found {
		t.Fatalf("schema should mark summary as required, got %v", s.Required)
	}
}

func TestAuditFinish_ReadOnlyIsFalse(t *testing.T) {
	// audit_finish writes to disk — must be a writer so the permission
	// policy / ConfineWires wiring treats it like write_file.
	if (auditFinish{}).ReadOnly() {
		t.Fatal("audit_finish.ReadOnly() should be false (writes .audit-report.md)")
	}
}

func TestUniqueInts_DedupesAndPreservesOrder(t *testing.T) {
	// Test the helper indirectly through audit_finish's note_refs parsing.
	matches := [][]string{
		{"note #3", "3"},
		{"note #7", "7"},
		{"note #3", "3"},
		{"note #5", "5"},
	}
	got := uniqueInts(matches)
	want := []int{3, 7, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestUniqueInts_Empty(t *testing.T) {
	if got := uniqueInts(nil); got != nil {
		t.Fatalf("nil matches should give nil, got %v", got)
	}
}

func TestAuditFinish_PostCallGuidance_ReturnsReminder(t *testing.T) {
	a := auditFinish{}
	summary := strings.Repeat("a", minAuditSummaryBytes)
	args, _ := json.Marshal(map[string]string{"summary": summary})
	guidance := a.PostCallGuidance(args)
	if guidance == "" {
		t.Fatal("PostCallGuidance should return non-empty guidance for a valid summary")
	}
	if !strings.Contains(guidance, "final assistant message") {
		t.Fatalf("guidance should mention final assistant message, got: %q", guidance)
	}
	if !strings.Contains(guidance, ".audit-report.md") {
		t.Fatalf("guidance should mention the report file, got: %q", guidance)
	}
}

func TestAuditFinish_PostCallGuidance_EmptyForStub(t *testing.T) {
	a := auditFinish{}
	args, _ := json.Marshal(map[string]string{"summary": "done"})
	if g := a.PostCallGuidance(args); g != "" {
		t.Fatalf("too-short summary should return empty guidance, got: %q", g)
	}
}

func TestAuditFinish_PostCallGuidance_EmptyForInvalidArgs(t *testing.T) {
	a := auditFinish{}
	if g := a.PostCallGuidance(json.RawMessage(`not json`)); g != "" {
		t.Fatalf("invalid json should return empty guidance, got: %q", g)
	}
}
