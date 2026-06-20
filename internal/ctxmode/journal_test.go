package ctxmode

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestJournal_recordAndSearch(t *testing.T) {
	j, err := openJournal("")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	j.Record("edit", "internal/auth/handler.go", "added session middleware")
	j.Record("read", "internal/auth/handler.go", "")
	RecordTool(j, "grep", json.RawMessage(`{"pattern":"PreCompact"}`), "", nil)

	lines := j.search("handler.go", nil, 5)
	if len(lines) == 0 {
		t.Fatal("want FTS hits")
	}
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "handler.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("search lines = %v", lines)
	}
}

func TestJournal_compactGuidance(t *testing.T) {
	j, err := openJournal("")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	j.Record("edit", "pkg/compaction/resume.go", "wire PreCompact resume block")
	region := []provider.Message{
		{Role: provider.RoleUser, Content: "finish compaction resume in pkg/compaction/resume.go"},
	}
	g := j.CompactGuidance("resume.go", region)
	if !strings.Contains(g, "resume.go") {
		t.Fatalf("guidance = %q", g)
	}
}

func TestJournal_compactResumeBlock(t *testing.T) {
	j, err := openJournal("")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	j.Record("git", "git status", "modified internal/ctxmode/journal.go")
	block := j.CompactResumeBlock("journal")
	if !strings.Contains(block, "Resume context recovered") {
		t.Fatalf("block = %q", block)
	}
	if !strings.Contains(block, "journal.go") {
		t.Fatalf("block = %q", block)
	}
}

func TestJournal_probeOK(t *testing.T) {
	if !JournalProbeOK() {
		t.Fatal("journal FTS probe failed")
	}
}

func TestRecordTool_extended(t *testing.T) {
	j, err := openJournal("")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	RecordTool(j, "glob", json.RawMessage(`{"pattern":"**/*.go"}`), "a.go\n", nil)
	RecordTool(j, "ls", json.RawMessage(`{"path":"internal"}`), "", nil)
	RecordTool(j, "mcp__cf-docs__search", json.RawMessage(`{}`), "hit", nil)

	for _, want := range []string{"*.go", "internal", "example.com", "mcp__cf-docs"} {
		lines := j.search(want, nil, 5)
		if len(lines) == 0 {
			t.Fatalf("want FTS hit for %q", want)
		}
	}
}