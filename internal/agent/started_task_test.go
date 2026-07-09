package agent

import (
	"strings"
	"testing"
)

func TestFormatAndExtractStartedTask(t *testing.T) {
	line := FormatStartedTaskResult("task-42", "explore")
	if got := ExtractJobIDFromStartedResult(line); got != "task-42" {
		t.Fatalf("rich extract got %q", got)
	}
	if !IsStartedTaskPlaceholder(line) {
		t.Fatal("should be placeholder")
	}
	if !strings.Contains(line, "ACCEPTED") || !strings.Contains(line, "tool_result: COMPLETE") {
		t.Fatalf("receipt must look complete, got:\n%s", line)
	}
	if !strings.Contains(line, `"tool_call_complete":true`) {
		t.Fatalf("machine payload should mark tool_call_complete, got:\n%s", line)
	}
	// Bare JSON still parses (history / tests).
	bare := `{"job_id":"task-9","status":"started","label":"x"}`
	if ExtractJobIDFromStartedResult(bare) != "task-9" || !IsStartedTaskPlaceholder(bare) {
		t.Fatal("bare JSON legacy form")
	}
	legacy := "Started task task-7 (x)"
	if ExtractJobIDFromStartedResult(legacy) != "task-7" {
		t.Fatal("legacy extract")
	}
	if !TaskToolContentReferencesJob(line, "task-42") {
		t.Fatal("TaskToolContentReferencesJob should match rich receipt")
	}
}
