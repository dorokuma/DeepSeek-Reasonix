package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/multiagent"
	"reasonix/internal/tool"
)

func TestMultiAgentV1ToolsRegistered(t *testing.T) {
	reg := tool.NewRegistry()
	multiagent.RegisterTools(reg)
	for _, name := range []string{"spawn_agent", "send_input", "wait_agent", "close_agent", "resume_agent"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("missing tool %s", name)
		}
	}
	for _, gone := range []string{"followup_task", "interrupt_agent", "send_message", "list_agents"} {
		if _, ok := reg.Get(gone); ok {
			t.Fatalf("non-V1 tool %s must not be registered", gone)
		}
	}
}

func TestGetSchemasKeepsAllMetaTools(t *testing.T) {
	c := multiagent.NewControl()
	reg := tool.NewRegistry()
	multiagent.RegisterTools(reg)
	reg.Add(stubSchemaTool{name: "read_file"})

	a := &Agent{tools: reg, multiAgent: c}
	schemas := a.getSchemasForContext(context.Background())
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		names = append(names, s.Name)
	}
	for _, want := range []string{"spawn_agent", "send_input", "wait_agent", "close_agent", "resume_agent", "read_file"} {
		if !containsName(names, want) {
			t.Fatalf("%s must remain visible; got %v", want, names)
		}
	}
}

type stubSchemaTool struct{ name string }

func (s stubSchemaTool) Name() string        { return s.name }
func (s stubSchemaTool) Description() string { return s.name }
func (s stubSchemaTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s stubSchemaTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "ok", nil
}
func (s stubSchemaTool) ReadOnly() bool { return true }

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestSendInputDescriptionMatchesCodexReuse(t *testing.T) {
	reg := tool.NewRegistry()
	multiagent.RegisterTools(reg)
	tl, ok := reg.Get("send_input")
	if !ok {
		t.Fatal("send_input missing")
	}
	if !strings.Contains(tl.Description(), "interrupt=true") {
		t.Fatalf("want Codex interrupt guidance: %s", tl.Description())
	}
	if !strings.Contains(tl.Description(), "reuse") {
		t.Fatalf("want Codex reuse guidance: %s", tl.Description())
	}
}

func TestSpawnAgentDescriptionHasCodexStyleGuidance(t *testing.T) {
	reg := tool.NewRegistry()
	multiagent.RegisterTools(reg)
	tl, ok := reg.Get("spawn_agent")
	if !ok {
		t.Fatal("spawn_agent missing")
	}
	d := tl.Description()
	for _, needle := range []string{
		"Do not spawn",
		"wait_agent very sparingly",
		"close_agent",
		"send_input",
		"cannot spawn further",
		"REASONIX.md",
	} {
		if !strings.Contains(d, needle) {
			t.Fatalf("spawn_agent description missing %q", needle)
		}
	}
}
