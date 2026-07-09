package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

func TestTaskToolExecuteAlwaysRefuses(t *testing.T) {
	reg := tool.NewRegistry()
	tt := NewTaskTool(nil, nil, reg, 0, 0, 0, 0, 0, "", "", nil, nil, nil)
	// Must not be registerable as a model tool.
	reg.Add(tt)
	if _, ok := reg.Get("task"); ok {
		t.Fatal("registry must not accept legacy task tool")
	}
	if _, ok := reg.Get(""); ok {
		t.Fatal("registry must not accept empty-name tool")
	}
	_, err := tt.Execute(context.Background(), json.RawMessage(`{"prompt":"nested","description":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "spawn_agent") {
		t.Fatalf("expected refuse with spawn_agent redirect, got %v", err)
	}
}

func TestMaySpawnAsyncSubagentOnlyAtMainDepth(t *testing.T) {
	if !MaySpawnAsyncSubagent(context.Background()) {
		t.Fatal("main depth should allow async spawn")
	}
	if MaySpawnAsyncSubagent(WithNestingDepth(context.Background(), 1)) {
		t.Fatal("depth 1 must not spawn async sub-agents")
	}
}

func TestRemovedToolRedirect(t *testing.T) {
	if removedToolRedirect("task") == "" {
		t.Fatal("task must be blocked")
	}
	if removedToolRedirect("spawn_agent") != "" {
		t.Fatal("spawn_agent must not be blocked")
	}
}
