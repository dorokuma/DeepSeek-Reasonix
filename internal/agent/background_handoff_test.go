package agent

import (
	"testing"

	"reasonix/internal/provider"
)

func TestMaybeNudgeBackgroundWake(t *testing.T) {
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleTool, Name: "task", Content: FormatStartedTaskResult("task-1", "x")})
	s.Add(provider.Message{Role: provider.RoleTool, Name: "task", ToolCallID: "c1", Content: "done"})
	ag := &Agent{session: s}
	before := len(s.Snapshot())
	ag.maybeNudgeBackgroundWake(true, 0)
	if len(s.Snapshot()) != before+1 {
		t.Fatalf("expected user nudge message")
	}
}
