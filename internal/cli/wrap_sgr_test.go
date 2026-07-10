package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrapTranscript_AssistantPrefixDoesNotGreyContinuation(t *testing.T) {
	prev := colorEnabled
	colorEnabled = true
	defer func() { colorEnabled = prev }()

	body := strings.Repeat("白字内容 ", 40)
	// Real assistantBlock shape: colored marker, then default-colored body.
	line := themeFg(activeCLITheme.toolRead, "  ▸ ") + body
	got := wrapTranscript(line, 40)
	parts := strings.Split(got, "\n")
	if len(parts) < 2 {
		t.Fatalf("expected wrap, got %d lines: %q", len(parts), got)
	}
	faint := fgSGR(activeCLITheme.faint)
	mark := fgSGR(activeCLITheme.toolRead)
	for i := 1; i < len(parts); i++ {
		stripped := ansi.Strip(parts[i])
		if strings.TrimSpace(stripped) == "" {
			continue
		}
		if strings.Contains(parts[i], faint) {
			t.Fatalf("continuation line %d has faint SGR:\nansi=%q\nplain=%q", i, parts[i], stripped)
		}
		if strings.Contains(parts[i], mark) {
			t.Fatalf("continuation line %d has marker color:\nansi=%q\nplain=%q", i, parts[i], stripped)
		}
	}
}

func TestAssistantBlock_MarkerIsBlueNotFaint(t *testing.T) {
	prev := colorEnabled
	colorEnabled = true
	defer func() { colorEnabled = prev }()
	got := assistantBlock("hello\nworld")
	faint := fgSGR(activeCLITheme.faint)
	mark := fgSGR(activeCLITheme.toolRead)
	if !strings.Contains(got, mark) {
		t.Fatalf("assistant marker should use toolRead blue/cyan, got %q", got)
	}
	if strings.Contains(got, faint) {
		t.Fatalf("assistant marker must not use faint grey, got %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(ansi.Strip(got), "world") {
		t.Fatalf("body missing: %q", got)
	}
}
