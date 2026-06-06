package rtk

import (
	"strings"
	"testing"
)

// rewriteSamples maps representative shell commands to the RTK subcommand we
// expect rewrite to produce. PASS entries must stay declined by rewrite.
var rewriteSamples = []struct {
	cmd        string
	wantRTK    string // prefix after rewrite, empty = must decline
	decline    bool
}{
	{cmd: "git status", wantRTK: "rtk git"},
	{cmd: "ls .", wantRTK: "rtk ls"},
	{cmd: "tree", wantRTK: "rtk tree"},
	{cmd: "cat README.md", wantRTK: "rtk read"},
	{cmd: "rg foo .", wantRTK: "rtk grep"},
	{cmd: `find . -name '*.go'`, wantRTK: "rtk find"},
	{cmd: "echo hello", decline: true},
	{cmd: "python3 -c 'print(1)'", decline: true},
	{cmd: "read README.md", decline: true},
}

func TestRewriteMatrix(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	for _, tc := range rewriteSamples {
		tc := tc
		t.Run(tc.cmd, func(t *testing.T) {
			got := Rewrite(tc.cmd)
			if tc.decline {
				if got != "" {
					t.Fatalf("rewrite %q = %q, want decline", tc.cmd, got)
				}
				return
			}
			if got == "" || !strings.HasPrefix(got, tc.wantRTK) {
				t.Fatalf("rewrite %q = %q, want prefix %q", tc.cmd, got, tc.wantRTK)
			}
		})
	}
}

func TestCoverageListsAllRTKFilters(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Coverage() {
		if e.RTKCommand == "" {
			t.Fatal("empty RTK command in coverage table")
		}
		if seen[e.RTKCommand] {
			t.Fatalf("duplicate coverage entry: %s", e.RTKCommand)
		}
		seen[e.RTKCommand] = true
		if e.Via == "" {
			t.Fatalf("%s: missing Via", e.RTKCommand)
		}
	}
	if len(seen) < 40 {
		t.Fatalf("coverage table too small: %d entries", len(seen))
	}
}