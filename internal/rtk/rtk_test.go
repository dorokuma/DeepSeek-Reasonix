package rtk

import (
	"testing"
)

func TestAcceptRewrite_exitCodes(t *testing.T) {
	tests := []struct {
		code int
		out  string
		want string
	}{
		{0, "rtk git status", "rtk git status"},
		{3, "rtk git status", "rtk git status"},
		{1, "rtk git status", ""},
		{2, "rtk git status", ""},
		{0, "", ""},
		{3, "  ", ""},
	}
	for _, tt := range tests {
		if got := acceptRewrite(tt.code, tt.out); got != tt.want {
			t.Errorf("acceptRewrite(%d, %q) = %q, want %q", tt.code, tt.out, got, tt.want)
		}
	}
}

func TestModeFromEnv(t *testing.T) {
	t.Setenv("REASONIX_RTK", "off")
	if ModeFromEnv() != ModeOff {
		t.Fatal("want off")
	}
	t.Setenv("REASONIX_RTK", "suggest")
	if ModeFromEnv() != ModeSuggest {
		t.Fatal("want suggest")
	}
	t.Setenv("REASONIX_RTK", "")
	if ModeFromEnv() != ModeRewrite {
		t.Fatal("want rewrite default")
	}
}

func TestSplitShellPipeline(t *testing.T) {
	segs := splitShellPipeline(`cd /tmp && git status`)
	if len(segs) != 2 || segs[0].text != "cd /tmp" || segs[0].sep != "&&" || segs[1].text != "git status" {
		t.Fatalf("got %+v", segs)
	}
	segs = splitShellPipeline(`ls -la | head -20`)
	if len(segs) != 2 || segs[0].sep != "|" {
		t.Fatalf("pipe split: %+v", segs)
	}
	segs = splitShellPipeline(`echo "a && b"`)
	if len(segs) != 1 {
		t.Fatalf("quoted && must not split: %+v", segs)
	}
}

func TestRewrite_smoke(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	if got := Rewrite("git status"); got == "" {
		t.Fatal("expected rewrite for git status")
	}
	if got := Rewrite("echo hello"); got != "" {
		t.Fatalf("unsupported command should pass through, got %q", got)
	}
}

func TestCollectProbe_smoke(t *testing.T) {
	p := CollectProbe()
	if !Available() {
		if p.Warning == "" {
			t.Fatal("want warning when rtk missing")
		}
		return
	}
	if p.Path == "" || p.Version == "" {
		t.Fatalf("probe: %+v", p)
	}
	if !p.RewriteOK {
		t.Fatalf("rewrite smoke failed: %+v", p)
	}
}