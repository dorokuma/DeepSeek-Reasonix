package agent

import "testing"

func TestFormatAndExtractStartedTask(t *testing.T) {
	line := FormatStartedTaskResult("task-42", "explore")
	if got := ExtractJobIDFromStartedResult(line); got != "task-42" {
		t.Fatalf("json extract got %q", got)
	}
	if !IsStartedTaskPlaceholder(line) {
		t.Fatal("should be placeholder")
	}
	legacy := "Started task task-7 (x)"
	if ExtractJobIDFromStartedResult(legacy) != "task-7" {
		t.Fatal("legacy extract")
	}
}
