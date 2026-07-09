package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskTool_PostCallGuidance_Short(t *testing.T) {
	var tt TaskTool
	g := strings.TrimSpace(tt.PostCallGuidance(json.RawMessage(`{"prompt":"x"}`)))
	if g == "" {
		t.Fatal("PostCallGuidance should be non-empty for task")
	}
	if strings.Contains(g, "task_result") || strings.Contains(g, "get_result") {
		t.Fatalf("guidance must not name phantom tools, got: %q", g)
	}
}

func TestTaskTool_PostCallGuidanceAfter_EmbedsJobID(t *testing.T) {
	var tt TaskTool
	rich := FormatStartedTaskResult("task-42", "explore")
	g := strings.TrimSpace(tt.PostCallGuidanceAfter(json.RawMessage(`{"prompt":"x"}`), rich))
	if !strings.Contains(g, "task-42") {
		t.Fatalf("guidance should embed job id, got: %q", g)
	}
	if strings.Contains(g, "task_result") {
		t.Fatalf("guidance must not name phantom tools, got: %q", g)
	}
}
