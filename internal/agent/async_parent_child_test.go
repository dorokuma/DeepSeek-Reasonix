package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

func TestTaskToolNotModelFacing(t *testing.T) {
	reg := tool.NewRegistry()
	tt := NewTaskTool(nil, nil, reg, 0, 0, 0, 0, 0, "", "", nil, nil, nil)
	reg.Add(tt)
	if _, ok := reg.Get("task"); ok {
		t.Fatal("empty-name kernel must not register as task")
	}
	_, err := tt.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "spawn_agent") {
		t.Fatalf("kernel Execute should refuse, got %v", err)
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
