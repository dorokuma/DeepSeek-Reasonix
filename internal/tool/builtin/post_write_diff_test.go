package builtin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipPostWriteDiffPreservesUTF8AndBothEnds(t *testing.T) {
	text := "head\n" + strings.Repeat("修改后的内容\n", 800) + "tail\n"
	got := clipPostWriteDiff(text)

	if len(got) > maxPostWriteDiffBytes {
		t.Fatalf("clipped diff = %d bytes, want at most %d", len(got), maxPostWriteDiffBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("clipped diff split a UTF-8 rune")
	}
	for _, want := range []string{"head\n", "tail\n", postWriteDiffTruncated} {
		if !strings.Contains(got, want) {
			t.Fatalf("clipped diff should preserve %q:\n%s", want, got)
		}
	}
}
