package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"reasonix/internal/jobs"
	"reasonix/internal/tool"
)

// bash_output and kill_shell operate the background jobs registered by
// bash(run_in_background) and task(run_in_background). They reach the session's
// job manager through the call context (jobs.FromContext) — the agent stamps it
// onto every tool call — and degrade to a clear error when it isn't available
// (a headless context with no manager). Together they poll a job's new output,
// terminate a job, and block until jobs finish.

func init() {
	tool.RegisterBuiltin(steerJob{})
	tool.RegisterBuiltin(cancelJob{})
	tool.RegisterBuiltin(peekJob{})
	tool.RegisterBuiltin(sendMessage{})
}

// --- bash_output: poll a background job's new output (non-blocking) ---

type bashOutput struct{}

func (bashOutput) Name() string { return "bash_output" }

func (bashOutput) Description() string {
	return "Read new output from a background job started with bash(run_in_background=true) or task(run_in_background=true). Returns the output produced since the last bash_output call for that job, plus its status (running/done/failed/killed). Does not block."
}

func (bashOutput) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"The background job id (e.g. \"bash-1\") returned when it was started."},"filter":{"type":"string","description":"Optional regular expression; only matching lines of the new output are returned."}},"required":["job_id"]}`)
}

func (bashOutput) ReadOnly() bool { return true }

func (bashOutput) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID  string `json:"job_id"`
		Filter string `json:"filter"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	text, status, found := jm.Output(p.JobID)
	if !found {
		return "", fmt.Errorf("no background job %q", p.JobID)
	}
	if p.Filter != "" && text != "" {
		filtered, err := filterLines(text, p.Filter)
		if err != nil {
			return "", err
		}
		text = filtered
	}
	header := fmt.Sprintf("[%s] %s", p.JobID, status)
	if strings.TrimSpace(text) == "" {
		return header + "\n(no new output)", nil
	}
	return header + "\n" + text, nil
}

// filterLines keeps only the lines of s matching the regular expression re.
// re is limited to 512 characters to prevent ReDoS attacks.
func filterLines(s, re string) (string, error) {
	const maxPatternLen = 512
	if len(re) > maxPatternLen {
		return "", fmt.Errorf("filter regexp too long (%d chars, max %d)", len(re), maxPatternLen)
	}
	rx, err := regexp.Compile(re)
	if err != nil {
		return "", fmt.Errorf("invalid filter regexp: %w", err)
	}
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if rx.MatchString(line) {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n"), nil
}

// --- kill_shell: terminate a running background job ---

type killShell struct{}

func (killShell) Name() string { return "kill_shell" }

func (killShell) Description() string {
	return "Terminate a running background job (bash or task) started with run_in_background. A no-op if the job has already finished or the id is unknown."
}

func (killShell) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"The background job id to terminate (e.g. \"bash-1\")."}},"required":["job_id"]}`)
}

func (killShell) ReadOnly() bool { return false }

func (killShell) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
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
		return fmt.Sprintf("Killed background job %q.", p.JobID), nil
	}
	return fmt.Sprintf("Background job %q was not running (already finished or unknown).", p.JobID), nil
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
func (cancelJob) Description() string { return "Cancel a running background job" }
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

func (peekJob) Name() string        { return "peek-job" }
func (peekJob) Description() string { return "Peek at the status of a background job" }
func (peekJob) ReadOnly() bool      { return true }
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
	b, err := json.Marshal(status)
	if err != nil {
		return "", fmt.Errorf("marshal status: %w", err)
	}
	return string(b), nil
}

// --- send_message: send a message from a background sub-agent to its parent ---

type sendMessage struct{}

func (sendMessage) Name() string        { return "send_message" }
func (sendMessage) Description() string { return "Send a message/report from a background sub-agent to its parent agent" }
func (sendMessage) ReadOnly() bool      { return false }
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
