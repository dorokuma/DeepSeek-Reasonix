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

var legacyStartedTaskLine = regexp.MustCompile(`^Started task ([a-z]+-\d+)`)

// FormatStartedTaskResult is the synchronous tool return when a background delegate starts.
func FormatStartedTaskResult(jobID, label string) string {
	if label == "" {
		label = "task"
	}
	b, _ := json.Marshal(startedTaskPayload{JobID: jobID, Status: "started", Label: label})
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
