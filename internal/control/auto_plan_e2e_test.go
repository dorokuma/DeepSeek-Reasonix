package control

import (
	"context"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// scriptedTurns is a provider that replays a distinct chunk set per Stream call,
// so a controller turn that re-enters the agent (plan turn, then approved
// execution turn) sees a different model response each time.
type scriptedTurns struct {
	turns [][]provider.Chunk
	call  int
}

func (s *scriptedTurns) Name() string { return "scripted" }

func (s *scriptedTurns) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	i := s.call
	if i >= len(s.turns) {
		i = len(s.turns) - 1
	}
	s.call++
	ch := make(chan provider.Chunk, len(s.turns[i]))
	for _, c := range s.turns[i] {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func firstUserMessage(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			return m.Content
		}
	}
	return ""
}

func textTurn(text string) []provider.Chunk {
	return []provider.Chunk{{Type: provider.ChunkText, Text: text}, {Type: provider.ChunkDone}}
}

func TestAutoPlanGateRejectionStaysInPlan(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Plan:\n1. Add the config field\n2. Add tests"),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)

	approvalID := make(chan string, 1)
	var seeded bool
	c := New(Options{
		AutoPlan: "on",
		Runner:   ag,
		Executor: ag,
		Sink: event.FuncSink(func(e event.Event) {
			switch e.Kind {
			case event.ApprovalRequest:
				approvalID <- e.Approval.ID
			case event.ToolDispatch:
				if e.Tool.ID == "plan-seed" {
					seeded = true
				}
			}
		}),
	})

	go func() { c.Approve(<-approvalID, false, false, false) }()

	input := "实现 issue #2395：新增配置项、自动判断复杂任务、补测试和文档"
	if err := c.runTurnWithRaw(context.Background(), input, input); err != nil {
		t.Fatalf("runTurnWithRaw: %v", err)
	}

	if !c.PlanMode() {
		t.Fatal("rejected plan should keep plan mode on")
	}
	if seeded {
		t.Fatal("rejected plan must not seed the task list")
	}
	if prov.call != 1 {
		t.Fatalf("provider called %d times, want 1 (plan only, no execution)", prov.call)
	}
}
