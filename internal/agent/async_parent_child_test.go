package agent

import (
	"context"
	"encoding/json"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/tool"
)

func TestTaskToolRejectsAsyncSpawnFromSubagentDepth(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()

	reg := tool.NewRegistry()
	tt := NewTaskTool(nil, nil, reg, 0, 0, 0, 0, 0, "", "", nil, nil, nil)
	reg.Add(tt)

	ctx := WithNestingDepth(context.Background(), 1)
	ctx = jobs.WithManager(ctx, jm)

	_, err := tt.Execute(ctx, json.RawMessage(`{"prompt":"nested","description":"x"}`))
	if err == nil {
		t.Fatal("expected error when sub-agent depth tries to spawn another task")
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