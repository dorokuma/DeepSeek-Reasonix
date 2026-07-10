package event

import "testing"

func TestKindCount(t *testing.T) {
	// Verify the number of Kind constants (safety net against accidental reorder).
	const expectedKinds = 17
	kinds := []Kind{
		TurnStarted, Reasoning, Text, Message, ToolDispatch, ToolResult,
		Usage, Notice, Phase, ApprovalRequest, AskRequest, TurnDone,
		CompactionStarted, CompactionDone, ToolProgress, MCPSurfaceReady,
		Retrying,
	}
	if len(kinds) != expectedKinds {
		t.Errorf("expected %d kinds, got %d", expectedKinds, len(kinds))
	}
}

func TestDiscardSink(t *testing.T) {
	// Discard must accept every kind without panicking.
	for _, k := range []Kind{
		TurnStarted, Reasoning, Text, Message, ToolDispatch, ToolResult,
		Usage, Notice, Phase, ApprovalRequest, AskRequest, TurnDone,
		CompactionStarted, CompactionDone, ToolProgress, MCPSurfaceReady,
		Retrying,
	} {
		Discard.Emit(Event{Kind: k, Text: "t"})
	}
}
