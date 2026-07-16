package multiagent

import (
	"strings"
	"sync"
)

// Activity is what wait_agent subscribes to.
type Activity int

const (
	ActivityMailbox Activity = iota
	ActivitySteer
)

// Mail is one inter-agent message.
type Mail struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Message     string `json:"message"`
	TriggerTurn bool   `json:"trigger_turn"`
}

// waiter is one wait_agent subscription. recipient filters mailbox wakes:
// empty recipient = any mail; otherwise only mail To matching recipient.
// Steer always wakes every waiter.
type waiter struct {
	ch        chan Activity
	recipient string
}

// Mailbox is session-scoped pending inter-agent mail + activity fan-out.
type Mailbox struct {
	mu      sync.Mutex
	pending []Mail
	waiters []waiter
	closed  bool
}

func NewMailbox() *Mailbox {
	return &Mailbox{}
}

// Enqueue appends mail and notifies matching waiters.
func (m *Mailbox) Enqueue(mail Mail) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.pending = append(m.pending, mail)
	m.broadcastLocked(ActivityMailbox, mail.To)
}

// NotifySteer wakes wait_agent with steered user input.
func (m *Mailbox) NotifySteer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.broadcastLocked(ActivitySteer, "")
}

// broadcastLocked wakes matching waiters. Buffer is 1 with coalesce so the
// latest signal is never permanently dropped while a waiter is blocked.
func (m *Mailbox) broadcastLocked(a Activity, mailTo string) {
	for _, w := range m.waiters {
		if a != ActivitySteer && w.recipient != "" && !mailMatchesRecipient(mailTo, w.recipient) {
			continue
		}
		// Coalesce: drop stale signal, then push latest.
		select {
		case <-w.ch:
		default:
		}
		select {
		case w.ch <- a:
		default:
		}
	}
}

// Subscribe is SubscribeFor("") — wake on any session mail.
func (m *Mailbox) Subscribe() (ch <-chan Activity, pending *Activity, unsub func()) {
	return m.SubscribeFor("")
}

// SubscribeFor returns a channel of activity filtered by recipient path.
// pending is set when matching mail is already queued (or any mail when recipient is empty).
// Steer always wakes. Caller must unsub.
func (m *Mailbox) SubscribeFor(recipient string) (ch <-chan Activity, pending *Activity, unsub func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := make(chan Activity, 1)
	w := waiter{ch: c, recipient: normalizeRecipient(recipient)}
	if strings.TrimSpace(recipient) == "" {
		w.recipient = ""
	}
	m.waiters = append(m.waiters, w)
	var p *Activity
	if w.recipient == "" {
		if len(m.pending) > 0 {
			a := ActivityMailbox
			p = &a
		}
	} else if len(filterPending(m.pending, w.recipient)) > 0 {
		a := ActivityMailbox
		p = &a
	}
	unsub = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for i, cur := range m.waiters {
			if cur.ch == c {
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

// DrainFor removes and returns mail addressed to recipient.
// Other recipients stay queued so nested agents do not steal parent mail.
func (m *Mailbox) DrainFor(recipient string) []Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep, out := splitPending(m.pending, recipient)
	m.pending = keep
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
		return recipient == RootPath
	}
	to = strings.TrimSuffix(to, "/")
	// Exact match.
	if to == recipient {
		return true
	}
	// Descendant match: nested agents' mail is visible to ancestor recipients.
	// e.g. mailTo="/root/a/b" matches recipient="/root".
	if recipient != "" && strings.HasPrefix(to, recipient+"/") {
		return true
	}
	return false
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
