package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type startedTaskPayload struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Label  string `json:"label,omitempty"`
}

var (
	legacyStartedTaskLine = regexp.MustCompile(`^Started task ([a-z]+-\d+)`)
	// bgResultJobIDRe extracts job_id from a runtime-injected delivery envelope.
	bgResultJobIDRe = regexp.MustCompile(`job_id="([^"]+)"`)
	// legacyDeliveryCallID still recognizes older synthetic tool deliveries in history.
	legacyDeliveryCallIDPrefix = "bg-delivery-"
)

// BackgroundDeliveryCallID is the legacy synthetic tool_call_id used by older
// sessions that delivered completions as a fake tool round. Kept only for
// idempotency/unread detection on historical transcripts.
func BackgroundDeliveryCallID(jobID string) string {
	if jobID == "" {
		return ""
	}
	return legacyDeliveryCallIDPrefix + jobID
}

// FormatStartedTaskResult is the synchronous tool return when a background delegate starts.
// This payload is complete and permanent for that tool_call_id — do not overwrite it later.
func FormatStartedTaskResult(jobID, label string) string {
	if label == "" {
		label = "task"
	}
	b, _ := json.Marshal(startedTaskPayload{JobID: jobID, Status: "started", Label: label})
	return string(b)
}

// FormatBackgroundTaskResult builds the runtime-injected observation the parent
// model sees when a background task finishes. It is a plain user-role message —
// NOT a tool call — so the model never learns a callable tool name from history.
func FormatBackgroundTaskResult(jobID, output string) string {
	output = strings.TrimSpace(output)
	return fmt.Sprintf(
		"<background-task-result job_id=%q status=%q>\n%s\n</background-task-result>",
		jobID, "completed", output,
	)
}

// IsBackgroundTaskResultMessage reports whether content is a runtime task delivery envelope.
func IsBackgroundTaskResultMessage(content string) bool {
	c := strings.TrimSpace(content)
	return strings.HasPrefix(c, "<background-task-result") ||
		strings.Contains(c, "<background-task-result ")
}

// BackgroundTaskResultJobID extracts job_id from a delivery envelope, or "".
func BackgroundTaskResultJobID(content string) string {
	if m := bgResultJobIDRe.FindStringSubmatch(content); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// ExtractJobIDFromStartedResult parses job_id from a started delegate tool result.
func ExtractJobIDFromStartedResult(result string) string {
	result = strings.TrimSpace(result)
	var p startedTaskPayload
	if json.Unmarshal([]byte(result), &p) == nil && p.JobID != "" && p.Status == "started" {
		return p.JobID
	}
	if m := legacyStartedTaskLine.FindStringSubmatch(result); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// IsStartedTaskPlaceholder reports whether tool content is a start stub (not a terminal answer).
func IsStartedTaskPlaceholder(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	var p startedTaskPayload
	if json.Unmarshal([]byte(content), &p) == nil {
		return p.Status == "started" && p.JobID != ""
	}
	return legacyStartedTaskLine.MatchString(content)
}

// TaskToolContentReferencesJob matches started stub lines (JSON or legacy text).
func TaskToolContentReferencesJob(content, jobID string) bool {
	if jobID == "" {
		return false
	}
	content = strings.TrimSpace(content)
	var p startedTaskPayload
	if json.Unmarshal([]byte(content), &p) == nil && p.JobID == jobID {
		return true
	}
	if strings.Contains(content, `"job_id":"`+jobID+`"`) {
		return true
	}
	return strings.Contains(content, "Started task "+jobID)
}
