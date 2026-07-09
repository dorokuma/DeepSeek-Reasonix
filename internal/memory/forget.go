package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/tool"
)

type forgetTool struct{ store Store }

// NewForgetTool returns the forget tool bound to store.
func NewForgetTool(store Store) tool.Tool { return forgetTool{store: store} }

func (forgetTool) Name() string { return "forget" }

func (forgetTool) Description() string {
	return "Delete a saved auto-memory fact by memory id so it stops loading. " +
		"Required parameter: memory (memory/<id> from Saved memories). Prefer remember with the same id to update. " +
		"Not for skills."
}

func (forgetTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"memory": {"type": "string", "description": "Memory id to delete (memory/<id> or bare <id>)."}
		},
		"required": ["memory"]
	}`)
}

func (t forgetTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	id, err := parseMemoryArg(args)
	if err != nil {
		return "", err
	}
	if err := t.store.Delete(id); err != nil {
		return "", err
	}
	if q, ok := QueueFromContext(ctx); ok {
		q.QueueMemory("Deleted memory \"" + MemoryNamespace + id + "\" — disregard its index line until next session.")
	}
	return fmt.Sprintf("Forgot %s%s (no longer loads in future sessions).", MemoryNamespace, id), nil
}

func (forgetTool) ReadOnly() bool { return false }
