package agent

import (
	"fmt"
	"os"
	"strings"

	"reasonix/internal/evidence"
)

const writeFailureFooterMaxPaths = 10

func writeFailureVerifierEnabled() bool {
	v := strings.TrimSpace(os.Getenv("REASONIX_WRITE_FAILURE_VERIFIER"))
	if v == "" {
		return true
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func appendWriteFailureFooter(text string, ledger *evidence.Ledger, enabled bool) string {
	text = strings.TrimSpace(text)
	if text == "" || ledger == nil || !enabled || !writeFailureVerifierEnabled() {
		return text
	}
	failures := ledger.UnresolvedWriteFailures()
	if len(failures) == 0 {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n" + formatWriteFailureFooter(failures)
}

func formatWriteFailureFooter(failures []evidence.WriteFailure) string {
	if len(failures) == 0 {
		return ""
	}
	lines := []string{
		fmt.Sprintf(
			"⚠️ 写文件校验：本回合有 %d 个文件未能修改（上文若写「已改好」不可信）。请 git status 或 read_file 确认。",
			len(failures),
		),
	}
	shown := 0
	for _, f := range failures {
		if shown >= writeFailureFooterMaxPaths {
			break
		}
		tool := strings.TrimSpace(f.Tool)
		if tool == "" {
			tool = "write"
		}
		preview := strings.TrimSpace(f.ErrorPreview)
		if preview != "" {
			lines = append(lines, fmt.Sprintf("  • `%s` — [%s] %s", f.Path, tool, preview))
		} else {
			lines = append(lines, fmt.Sprintf("  • `%s` — [%s] failed", f.Path, tool))
		}
		shown++
	}
	if remaining := len(failures) - shown; remaining > 0 {
		lines = append(lines, fmt.Sprintf("  • … 另有 %d 个", remaining))
	}
	return strings.Join(lines, "\n")
}