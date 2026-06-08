package agent

import (
	"strings"
	"testing"

	"reasonix/internal/evidence"
)

func TestFormatWriteFailureFooterListsPaths(t *testing.T) {
	footer := formatWriteFailureFooter([]evidence.WriteFailure{
		{Path: "/tmp/a.go", Tool: "edit_file", ErrorPreview: "old_string not found"},
		{Path: "/tmp/b.go", Tool: "write_file", ErrorPreview: "permission denied"},
	})
	if !strings.Contains(footer, "2 个文件未能修改") {
		t.Fatalf("missing count line: %q", footer)
	}
	if !strings.Contains(footer, "`/tmp/a.go`") || !strings.Contains(footer, "[edit_file]") {
		t.Fatalf("missing path detail: %q", footer)
	}
}

func TestAppendWriteFailureFooterSkipsWhenClean(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{ToolName: "write_file", Success: true, Paths: []string{"/tmp/a.go"}, Write: true})
	out := appendWriteFailureFooter("Done.", ledger, true)
	if out != "Done." {
		t.Fatalf("got %q", out)
	}
}

