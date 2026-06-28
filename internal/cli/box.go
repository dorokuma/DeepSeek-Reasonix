package cli

import (
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// visibleWidth returns the printable column width of s: ANSI SGR codes are
// ignored and wide / grapheme-cluster characters (CJK, emoji ZWJ sequences,
// keycaps, flags) are each counted as the cells they occupy. Thin wrapper over
// x/ansi (already in the dep tree via bubbletea/lipgloss) so call sites read
// intent rather than re-deriving the strip-and-measure dance.
func visibleWidth(s string) int {
	return ansi.StringWidth(s)
}

// padRight returns s padded with spaces on the right until it occupies w
// terminal columns (visible width, not bytes). Strings already at or beyond
// width are returned unchanged. Use this instead of fmt's %-Ns when content
// may contain CJK or ANSI SGR codes.
func padRight(s string, w int) string {
	pad := w - visibleWidth(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// boxed wraps content in a rounded box drawn with the brand accent. Width
// auto-fits the longest line plus one column of padding on each side. The
// result always ends with a trailing newline so callers can Print it directly.
func boxed(lines []string) string {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}
	inner := width - 2
	if inner < 8 {
		inner = 8
	}
	contentW := inner - 2

	var renderLines []string
	for _, l := range lines {
		if visibleWidth(l) > contentW {
			wrapped := ansi.Wrap(l, contentW, "")
			subLines := strings.Split(wrapped, "\n")
			renderLines = append(renderLines, subLines...)
		} else {
			renderLines = append(renderLines, l)
		}
	}

	bar := strings.Repeat("─", inner)

	var b strings.Builder
	b.WriteString(accent("╭" + bar + "╮"))
	b.WriteByte('\n')
	for _, l := range renderLines {
		gap := inner - visibleWidth(l) - 2
		if gap < 0 {
			gap = 0
		}
		b.WriteString(accent("│"))
		b.WriteByte(' ')
		b.WriteString(l)
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteByte(' ')
		b.WriteString(accent("│"))
		b.WriteByte('\n')
	}
	b.WriteString(accent("╰" + bar + "╯"))
	b.WriteByte('\n')
	return b.String()
}
