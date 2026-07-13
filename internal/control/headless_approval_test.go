package control

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/permission"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// runHeadlessWriteOnce drives one write_file tool call through a headless gate in
// the given mode, with the given explicit ask rules, and reports how many
// approval prompts were emitted (must always be 0 headless) and which paths were
// actually written. It fails the test if the turn blocks (a wrongful prompt would
// hang forever under the zero approval timeout).
func runHeadlessWriteOnce(t *testing.T, mode string, askRules []string) (prompts int, written []string) {
	t.Helper()
	writer := &recordingWriter{}
	reg := tool.NewRegistry()
	reg.Add(writer)

	prov := &scriptedTurns{turns: [][]provider.Chunk{
		toolCallTurn("c1", "write_file", `{"path":"a.txt"}`),
		textTurn("Done."),
	}}
	ag := agent.New(prov, reg, agent.NewSession(""), agent.Options{}, event.Discard)

	c := New(Options{
		Runner:   ag,
		Executor: ag,
		Policy:   permission.New("ask", nil, askRules, nil),
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				prompts++
			}
		}),
		// ApprovalTimeout intentionally zero: a wrongful prompt would block forever.
	})
	c.ApplyHeadlessApprovalMode(mode)

	done := make(chan error, 1)
	go func() { done <- c.runTurnWithRaw(context.Background(), "edit", "edit") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runTurnWithRaw: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("headless %s must not block on a write", mode)
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return prompts, append([]string(nil), writer.paths...)
}

// TestApplyHeadlessApprovalModeAutoDeniesExplicitAskRule pins the corrected auto
// contract: a command the config explicitly marked "ask" must NOT run silently
// under headless auto (there is no one to approve it), yet must not prompt or
// hang either. auto preserves explicit ask rules by failing closed.
func TestApplyHeadlessApprovalModeAutoDeniesExplicitAskRule(t *testing.T) {
	prompts, written := runHeadlessWriteOnce(t, ToolApprovalAuto, []string{"write_file"})
	if prompts != 0 {
		t.Fatalf("approval prompts = %d, want 0 (headless run has no UI to answer)", prompts)
	}
	if len(written) != 0 {
		t.Fatalf("executed writes = %v, want none (auto must not silently run an explicit ask rule)", written)
	}
}

// TestApplyHeadlessApprovalModeAutoAllowsWriterFallback confirms auto still
// auto-approves the ordinary writer fallback (no explicit rule): that is the
// permissiveness auto is meant to add over the default headless gate.
func TestApplyHeadlessApprovalModeAutoAllowsWriterFallback(t *testing.T) {
	prompts, written := runHeadlessWriteOnce(t, ToolApprovalAuto, nil)
	if prompts != 0 {
		t.Fatalf("approval prompts = %d, want 0", prompts)
	}
	if len(written) != 1 || written[0] != "a.txt" {
		t.Fatalf("executed writes = %v, want a.txt (auto auto-approves the writer fallback)", written)
	}
}

// TestApplyHeadlessApprovalModeYoloBypassesAskRule confirms only bypass runs an
// explicitly ask-gated command unattended.
func TestApplyHeadlessApprovalModeYoloBypassesAskRule(t *testing.T) {
	prompts, written := runHeadlessWriteOnce(t, ToolApprovalYolo, []string{"write_file"})
	if prompts != 0 {
		t.Fatalf("approval prompts = %d, want 0", prompts)
	}
	if len(written) != 1 || written[0] != "a.txt" {
		t.Fatalf("executed writes = %v, want a.txt (bypass runs even explicit ask rules)", written)
	}
}

// TestApplyHeadlessApprovalModeDontAskDeniesWithoutPrompting checks that dontAsk
// denies a would-ask tool through the non-blocking deny approver rather than
// emitting a prompt or hanging.
func TestApplyHeadlessApprovalModeDontAskDeniesWithoutPrompting(t *testing.T) {
	writer := &recordingWriter{}
	reg := tool.NewRegistry()
	reg.Add(writer)

	prov := &scriptedTurns{turns: [][]provider.Chunk{
		toolCallTurn("c1", "write_file", `{"path":"a.txt"}`),
		textTurn("Done."),
	}}
	ag := agent.New(prov, reg, agent.NewSession(""), agent.Options{}, event.Discard)

	prompts := 0
	c := New(Options{
		Runner:   ag,
		Executor: ag,
		Policy:   permission.New("ask", nil, []string{"write_file"}, nil),
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				prompts++
			}
		}),
	})
	c.ApplyHeadlessApprovalMode(ToolApprovalDontAsk)

	done := make(chan error, 1)
	go func() { done <- c.runTurnWithRaw(context.Background(), "edit", "edit") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runTurnWithRaw: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("headless dontAsk must not block")
	}
	if prompts != 0 {
		t.Fatalf("approval prompts = %d, want 0 (dontAsk denies silently)", prompts)
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.paths) != 0 {
		t.Fatalf("executed writes = %v, want none (dontAsk must deny)", writer.paths)
	}
}

// TestInteractiveGateIgnoresSessionAllowForFreshHumanTools guards the memory
// contract: --allowed-tools (SessionAllow) must never satisfy a tool that
// requires fresh human approval every call, even though SessionAllow outranks Ask
// rules for ordinary tools.
func TestInteractiveGateIgnoresSessionAllowForFreshHumanTools(t *testing.T) {
	policy := permission.New("ask", nil, nil, nil).
		WithSessionAllow([]string{"remember", "forget", "write_file"})
	c := New(Options{Policy: policy})

	gate := c.newInteractiveGate()
	for _, name := range []string{memoryRememberTool, memoryForgetTool} {
		if got := gate.Policy.DecideSubject(name, false, ""); got != permission.Ask {
			t.Fatalf("%s decision = %v, want Ask (SessionAllow must not cover fresh-human tools)", name, got)
		}
	}
	// An ordinary tool in the allowlist is still honored.
	if got := gate.Policy.DecideSubject("write_file", false, ""); got != permission.Allow {
		t.Fatalf("write_file decision = %v, want Allow (SessionAllow still applies to ordinary tools)", got)
	}
}
