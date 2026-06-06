package rtk

import "testing"

func TestDeclineRewrite_sideEffects(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"curl -s https://api.telegram.org/bot/sendMessage",
		"wget -qO- https://example.com",
		"ssh root@host",
	} {
		if !DeclineRewrite(cmd) {
			t.Fatalf("want decline for %q", cmd)
		}
		if got := Rewrite(cmd); got != "" {
			t.Fatalf("Rewrite(%q) = %q, want decline", cmd, got)
		}
	}
}

func TestDeclineRewrite_allowsGit(t *testing.T) {
	t.Parallel()
	if DeclineRewrite("git status") {
		t.Fatal("git status should not decline")
	}
}

func TestFirstShellWord(t *testing.T) {
	t.Parallel()
	if got := firstShellWord(`curl -s "foo bar"`); got != "curl" {
		t.Fatalf("got %q", got)
	}
}