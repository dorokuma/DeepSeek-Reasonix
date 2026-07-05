package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/jobs"
	"reasonix/internal/tool"
)

// steer-job, cancel-job, and peek-job operate session background jobs (task
// sub-agents, bash run_in_background, etc.) via jobs.FromContext.

func init() {
	tool.RegisterBuiltin(steerJob{})
	tool.RegisterBuiltin(cancelJob{})
	tool.RegisterBuiltin(peekJob{})
	tool.RegisterBuiltin(sendMessage{})
}

// --- steer-job: send a message to a running background job ---

type steerJob struct{}

func (steerJob) Name() string        { return "steer-job" }
func (steerJob) Description() string { return "Send a message to a running background job" }
func (steerJob) ReadOnly() bool      { return false }
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
func (cancelJob) Description() string { return "Cancel a running background job (bash or task)" }
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
	return "Non-blocking snapshot of a background job (task or bash). Includes new stdout/stderr since the last peek for buffered jobs. Task sub-agent final answers are delivered by the runtime when the job finishes; peek is for in-flight status or bash output."
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
	if reports := jm.PendingReports(p.JobID); len(reports) > 0 {
		out["reports"] = reports
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

// --- send_message: send a message from a background sub-agent to its parent ---

type sendMessage struct{}

func (sendMessage) OnlyForSubAgent() bool { return true }

func (sendMessage) Name() string { return "send_message" }
func (sendMessage) Description() string {
	return "Send a message/report from a background sub-agent to its parent agent"
}
func (sendMessage) ReadOnly() bool { return false }
func (sendMessage) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"message": {"type": "string", "description": "The text report/message to send to the parent"}
		},
		"required": ["message"]
	}`)
}

func (sendMessage) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Message == "" {
		return "", fmt.Errorf("message is required")
	}
	if ok := jobs.PostMessage(ctx, p.Message); ok {
		return `{"status":"sent"}`, nil
	}
	return `{"status":"failed","reason":"parent inbox full or job context unavailable"}`, nil
}
