package control

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// TestBypassAllowsToolCallWithoutPrompting verifies that with bypass on, tool
// approval requests are auto-allowed without emitting an ApprovalRequest event.
func TestBypassAllowsToolCallWithoutPrompting(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Done."),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)

	var approvalRequested bool
	c := New(Options{
		Runner:   ag,
		Executor: ag,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				approvalRequested = true
			}
		}),
	})
	c.SetBypass(true)

	input := "Do something"
	if err := c.runTurnWithRaw(context.Background(), input, input); err != nil {
		t.Fatalf("runTurnWithRaw: %v", err)
	}

	if approvalRequested {
		t.Fatal("bypass must not emit an approval request")
	}
	if prov.call != 1 {
		t.Fatalf("provider called %d times, want 1", prov.call)
	}
}

// TestRequestApprovalHonorsBypass guards the underlying gate: the approval
// path routes through requestApproval. Under bypass it must return allow
// immediately without emitting anything.
func TestRequestApprovalHonorsBypass(t *testing.T) {
	var approvalRequested bool
	c := New(Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				approvalRequested = true
			}
		}),
	})
	c.SetBypass(true)

	done := make(chan bool, 1)
	go func() {
		allow, _, err := c.requestApproval(context.Background(), "write_file", "some/path", "", "gate")
		if err != nil {
			t.Errorf("requestApproval: %v", err)
		}
		done <- allow
	}()

	select {
	case allow := <-done:
		if !allow {
			t.Fatal("bypass should auto-allow the approval")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestApproval blocked under bypass; it must auto-allow without prompting")
	}

	if approvalRequested {
		t.Fatal("bypass must not emit an ApprovalRequest event")
	}
}

// TestSetBypassAllowsNewApprovals verifies that after turning bypass on, new
// approval requests are auto-allowed without blocking.
func TestSetBypassAllowsNewApprovals(t *testing.T) {
	c, _, _ := approvalIDs()
	c.SetBypass(true)

	// A new approval request should be auto-allowed without blocking.
	done := make(chan bool, 1)
	go func() {
		allow, _, err := c.requestApproval(context.Background(), "multi_edit", "/tmp/file", "", "gate")
		if err != nil {
			t.Errorf("requestApproval: %v", err)
		}
		done <- allow
	}()

	select {
	case allow := <-done:
		if !allow {
			t.Fatal("bypass should auto-allow the approval")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestApproval blocked under bypass; it must auto-allow without prompting")
	}
	if !c.Bypass() {
		t.Fatal("bypass should remain on")
	}
}

// TestSetModeYoloDrainsPendingApproval is the SetMode-path twin of the SetBypass
func TestBypassDoesNotAutoAnswerAsk(t *testing.T) {
	askCh := make(chan event.Ask, 1)
	c := New(Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.AskRequest {
				askCh <- e.Ask
			}
		}),
	})
	c.SetBypass(true)

	questions := []event.AskQuestion{
		{
			ID:     "approach",
			Header: "Approach",
			Prompt: "Which path?",
			Options: []event.AskOption{
				{Label: "Recommended path"},
				{Label: "Alternative path"},
			},
		},
		{
			ID:     "scope",
			Header: "Scope",
			Prompt: "How broad?",
			Options: []event.AskOption{
				{Label: "Minimal"},
				{Label: "Broad"},
			},
			Multi: true,
		},
	}

	done := make(chan struct {
		answers []event.AskAnswer
		err     error
	}, 1)
	go func() {
		answers, err := c.Ask(context.Background(), questions)
		done <- struct {
			answers []event.AskAnswer
			err     error
		}{answers, err}
	}()

	// Even with bypass on, Ask must emit an AskRequest and wait for the user.
	var ask event.Ask
	select {
	case ask = <-askCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not emit AskRequest under bypass; bypass should not auto-answer ask")
	}

	// Answer with NON-recommended options to prove the user was consulted.
	c.AnswerQuestion(ask.ID, []event.AskAnswer{
		{QuestionID: "approach", Selected: []string{"Alternative path"}},
		{QuestionID: "scope", Selected: []string{"Broad"}},
	})

	var result struct {
		answers []event.AskAnswer
		err     error
	}
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Ask stayed blocked after AnswerQuestion")
	}
	if result.err != nil {
		t.Fatalf("Ask: %v", result.err)
	}

	wantAnswers := []event.AskAnswer{
		{QuestionID: "approach", Selected: []string{"Alternative path"}},
		{QuestionID: "scope", Selected: []string{"Broad"}},
	}
	if len(result.answers) != len(wantAnswers) {
		t.Fatalf("answers len = %d, want %d: %#v", len(result.answers), len(wantAnswers), result.answers)
	}
	for i := range wantAnswers {
		if result.answers[i].QuestionID != wantAnswers[i].QuestionID ||
			len(result.answers[i].Selected) != 1 ||
			result.answers[i].Selected[0] != wantAnswers[i].Selected[0] {
			t.Fatalf("answers[%d] = %#v, want %#v", i, result.answers[i], wantAnswers[i])
		}
	}
}

func TestAskSerializesBehindPromptLockEvenWithBypass(t *testing.T) {
	askCh := make(chan event.Ask, 1)
	c := New(Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.AskRequest {
				askCh <- e.Ask
			}
		}),
	})
	questions := []event.AskQuestion{{
		ID:     "q1",
		Header: "Choice",
		Prompt: "Which path?",
		Options: []event.AskOption{
			{Label: "Recommended"},
			{Label: "Alternative"},
		},
	}}

	c.promptMu.Lock()
	done := make(chan []event.AskAnswer, 1)
	errs := make(chan error, 1)
	go func() {
		answers, err := c.Ask(context.Background(), questions)
		if err != nil {
			errs <- err
			return
		}
		done <- answers
	}()
	// Give the goroutine time to block on promptMu.
	time.Sleep(20 * time.Millisecond)

	// Pre-unlock assertion: while promptMu is still held, Ask must NOT have
	// emitted AskRequest — proving it truly serialized behind the lock.
	select {
	case <-askCh:
		t.Fatal("Ask emitted AskRequest before acquiring promptMu; it did not serialize behind the lock")
	default:
	}

	// Enable bypass while Ask is queued behind promptMu.
	c.SetBypass(true)
	// Release the lock — Ask proceeds but must still emit an AskRequest.
	c.promptMu.Unlock()

	// Post-unlock assertion: Ask must emit AskRequest now that it holds the lock.
	var ask event.Ask
	select {
	case ask = <-askCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not emit AskRequest after acquiring promptMu with bypass on; bypass should not suppress ask")
	}

	// Answer and verify we get the user's choice.
	c.AnswerQuestion(ask.ID, []event.AskAnswer{
		{QuestionID: "q1", Selected: []string{"Alternative"}},
	})

	var answers []event.AskAnswer
	select {
	case answers = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Ask stayed blocked after AnswerQuestion")
	}
	if len(answers) != 1 || answers[0].QuestionID != "q1" || len(answers[0].Selected) != 1 || answers[0].Selected[0] != "Alternative" {
		t.Fatalf("answers = %#v, want Alternative (user's choice, not auto-recommended)", answers)
	}
}
