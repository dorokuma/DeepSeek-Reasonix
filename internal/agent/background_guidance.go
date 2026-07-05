package agent

import (
	"regexp"
	"strings"
)

// startedJobLine matches any background delegate success line from task, explore, run_skill, etc.
var startedJobLine = regexp.MustCompile(`^Started task ([a-z]+-\d+)`)

// BackgroundJobPostCallGuidance returns post-call text for any tool that returned a Started line.
func BackgroundJobPostCallGuidance(result string) string {
	m := startedJobLine.FindStringSubmatch(strings.TrimSpace(result))
	if len(m) < 2 {
		return ""
	}
	return taskPostCallGuidance(m[1])
}
