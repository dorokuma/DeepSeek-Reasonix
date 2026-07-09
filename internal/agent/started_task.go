package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

type startedTaskPayload struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Label  string `json:"label,omitempty"`
}

// completedTaskArgs is the synthetic tool_call.arguments for a tail delivery turn.
// The Started stub is the FINAL answer to the original spawn call; completion is a
// separate, properly paired assistant+tool turn (never a mid-history rewrite).
type completedTaskArgs struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Event  string `json:"event"`
}

var legacyStartedTaskLine = regexp.MustCompile(`^Started task ([a-z]+-\d+)`)

// BackgroundDeliveryCallID is the synthetic tool_call_id for a finished job.
// Stable per job so completion/drain races stay idempotent.
func BackgroundDeliveryCallID(jobID string) string {
	if jobID == "" {
		return ""
	}
	return "bg-delivery-" + jobID
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

// FormatCompletedTaskCallArgs is the arguments JSON for the synthetic completion tool_call.
func FormatCompletedTaskCallArgs(jobID string) string {
	b, _ := json.Marshal(completedTaskArgs{JobID: jobID, Status: "completed", Event: "background_task_result"})
	return string(b)
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
