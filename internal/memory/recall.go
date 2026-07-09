package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

type recallTool struct{ store Store }

// NewRecallTool creates the recall tool bound to store.
func NewRecallTool(store Store) tool.Tool { return recallTool{store: store} }

func (recallTool) Name() string { return "recall" }

func (recallTool) Description() string {
	return "Read a saved auto-memory fact (memory/<id>) and return its full body. " +
		"Required parameter: memory (from the Saved memories list only). " +
		"Not for Skills — skills use run_skill({skill:…}). Read-only."
}

func (recallTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"memory": {"type": "string", "description": "Memory id: memory/<id> or bare <id> from Saved memories. Never a skill/* id."}
		},
		"required": ["memory"]
	}`)
}

func (t recallTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, err := parseMemoryArg(args)
	if err != nil {
		return "", err
	}
	return t.store.Read(id)
}

func (recallTool) ReadOnly() bool { return true }

func parseMemoryArg(args json.RawMessage) (string, error) {
	var in struct {
		Memory string `json:"memory"`
		Name   string `json:"name"` // not documented; accept only if memory empty
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	raw := strings.TrimSpace(in.Memory)
	if raw == "" {
		raw = strings.TrimSpace(in.Name)
	}
	if raw == "" {
		return "", fmt.Errorf("requires parameter \"memory\" (memory/<id> from Saved memories)")
	}
	if strings.HasPrefix(raw, "skill/") {
		return "", fmt.Errorf("parameter memory=%q is not a memory id (skill/* uses run_skill)", raw)
	}
	id := NormalizeMemoryID(raw)
	if id == "" {
		return "", fmt.Errorf("invalid memory id %q", raw)
	}
	return id, nil
}
