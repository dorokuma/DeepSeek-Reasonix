package evidence

import (
	"testing"
)

func TestUnresolvedWriteFailuresTracksAndClearsOnSuccess(t *testing.T) {
	ledger := NewLedger()
	ledger.Record(Receipt{ToolName: "edit_file", Success: false, Paths: []string{"/tmp/a.go"}, Write: true, ErrorPreview: "old_string not found"})
	ledger.Record(Receipt{ToolName: "write_file", Success: false, Paths: []string{"/tmp/b.go"}, Write: true, ErrorPreview: "permission denied"})
	ledger.Record(Receipt{ToolName: "edit_file", Success: true, Paths: []string{"/tmp/a.go"}, Write: true})

	failures := ledger.UnresolvedWriteFailures()
	if len(failures) != 1 {
		t.Fatalf("want 1 unresolved failure, got %d: %+v", len(failures), failures)
	}
	if failures[0].Path != "/tmp/b.go" || failures[0].Tool != "write_file" {
		t.Fatalf("unexpected failure: %+v", failures[0])
	}
}

func TestErrorPreviewFromToolOutputPrefersJSONError(t *testing.T) {
	got := ErrorPreviewFromToolOutput(`{"success":false,"error":"Could not find old_string"}`, "tool failed")
	if got != "Could not find old_string" {
		t.Fatalf("got %q", got)
	}
}