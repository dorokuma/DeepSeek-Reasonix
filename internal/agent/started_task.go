package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type startedTaskPayload struct {
	JobID              string `json:"job_id"`
	Status             string `json:"status"`
	Label              string `json:"label,omitempty"`
	ToolCallComplete   bool   `json:"tool_call_complete"`
	AnswerDelivery     string `json:"answer_delivery,omitempty"`
}

var (
	legacyStartedTaskLine = regexp.MustCompile(`^Started task ([a-z]+-\d+)`)
	// bgResultJobIDRe extracts job_id from a runtime-injected delivery envelope.
	bgResultJobIDRe = regexp.MustCompile(`job_id="([^"]+)"`)
	// lineJobIDRe matches the human receipt line "job_id: task-N".
	lineJobIDRe = regexp.MustCompile(`(?m)^job_id:\s*([a-z]+-\d+)\s*$`)
	// jsonJobIDRe finds "job_id":"…" anywhere in a multi-line receipt.
	jsonJobIDRe = regexp.MustCompile(`"job_id"\s*:\s*"([a-z]+-\d+)"`)
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
//
// Models treat bare short JSON as an incomplete/unreliable status object and re-call task.
// This receipt is deliberately plain-language + authoritative: this tool call is already
// COMPLETE; the answer arrives later as a separate observation (never by mutating this row).
func FormatStartedTaskResult(jobID, label string) string {
	if label == "" {
		label = "task"
	}
	machine, _ := json.Marshal(startedTaskPayload{
		JobID:            jobID,
		Status:           "started",
		Label:            label,
		ToolCallComplete: true,
		AnswerDelivery:   "conversation-tail <background-task-result> observation (not a tool)",
	})
	return fmt.Sprintf(`ACCEPTED — background sub-agent is running.

job_id: %s
label: %s
tool_result: COMPLETE

This is the full successful result of the task tool call. It is a permanent receipt, not a partial status and not "unreliable JSON".
Nothing about this tool call will update later. Do not re-call task because this looks short.

Final answer path (runtime only — you never call a tool for this):
  when the sub-agent finishes, a NEW message is appended at the conversation tail:
  <background-task-result job_id=%q status="completed">
  … full sub-agent answer …
  </background-task-result>

Until that <background-task-result job_id=%q> message exists:
  • do NOT call task again for the same or similar goal (exact or paraphrased)
  • do NOT invent task_result / get_result / poll tools
  • do NOT peek-job this job_id (task results auto-deliver; peek is for shell jobs)

%s`, jobID, label, jobID, jobID, string(machine))
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

// ExtractJobIDFromStartedResult parses job_id from a started delegate tool result
// (rich receipt, bare JSON, or legacy "Started task …" line).
func ExtractJobIDFromStartedResult(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	// Bare JSON (legacy short form).
	var p startedTaskPayload
	if json.Unmarshal([]byte(result), &p) == nil && p.JobID != "" && isStartedStatus(p.Status) {
		return p.JobID
	}
	// Trailing / embedded machine JSON object.
	if i := strings.LastIndex(result, "{"); i >= 0 {
		if json.Unmarshal([]byte(result[i:]), &p) == nil && p.JobID != "" {
			return p.JobID
		}
	}
	if m := lineJobIDRe.FindStringSubmatch(result); len(m) >= 2 {
		return m[1]
	}
	if m := jsonJobIDRe.FindStringSubmatch(result); len(m) >= 2 {
		return m[1]
	}
	if m := legacyStartedTaskLine.FindStringSubmatch(result); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func isStartedStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "started", "accepted", "queued", "running":
		return true
	default:
		return false
	}
}

// IsStartedTaskPlaceholder reports whether tool content is a start receipt
// (not a terminal sub-agent answer).
func IsStartedTaskPlaceholder(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	// Rich receipt form.
	if strings.HasPrefix(content, "ACCEPTED") && strings.Contains(content, "job_id:") {
		return ExtractJobIDFromStartedResult(content) != ""
	}
	if strings.Contains(content, "tool_result: COMPLETE") && ExtractJobIDFromStartedResult(content) != "" {
		return true
	}
	// Bare JSON form.
	var p startedTaskPayload
	if json.Unmarshal([]byte(content), &p) == nil {
		return isStartedStatus(p.Status) && p.JobID != ""
	}
	// Embedded machine JSON at end of multi-line body.
	if i := strings.LastIndex(content, "{"); i >= 0 {
		if json.Unmarshal([]byte(content[i:]), &p) == nil && isStartedStatus(p.Status) && p.JobID != "" {
			return true
		}
	}
	return legacyStartedTaskLine.MatchString(content)
}

// TaskToolContentReferencesJob matches started receipt lines (rich, JSON, or legacy).
func TaskToolContentReferencesJob(content, jobID string) bool {
	if jobID == "" {
		return false
	}
	content = strings.TrimSpace(content)
	if ExtractJobIDFromStartedResult(content) == jobID {
		return true
	}
	if strings.Contains(content, `"job_id":"`+jobID+`"`) {
		return true
	}
	if strings.Contains(content, "job_id: "+jobID) {
		return true
	}
	return strings.Contains(content, "Started task "+jobID)
}
