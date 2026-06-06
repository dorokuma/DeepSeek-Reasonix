package rtk

import (
	"context"
	"errors"
	"testing"
)

func TestShellQuote(t *testing.T) {
	got := ShellQuote("it's fine")
	if got != `'it'"'"'s fine'` {
		t.Fatalf("quote = %q", got)
	}
}

func TestRunShellIfRewritten_declinesUnsupported(t *testing.T) {
	t.Setenv("REASONIX_RTK", "rewrite")
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	_, err := RunShellIfRewritten(context.Background(), "", "echo hello")
	if !errors.Is(err, ErrNotRewritten) {
		t.Fatalf("unsupported command should not rewrite: %v", err)
	}
}

func TestRunShellIfRewritten_acceptsGitStatus(t *testing.T) {
	t.Setenv("REASONIX_RTK", "rewrite")
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	out, err := RunShellIfRewritten(context.Background(), "/root/reasonix", "git status")
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("expected git status output")
	}
}

func TestRipgrepShell_rewriteAccepted(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	cmd := RipgrepShell("package rtk", "internal/rtk/rtk.go")
	if Rewrite(cmd) == "" {
		t.Fatalf("rg shell should rewrite: %q", cmd)
	}
}

func TestFindNameShell_rewriteAccepted(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	cmd, ok := FindNameShell(".", "*.go")
	if !ok {
		t.Fatal("expected mappable find shell")
	}
	if Rewrite(cmd) == "" {
		t.Fatalf("find shell should rewrite: %q", cmd)
	}
}