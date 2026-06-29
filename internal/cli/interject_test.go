package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

func TestInterjectQueuesWhileRunningWithoutOverwrite(t *testing.T) {
	r := &blockingTurnRunner{started: make(chan struct{})}
	ctrl := control.New(control.Options{Runner: r, Sink: event.Discard, SessionDir: t.TempDir(), Label: "test"})
	m := newTestChatTUI()
	m.ctrl = ctrl
	m.state = tuiRunning

	m.input.SetValue("first")
	m0, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m0.(chatTUI)

	m.input.SetValue("second")
	m0, _ = m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m0.(chatTUI)

	m.input.SetValue("")
	m0, _ = m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m0.(chatTUI)

	// Enter while running steers the input into the running turn; pendingInterject
	// is unchanged.
	if len(m.pendingInterject) != 0 {
		t.Fatalf("pendingInterject should remain empty after steer, got %d items", len(m.pendingInterject))
	}
	if m.state != tuiRunning {
		t.Fatalf("steering input must not change state; got %v", m.state)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input should be reset after steer, got %q", got)
	}
}

func TestInterjectDequeuesFrontOnTurnDone(t *testing.T) {
	r := &blockingTurnRunner{started: make(chan struct{})}
	ctrl := control.New(control.Options{Runner: r, Sink: event.Discard, SessionDir: t.TempDir(), Label: "test"})
	m := newChatTUI(ctrl, "", make(chan event.Event, 8), 80)
	m.state = tuiRunning
	m.pendingInterject = []string{"first", "second"}

	m0, _ := m.update(agentEventMsg(event.Event{Kind: event.TurnDone}))
	m = m0.(chatTUI)

	if len(m.pendingInterject) != 1 || m.pendingInterject[0] != "second" {
		t.Fatalf("TurnDone should dequeue only the front; got %v", m.pendingInterject)
	}
}
