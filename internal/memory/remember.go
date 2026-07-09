package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

// rememberTool lets the model persist a durable fact to the auto-memory store.
type rememberTool struct {
	store   Store
	isSkill NameExists
}

// NewRememberTool returns the memory_save tool bound to store.
func NewRememberTool(store Store, isSkill ...NameExists) tool.Tool {
	t := rememberTool{store: store}
	if len(isSkill) > 0 {
		t.isSkill = isSkill[0]
	}
	return t
}

func (rememberTool) Name() string { return "memory_save" }

func (rememberTool) Description() string {
	return "Save a durable fact to project auto-memory (memory/<id>) so it survives across sessions. " +
		"This is NOT a skill — do not use install_skill/run_skill for durable facts. " +
		"Use for long-term prefs (type \"user\"), work guidance (type \"feedback\"), project constraints (type \"project\"), or external pointers (type \"reference\"). " +
		"Pass memory as the kebab-case id (e.g. \"prefers-tabs\"); reusing an id updates that fact. " +
		"To drop a wrong fact use memory_forget. The saved index loads at each session start as memory/<id> lines."
}

func (rememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"memory": {"type": "string", "description": "Kebab-case memory id, e.g. \"prefers-tabs\". Reusing an id overwrites that memory. Optional — derived from title/description if omitted."},
			"name": {"type": "string", "description": "Deprecated alias of memory; prefer \"memory\"."},
			"title": {"type": "string", "description": "Short human-readable label shown in the memory index."},
			"description": {"type": "string", "description": "One-line hook shown in the index."},
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
	name := firstNonEmpty(in.Memory, in.Name, in.Title, in.Description)
	if strings.HasPrefix(strings.TrimSpace(name), "skill/") {
		return "", fmt.Errorf("memory_save cannot use a skill/ id — pick a memory/* id")
	}
	id := NormalizeMemoryID(name)
	if id == "" {
		return "", fmt.Errorf("could not derive a memory id")
	}
	if t.isSkill != nil && t.isSkill(id) {
		// Allow saving a memory that collides with a skill name, but warn in the result.
		// Collision is rare; still save under memory namespace.
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
	return fmt.Sprintf("Saved %s%s to %s (applies now; loads as a memory/* line in future sessions).", MemoryNamespace, id, path), nil
}

func (rememberTool) ReadOnly() bool { return false }
