package control

import (
	"strings"
)

// peekTurnOperatorNote is appended for one turn when peek-job is exposed.
const peekTurnOperatorNote = `[Operator] peek-job is available this turn only (read-only), for background shell jobs. The task tool is synchronous — its tool result is the answer.`

// UserRequestsJobPeek is true when the user's raw line asks to expose peek-job.
// Product rule: only the substring "peek" (case-insensitive), with explicit negation filtered.
func UserRequestsJobPeek(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	lo := strings.ToLower(raw)
	if !strings.Contains(lo, "peek") {
		return false
	}
	neg := []string{
		"别peek", "不要peek", "不准peek", "别再peek", "禁止peek",
		"stop peek", "don't peek", "do not peek", "no peek",
	}
	for _, n := range neg {
		if strings.Contains(lo, n) {
			return false
		}
	}
	return true
}

func (c *Controller) maybeExposePeekJob(input, raw string) string {
	if c.executor == nil {
		return input
	}
	if !UserRequestsJobPeek(raw) {
		return input
	}
	c.executor.SetDiagnosticRequested(true)
	if strings.TrimSpace(input) == "" {
		return peekTurnOperatorNote
	}
	return strings.TrimSpace(input) + "\n\n" + peekTurnOperatorNote
}
