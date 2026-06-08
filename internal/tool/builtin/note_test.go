package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNote_WritesAndAppends(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	for i, content := range []string{"first audit finding", "second one", "third"} {
		args, _ := json.Marshal(map[string]string{"content": content, "kind": "evidence"})
		out, err := n.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if !strings.Contains(out, "note_id=") {
			t.Fatalf("call %d: result missing note_id: %q", i+1, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, ".notes.md"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	for _, want := range []string{"## note #1", "## note #2", "## note #3", "first audit finding", "third"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("file missing %q\n---\n%s\n---", want, data)
		}
	}
}

func TestNote_IDsMonotonicAfterRestart(t *testing.T) {
	// Restart-safety: a fresh tool instance must pick up where the file
	// left off, not start back at 1.
	dir := t.TempDir()
	pre := filepath.Join(dir, ".notes.md")
	if err := os.WriteFile(pre, []byte("## note #1 · x · kind=evidence\n\nfoo\n\n## note #5 · y · kind=evidence\n\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := note{workDir: dir}
	out, err := n.Execute(context.Background(), json.RawMessage(`{"content":"baz"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "note_id=6") {
		t.Fatalf("want note_id=6, got %q", out)
	}
}

func TestNote_DefaultKind(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	out, err := n.Execute(context.Background(), json.RawMessage(`{"content":"hi"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "kind=scratch") {
		t.Fatalf("default kind should be scratch, got %q", out)
	}
}

func TestNote_RejectsInvalidKind(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	_, err := n.Execute(context.Background(), json.RawMessage(`{"content":"x","kind":"vibes"}`))
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want kind error, got %v", err)
	}
}

func TestNote_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	for _, body := range []string{`""`, `"   "`, `"\n\t\n"`} {
		_, err := n.Execute(context.Background(), json.RawMessage(`{"content":`+body+`}`))
		if err == nil {
			t.Fatalf("empty content %s should be rejected", body)
		}
	}
}

func TestNote_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	huge := strings.Repeat("a", maxNoteContentBytes+1)
	args, _ := json.Marshal(map[string]string{"content": huge})
	_, err := n.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("oversize should be rejected with max hint, got %v", err)
	}
}

func TestNote_AcceptsAtLimit(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	// Pick a body whose BYTE size is just under the cap, even though rune
	// count is well below. Each "中" is 3 bytes in UTF-8.
	big := strings.Repeat("中", maxNoteContentBytes/3-1)
	if len(big) >= maxNoteContentBytes {
		t.Fatalf("test setup wrong: %d bytes", len(big))
	}
	args, _ := json.Marshal(map[string]string{"content": big})
	_, err := n.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("at-limit content should pass, got %v", err)
	}
}

func TestNote_PathOverride(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	n := note{workDir: dir}
	override := filepath.Join(sub, "custom.md")
	out, err := n.Execute(context.Background(), json.RawMessage(`{"content":"hi","path":"`+override+`"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "custom.md") {
		t.Fatalf("result should mention the override path, got %q", out)
	}
	// Default file should NOT have been created.
	if _, err := os.Stat(filepath.Join(dir, ".notes.md")); !os.IsNotExist(err) {
		t.Fatalf("default sidecar should not exist when override given, stat err = %v", err)
	}
}

func TestNote_ConfinedToRoots(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	n := note{roots: []string{dir}, workDir: dir}
	leak := filepath.Join(outside, "leak.md")
	_, err := n.Execute(context.Background(), json.RawMessage(`{"content":"x","path":"`+leak+`"}`))
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("path outside roots should be rejected, got %v", err)
	}
}

func TestNote_FormatStable(t *testing.T) {
	// Lock the file format: noteHeaderRe depends on the exact heading shape.
	dir := t.TempDir()
	n := note{workDir: dir}
	if _, err := n.Execute(context.Background(), json.RawMessage(`{"content":"body","kind":"evidence"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !noteHeaderRe.Match(data) {
		t.Fatalf("file format not parseable by noteHeaderRe:\n%s", data)
	}
	if !regexp.MustCompile(`## note #1 .*\n\nbody\n$`).Match(data) {
		t.Fatalf("unexpected block shape:\n%s", data)
	}
}

func TestNote_TrimsTrailingNewlines(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	if _, err := n.Execute(context.Background(), json.RawMessage(`{"content":"body\n\n\n\n"}`)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".notes.md"))
	if strings.HasSuffix(string(data), "\n\n\n") {
		t.Fatalf("trailing newlines not trimmed:\n%s", data)
	}
}

func TestNote_SchemaHasRequiredContent(t *testing.T) {
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(note{}.Schema(), &s); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range s.Required {
		if r == "content" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("schema should mark content as required, got %v", s.Required)
	}
}

func TestNote_ReadOnlyIsFalse(t *testing.T) {
	// `note` writes to disk and must be classified as a writer so the
	// permission policy / ConfineWriters wiring treats it like write_file.
	if (note{}).ReadOnly() {
		t.Fatal("note.ReadOnly() should be false (it writes to disk)")
	}
}

func TestNote_PostCallGuidance_ReturnsWorkflow(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	args, _ := json.Marshal(map[string]string{"content": "x"})
	guidance := n.PostCallGuidance(args)
	if guidance == "" {
		t.Fatal("PostCallGuidance should return non-empty guidance")
	}
	if !strings.Contains(guidance, "read_file") {
		t.Fatalf("guidance should mention read_file, got: %q", guidance)
	}
	if !strings.Contains(guidance, "audit_finish") {
		t.Fatalf("guidance should mention audit_finish, got: %q", guidance)
	}
	if !strings.Contains(guidance, "final assistant message") {
		t.Fatalf("guidance should mention final assistant message, got: %q", guidance)
	}
	if !strings.Contains(guidance, ".notes.md") {
		t.Fatalf("guidance should mention .notes.md path, got: %q", guidance)
	}
}

func TestNote_PostCallGuidance_MentionsOverridePath(t *testing.T) {
	dir := t.TempDir()
	n := note{workDir: dir}
	override := filepath.Join(dir, "sub", "custom.md")
	args, _ := json.Marshal(map[string]string{"content": "x", "path": override})
	guidance := n.PostCallGuidance(args)
	if guidance == "" {
		t.Fatal("PostCallGuidance should return non-empty guidance")
	}
	if !strings.Contains(guidance, "custom.md") {
		t.Fatalf("guidance should mention the override path, got: %q", guidance)
	}
}

func TestNote_PostCallGuidance_EmptyForInvalidArgs(t *testing.T) {
	n := note{}
	if g := n.PostCallGuidance(json.RawMessage(`not json`)); g != "" {
		t.Fatalf("invalid json should return empty guidance, got: %q", g)
	}
}
