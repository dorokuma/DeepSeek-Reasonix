package rtk

import (
	"errors"
	"strings"
	"testing"
)

func TestPipeFilterForRewrite_mapping(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"rtk git status", "git-status", true},
		{"rtk git log -5", "git-log", true},
		{"rtk git diff", "git-diff", true},
		{"rtk grep foo .", "grep", true},
		{"rtk find . -name '*.go'", "find", true},
		{"rtk pytest -q", "pytest", true},
		{"rtk cargo test", "cargo-test", true},
		{"rtk go test ./...", "go-test", true},
		{"rtk ruff check .", "ruff-check", true},
		{"rtk docker ps", "", false},
		{"echo hello", "", false},
	}
	for _, tc := range tests {
		got, ok := PipeFilterForRewrite(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("PipeFilterForRewrite(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPipeCompact_unknownFilter(t *testing.T) {
	t.Setenv("REASONIX_RTK", "rewrite")
	_, err := PipeCompact("not-a-filter", "hello")
	if !errors.Is(err, ErrNotRewritten) {
		t.Fatalf("unknown filter should decline: %v", err)
	}
}

func TestPipeCompact_gitLog(t *testing.T) {
	t.Setenv("REASONIX_RTK", "rewrite")
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	in := strings.Builder{}
	for i := 0; i < 300; i++ {
		in.WriteString("commit abc")
		in.WriteByte('0' + byte(i%10))
		in.WriteString("\nAuthor: x\nDate: 2024\n\n    msg\n\n")
	}
	compact, err := PipeCompact("git-log", in.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) >= len(in.String()) {
		t.Fatalf("pipe should shrink git-log output: in=%d out=%d", len(in.String()), len(compact))
	}
}

func TestPipeFilterForShell_gitStatus(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	f, ok := PipeFilterForShell("git status")
	if !ok || f != "git-status" {
		t.Fatalf("git status -> (%q, %v)", f, ok)
	}
	_, ok = PipeFilterForShell("echo hello")
	if ok {
		t.Fatal("echo hello should not map to a pipe filter")
	}
}
