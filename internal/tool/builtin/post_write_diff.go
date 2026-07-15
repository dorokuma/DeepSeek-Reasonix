package builtin

import (
	"strings"
	"unicode/utf8"

	"reasonix/internal/diff"
)

// Keep mutation receipts small: they are appended to the provider-visible
// conversation after every edit and therefore add uncached tail tokens. The
// diff still carries the actual post-write change needed to ground the next edit.
const maxPostWriteDiffBytes = 2048

const postWriteDiffTruncated = "…[actual diff truncated; use read_file for complete current contents]…"

func withActualPostWriteDiff(summary, path, before, after string) string {
	change := diff.BuildWithOptions(path, before, after, diff.Modify, diff.BuildOptions{
		// Do not auto-upload unchanged neighboring lines that the model did not
		// include in its edit request. The changed lines are enough to confirm the
		// applied state without widening workspace-data exposure.
		ContextLines: 0,
		OldLabel:     "before",
		NewLabel:     "after",
		Mode:         diff.OutputModePreview,
	})
	if strings.TrimSpace(change.Diff) == "" {
		return summary
	}
	return summary + "\nActual diff after write:\n" + clipPostWriteDiff(change.Diff)
}

func clipPostWriteDiff(text string) string {
	if len(text) <= maxPostWriteDiffBytes {
		return text
	}
	marker := "\n" + postWriteDiffTruncated + "\n"
	budget := maxPostWriteDiffBytes - len(marker)
	if budget <= 0 {
		return postWriteDiffTruncated
	}

	headBytes := budget * 3 / 4
	tailBytes := budget - headBytes
	headEnd := utf8PrefixBoundary(text, headBytes)
	tailStart := utf8SuffixBoundary(text, len(text)-tailBytes)
	return text[:headEnd] + marker + text[tailStart:]
}

func utf8PrefixBoundary(text string, end int) int {
	if end >= len(text) {
		return len(text)
	}
	if end < 0 {
		return 0
	}
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}

func utf8SuffixBoundary(text string, start int) int {
	if start <= 0 {
		return 0
	}
	if start >= len(text) {
		return len(text)
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return start
}
