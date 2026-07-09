package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/tool"
)

// Name must match agent.BackgroundDeliveryToolName ("task_result").
const taskResultToolName = "task_result"

// task_result is system-delivered when a background sub-agent finishes. It is
// registered so history validators and Execute can resolve the synthetic tool
// name, but OmitFromModelSchema keeps it out of provider tool lists — listing
// it as callable taught models to invent calls that always fail.
func init() {
	tool.RegisterBuiltin(taskResultGuard{})
}

type taskResultGuard struct{}

func (taskResultGuard) Name() string { return taskResultToolName }
func (taskResultGuard) Description() string {
	// Not sent to the model (OmitFromModelSchema); kept for registry dumps/tests.
	return "System-only delivery channel for finished background sub-agents. Not model-callable."
}
func (taskResultGuard) ReadOnly() bool             { return true }
func (taskResultGuard) OmitFromModelSchema() bool { return true }
func (taskResultGuard) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "System-filled on auto-delivery"}
		}
	}`)
}

func (taskResultGuard) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("%s is not a callable tool — the runtime appends it automatically when a background sub-agent finishes. Wait for that tail delivery; do not invent a %s call", taskResultToolName, taskResultToolName)
}
