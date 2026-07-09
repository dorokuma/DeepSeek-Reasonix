package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/jobs"
	"reasonix/internal/tool"
)

// steer-job, cancel-job, and peek-job operate session background jobs via jobs.FromContext.
//
// Product split:
//   - kind task  (async sub-agent): final answer auto-delivers as a tail observation; peek is diagnostic.
//   - kind bash  (shell background): must use peek-job for output; no auto chat delivery.

func init() {
	tool.RegisterBuiltin(steerJob{})
	tool.RegisterBuiltin(cancelJob{})
	tool.RegisterBuiltin(peekJob{})
}

// --- steer-job: send a message to a running background job ---

type steerJob struct{}

func (steerJob) Name() string { return "steer-job" }
func (steerJob) Description() string {
	return `Send a new instruction to a running background sub-agent (task) or shell job.

CRITICAL — NOT FOR STATUS CHECKING: Never use steer-job to poll. It only queues a new instruction.
For task sub-agents: wait for automatic <background-task-result> at conversation tail.
For shell (bash) jobs: use peek-job for status/output.`
}
func (steerJob) ReadOnly() bool { return false }
func (steerJob) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "The ID of the job to steer"},
			"message": {"type": "string", "description": "The message to send to the job"}
		},
		"required": ["job_id", "message"]
	}`)
}

func (steerJob) PostCallGuidanceAfter(_ json.RawMessage, result string) string {
	if strings.Contains(result, `"status":"queued"`) || strings.Contains(result, "queued") {
		return "steer-job only queued a new instruction; that is not a final answer. For task sub-agents wait for <background-task-result> at the conversation tail. For shell jobs use peek-job."
	}
	return ""
}

func (steerJob) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p struct {
		JobID   string `json:"job_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	if p.Message == "" {
		return "", fmt.Errorf("message is required")
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	if err := jm.Steer(p.JobID, p.Message); err != nil {
		if err == jobs.ErrJobNotFound {
			return `{"status":"not_found"}`, nil
		}
		return "", fmt.Errorf("steer failed: %w", err)
	}
	return `{"status":"queued","message":"queued"}`, nil
}

// --- cancel-job: cancel a running background job ---

type cancelJob struct{}

func (cancelJob) Name() string        { return "cancel-job" }
func (cancelJob) Description() string { return "Cancel a running background job (shell or sub-agent)" }
func (cancelJob) ReadOnly() bool      { return false }
func (cancelJob) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "The ID of the job to cancel"}
		},
		"required": ["job_id"]
	}`)
}

func (cancelJob) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	if jm.Kill(p.JobID) {
		return `{"cancelled":true}`, nil
	}
	return `{"cancelled":false,"reason":"not found"}`, nil
}

// --- peek-job: peek at the status of a background job ---

type peekJob struct{}

func (peekJob) Name() string { return "peek-job" }
func (peekJob) Description() string {
	return `Non-blocking snapshot of a background job.

PRIMARY USE — shell (bash) jobs: read status and new stdout/stderr since last peek. Shell output is NOT auto-delivered to chat.

TASK SUB-AGENTS: results auto-deliver as a <background-task-result> observation at conversation tail (not a tool). Do not poll task jobs with peek-job. Only peek a task when the user explicitly asks for mid-flight status.

Never call peek-job more than once per turn unless the user asks again.`
}
func (peekJob) ReadOnly() bool { return true }
func (peekJob) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "The ID of the job to peek at"}
		},
		"required": ["job_id"]
	}`)
}

func (peekJob) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	status, err := jm.Peek(p.JobID)
	if err != nil {
		if err == jobs.ErrJobNotFound {
			return "", fmt.Errorf("no background job %q", p.JobID)
		}
		return "", fmt.Errorf("peek failed: %w", err)
	}
	out := map[string]any{
		"job_id": status.JobID,
		"status": status.Status,
	}
	if kind, kOK := jm.Kind(p.JobID); kOK {
		out["kind"] = kind
		if jobs.AutoDelivers(kind) {
			out["delivery"] = "auto_observation"
			out["note"] = "task sub-agent: final answer auto-delivers as <background-task-result>; peek is diagnostic only"
		} else {
			out["delivery"] = "peek_only"
			out["note"] = "shell job: use new_output below; not auto-delivered to chat"
		}
	}
	if status.StartedAtMs > 0 {
		out["started_at_ms"] = status.StartedAtMs
	}
	if status.Step > 0 {
		out["step"] = status.Step
	}
	if status.LastTool != "" {
		out["last_tool"] = status.LastTool
	}
	if status.LastAck != "" {
		out["last_ack"] = status.LastAck
	}
	if text, _, found := jm.Output(p.JobID); found && strings.TrimSpace(text) != "" {
		out["new_output"] = text
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal status: %w", err)
	}
	return string(b), nil
}
