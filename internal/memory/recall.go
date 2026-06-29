package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/tool"
)

type recallTool struct{ store Store }

// NewRecallTool creates a tool that reads a saved memory by name.
func NewRecallTool(store Store) tool.Tool { return recallTool{store: store} }

func (recallTool) Name() string { return "recall" }

func (recallTool) Description() string {
	return "Read a saved memory by name and return its full content. " +
		"Use the slug from the memory index — the \"<name>\" in \"[label](<name>.md)\". " +
		"Use this when you need to verify a memory's details before acting on it, " +
		"or when a memory's title/description in the index is ambiguous and you need the full body. " +
		"This is a read-only operation — it does not modify any files."
}

func (recallTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Slug of the memory to read, as shown in the index (the \"<name>\" in \"[label](<name>.md)\")."}
		},
		"required": ["name"]
	}`)
}

func (t recallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	return t.store.Read(in.Name)
}

func (recallTool) ReadOnly() bool { return true }
