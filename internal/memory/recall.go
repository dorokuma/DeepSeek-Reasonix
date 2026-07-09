package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

// NameExists reports whether an identifier is claimed by another namespace (skills).
type NameExists func(id string) bool

type memoryGetTool struct {
	store   Store
	isSkill NameExists // optional cross-namespace guard
}

// NewRecallTool creates memory_get (legacy constructor name kept for boot call sites).
func NewRecallTool(store Store, isSkill ...NameExists) tool.Tool {
	t := memoryGetTool{store: store}
	if len(isSkill) > 0 {
		t.isSkill = isSkill[0]
	}
	return t
}

func (memoryGetTool) Name() string { return "memory_get" }

func (memoryGetTool) Description() string {
	return "Read a saved auto-memory fact by its memory id and return the full file body. " +
		"ONLY for entries listed under the Saved memories section as memory/<id> — never for Skills. " +
		"Pass parameter memory (e.g. \"prefers-tabs\" or \"memory/prefers-tabs\"). " +
		"Do NOT use read_skill/run_skill for memories. This does not modify any files."
}

func (memoryGetTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"memory": {"type": "string", "description": "Memory id from the Saved memories index (memory/<id> or bare <id>). Not a skill name."}
		},
		"required": ["memory"]
	}`)
}

func (t memoryGetTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Memory string `json:"memory"`
		// Accept mistaken skill-style or legacy fields so we can redirect clearly.
		Name  string `json:"name"`
		Skill string `json:"skill"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	raw := firstNonEmpty(in.Memory, in.Name, in.Skill)
	if raw == "" {
		return "", fmt.Errorf("memory_get requires parameter \"memory\" (a memory/<id> from the Saved memories index)")
	}
	// Hard reject skill namespace paste.
	if strings.HasPrefix(strings.TrimSpace(raw), "skill/") {
		return "", fmt.Errorf("%q looks like a SKILL id — use read_skill({skill:%q}) or run_skill; memory_get is only for memory/* entries", raw, strings.TrimPrefix(strings.TrimSpace(raw), "skill/"))
	}
	id := NormalizeMemoryID(raw)
	if id == "" {
		return "", fmt.Errorf("invalid memory id %q", raw)
	}
	if t.isSkill != nil && t.isSkill(id) && !t.store.Exists(id) {
		return "", fmt.Errorf("%q is a SKILL (skill/%s), not a memory — use read_skill({skill:%q}) or run_skill; memory_get is only for memory/*", id, id, id)
	}
	body, err := t.store.Read(id)
	if err != nil {
		if t.isSkill != nil && t.isSkill(id) {
			return "", fmt.Errorf("memory %q not found; skill/%s exists — use read_skill({skill:%q}) instead of memory_get", id, id, id)
		}
		return "", err
	}
	return body, nil
}

func (memoryGetTool) ReadOnly() bool { return true }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
