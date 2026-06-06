package ctxmode

import (
	"strings"
	"testing"
)

func TestNumberedLine_readFile(t *testing.T) {
	lines := []string{
		"   1→package main",
		"   2→import \"fmt\"",
		"   3→func main() {}",
	}
	if !hasNumberedLines(lines) {
		t.Fatal("want numbered read_file detection")
	}
	var b strings.Builder
	writePreview(&b, lines, 2, 1, true)
	out := b.String()
	if strings.Contains(out, "   1→   1→") {
		t.Fatalf("double numbering: %q", out)
	}
	if !strings.Contains(out, "package main") {
		t.Fatalf("preview missing content: %q", out)
	}
}