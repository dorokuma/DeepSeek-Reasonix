package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

type rememberTool struct{ store Store }

// NewRememberTool returns the remember tool bound to store.
func NewRememberTool(store Store) tool.Tool { return rememberTool{store: store} }

func (rememberTool) Name() string { return "remember" }

func (rememberTool) Description() string {
	return "Save a durable auto-memory fact as memory/<id> so it survives across sessions. " +
		"Parameter memory is the kebab-case id (optional if title/description can derive it). " +
		"Not for skill playbooks — those use install_skill / run_skill. " +
		"Reuse the same memory id to update; use forget to remove."
}

func (rememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"memory": {"type": "string", "description": "Kebab-case memory id, e.g. \"prefers-tabs\". Reusing updates that fact. Optional if title/description provided."},
			"name": {"type": "string", "description": "Deprecated alias of memory."},
			"title": {"type": "string", "description": "Short label shown in the memory index."},
			"description": {"type": "string", "description": "One-line hook in the memory index."},
			"type": {"type": "string", "enum": ["user", "feedback", "project", "reference"], "description": "Category of the fact."},
			"body": {"type": "string", "description": "The fact itself (Markdown)."}
		},
		"required": ["description", "body"]
	}`)
}

func (t rememberTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Memory      string `json:"memory"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Description == "" || in.Body == "" {
		return "", fmt.Errorf("description and body are required")
	}
	raw := strings.TrimSpace(in.Memory)
	if raw == "" {
		raw = strings.TrimSpace(in.Name)
	}
	if raw == "" {
		raw = strings.TrimSpace(in.Title)
	}
	if raw == "" {
		raw = strings.TrimSpace(in.Description)
	}
	if strings.HasPrefix(raw, "skill/") {
		return "", fmt.Errorf("remember cannot use skill/* ids")
	}
	id := NormalizeMemoryID(raw)
	if id == "" {
		return "", fmt.Errorf("could not derive a memory id")
	}
	path, err := t.store.Save(Memory{
		Name:        id,
		Title:       in.Title,
		Description: in.Description,
		Type:        NormalizeType(in.Type),
		Body:        in.Body,
	})
	if err != nil {
		return "", err
	}
	if q, ok := QueueFromContext(ctx); ok {
		q.QueueMemory("Saved memory \"" + MemoryNamespace + id + "\": " + oneLine(in.Description))
	}
	return fmt.Sprintf("Saved %s%s to %s (applies now; future sessions list it under Saved memories).", MemoryNamespace, id, path), nil
}

func (rememberTool) ReadOnly() bool { return false }
