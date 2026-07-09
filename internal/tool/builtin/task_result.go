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
// registered so request validators accept the synthetic history tool name, and
// so the model never invents a call to a phantom tool.
func init() {
	tool.RegisterBuiltin(taskResultGuard{})
}

type taskResultGuard struct{}

func (taskResultGuard) Name() string { return taskResultToolName }
func (taskResultGuard) Description() string {
	return `System delivery channel for finished background sub-agents (task tool).

When a background task completes, the runtime appends a paired tool round with this name at the conversation tail — you will see it automatically. Do NOT call this tool yourself. Wait for that automatic result instead of polling or re-dispatching.`
}
func (taskResultGuard) ReadOnly() bool { return true }
func (taskResultGuard) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "Unused — system fills this on auto-delivery"}
		}
	}`)
}

func (taskResultGuard) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("%s is delivered automatically when a background sub-agent finishes — do not invoke it; wait for the automatic result at the conversation tail", taskResultToolName)
}
