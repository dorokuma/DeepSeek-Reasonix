package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskTool_RemovedFromModelSurface(t *testing.T) {
	var tt TaskTool
	if tt.Name() != "" {
		t.Fatalf("TaskTool must not expose a model-facing name, got %q", tt.Name())
	}
	_, err := tt.Execute(nil, json.RawMessage(`{"prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "spawn_agent") {
		t.Fatalf("Execute must refuse legacy task, got %v", err)
	}
}
