package agent

import (
	"encoding/json"
	"testing"
)

func TestTaskTool_NoPostCallGuidance(t *testing.T) {
	// Sync task returns the answer in the tool result; no extra wait/poll sermon.
	var tt TaskTool
	if _, ok := any(&tt).(interface {
		PostCallGuidance(json.RawMessage) string
	}); ok {
		// TaskTool must not implement PostCallGuidance after the sync cutover.
		// If someone re-adds it, this test fails loudly.
		t.Fatal("TaskTool should not implement PostCallGuidance")
	}
}
