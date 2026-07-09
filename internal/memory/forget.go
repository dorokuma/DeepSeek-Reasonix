package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

type forgetTool struct {
	store   Store
	isSkill NameExists
}

// NewForgetTool returns the memory_forget tool bound to store.
func NewForgetTool(store Store, isSkill ...NameExists) tool.Tool {
	t := forgetTool{store: store}
	if len(isSkill) > 0 {
		t.isSkill = isSkill[0]
	}
	return t
}

func (forgetTool) Name() string { return "memory_forget" }

func (forgetTool) Description() string {
	return "Delete a saved auto-memory fact by memory id so it stops loading into future sessions. " +
		"Use the id from the Saved memories index (memory/<id>). Prefer memory_save with the same id to update. " +
		"Not for skills — skills use install_skill / filesystem, not memory_forget."
}

func (forgetTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"memory": {"type": "string", "description": "Memory id to delete (memory/<id> or bare <id>). Not a skill name."}
		},
		"required": ["memory"]
	}`)
}

func (t forgetTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Memory string `json:"memory"`
		Name   string `json:"name"`
		Skill  string `json:"skill"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	raw := firstNonEmpty(in.Memory, in.Name, in.Skill)
	if raw == "" {
		return "", fmt.Errorf("memory_forget requires parameter \"memory\"")
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "skill/") {
		return "", fmt.Errorf("%q is a skill id — memory_forget only deletes memory/* entries", raw)
	}
	id := NormalizeMemoryID(raw)
	if id == "" {
		return "", fmt.Errorf("invalid memory id %q", raw)
	}
	if !t.store.Exists(id) {
		if t.isSkill != nil && t.isSkill(id) {
			return "", fmt.Errorf("%q is a SKILL (skill/%s), not a memory — cannot memory_forget a skill", id, id)
		}
		return "", fmt.Errorf("memory %q not found", id)
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
