package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskTool_PostCallGuidance_NonEmpty(t *testing.T) {
	var tt TaskTool
	g := strings.TrimSpace(tt.PostCallGuidance(json.RawMessage(`{"prompt":"x"}`)))
	if g == "" {
		t.Fatal("PostCallGuidance should be non-empty for task")
	}
	if !strings.Contains(g, "Background job results auto-deliver") {
		t.Fatalf("guidance should tell model not to poll, got: %q", g)
	}
}

func TestTaskTool_PostCallGuidanceAfter_EmbedsJobID(t *testing.T) {
	var tt TaskTool
	g := strings.TrimSpace(tt.PostCallGuidanceAfter(json.RawMessage(`{"prompt":"x"}`), "Started task task-42 (explore)"))
	if !strings.Contains(g, `job_id="task-42"`) {
		t.Fatalf("guidance should embed exact job id, got: %q", g)
	}
}
