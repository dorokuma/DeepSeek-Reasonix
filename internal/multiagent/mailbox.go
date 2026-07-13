package multiagent

import (
	"strings"
	"sync"
)

// Activity is what wait_agent subscribes to (Codex InputQueueActivity).
type Activity int

const (
	ActivityMailbox Activity = iota
	ActivitySteer
)

// Mail is one InterAgentCommunication (Codex).
type Mail struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Message     string `json:"message"`
	TriggerTurn bool   `json:"trigger_turn"`
}

// Mailbox is session-scoped pending inter-agent mail + activity fan-out.
type Mailbox struct {
	mu       sync.Mutex
	pending  []Mail
	waiters  []chan Activity
	closed   bool
}

func NewMailbox() *Mailbox {
	return &Mailbox{}
}

// Enqueue appends mail and notifies waiters (Codex enqueue_mailbox_communication).
func (m *Mailbox) Enqueue(mail Mail) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.pending = append(m.pending, mail)
	m.broadcastLocked(ActivityMailbox)
}

// NotifySteer wakes wait_agent with Steered (user input).
func (m *Mailbox) NotifySteer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.broadcastLocked(ActivitySteer)
}

func (m *Mailbox) broadcastLocked(a Activity) {
	for _, ch := range m.waiters {
		select {
		case ch <- a:
		default:
			// non-blocking: waiter will re-check pending
		}
	}
}

// Subscribe returns a channel of activity; caller must Unsubscribe.
// pending is set if mail or... we only signal mailbox pending at subscribe time.
func (m *Mailbox) Subscribe() (ch <-chan Activity, pending *Activity, unsub func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := make(chan Activity, 4)
	m.waiters = append(m.waiters, c)
	var p *Activity
	if len(m.pending) > 0 {
		a := ActivityMailbox
		p = &a
	}
	unsub = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for i, w := range m.waiters {
			if w == c {
				m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
				break
			}
		}
	}
	return c, p, unsub
}

// HasPending reports whether any mail is queued (session-wide).
func (m *Mailbox) HasPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending) > 0
}

// HasPendingFor reports whether mail is queued for a recipient path.
// Empty recipient or RootPath matches RootPath and legacy empty To.
func (m *Mailbox) HasPendingFor(recipient string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(filterPending(m.pending, recipient)) > 0
}

// HasTriggerTurn reports whether any mail wants an automatic turn.
func (m *Mailbox) HasTriggerTurn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mail := range m.pending {
		if mail.TriggerTurn {
			return true
		}
	}
	return false
}

// Drain removes and returns all pending mail.
func (m *Mailbox) Drain() []Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.pending
	m.pending = nil
	return out
}

// DrainFor removes and returns mail addressed to recipient (Codex per-agent drain).
// Other recipients stay queued so nested agents do not steal parent mail.
func (m *Mailbox) DrainFor(recipient string) []Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep, out := splitPending(m.pending, recipient)
	m.pending = keep
	return out
}

// Peek copies pending without draining.
func (m *Mailbox) Peek() []Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Mail, len(m.pending))
	copy(out, m.pending)
	return out
}

func normalizeRecipient(recipient string) string {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return RootPath
	}
	return strings.TrimSuffix(recipient, "/")
}

func mailMatchesRecipient(to, recipient string) bool {
	recipient = normalizeRecipient(recipient)
	to = strings.TrimSpace(to)
	if to == "" {
		// Legacy / root-bound completions without explicit To.
		return recipient == RootPath
	}
	to = strings.TrimSuffix(to, "/")
	return to == recipient
}

func filterPending(pending []Mail, recipient string) []Mail {
	var out []Mail
	for _, mail := range pending {
		if mailMatchesRecipient(mail.To, recipient) {
			out = append(out, mail)
		}
	}
	return out
}

func splitPending(pending []Mail, recipient string) (keep, out []Mail) {
	for _, mail := range pending {
		if mailMatchesRecipient(mail.To, recipient) {
			out = append(out, mail)
		} else {
			keep = append(keep, mail)
		}
	}
	return keep, out
}
