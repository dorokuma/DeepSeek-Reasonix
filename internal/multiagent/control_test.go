package multiagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	fn func(ctx context.Context, path, message string, depth int) (string, error)
}

func (f fakeRunner) Run(ctx context.Context, path, message string, depth int) (string, error) {
	if f.fn != nil {
		return f.fn(ctx, path, message, depth)
	}
	return "ok", nil
}

// waitUntil polls cond until true or deadline; fails the test on timeout.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		// Short yield so we do not busy-spin; not a fixed test timing assumption.
		select {
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestListShowsRunningAgents(t *testing.T) {
	c := NewControl()
	// Hold all workers until the test releases them so list can observe "running".
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		select {
		case <-hold:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	for _, name := range []string{"a", "b", "c"} {
		if _, _, err := c.Spawn(context.Background(), RootPath, name, "prompt-"+name+" "+strings.Repeat("x", 100), 0); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 2*time.Second, func() bool {
		return int(c.runningCount.Load()) >= 3
	})
	// While running, list must include root + 3 children with running status.
	list := c.List(RootPath, "")
	if len(list) < 4 {
		t.Fatalf("want root+3 agents, got %d: %+v", len(list), list)
	}
	// Encode like the tool does; ensure status fields present and not wiped by long messages.
	b, err := json.Marshal(map[string]any{"agents": list})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"agent_status":"running"`) {
		t.Fatalf("expected running status in list JSON:\n%s", s)
	}
	if !strings.Contains(s, `"/root/a"`) && !strings.Contains(s, `/root/a`) {
		t.Fatalf("expected canonical path agent_name:\n%s", s)
	}
	// last_task_message capped
	for _, a := range list {
		if a.AgentName == RootPath {
			continue
		}
		if msg, ok := a.LastTaskMessage.(string); ok && len([]rune(msg)) > lastTaskListCap+5 {
			t.Fatalf("last_task_message not capped: %d", len([]rune(msg)))
		}
	}
	// Mailbox empty while running
	if c.Mailbox().HasPending() {
		t.Fatal("mailbox should be empty while agents still running")
	}
	close(hold)
}

func TestSpawnWaitMailbox(t *testing.T) {
	c := NewControl()
	// Instant completion — Wait should observe mailbox without fixed sleep.
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		return "done-answer", nil
	}})
	path, nick, err := c.Spawn(context.Background(), RootPath, "explore", "find X", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "explore") || nick == "" {
		t.Fatalf("path/nick %q %q", path, nick)
	}
	msg, timedOut := c.Wait(context.Background(), 5000)
	if timedOut || msg != "Wait completed." {
		t.Fatalf("wait got %q timedOut=%v", msg, timedOut)
	}
	mails := c.Mailbox().Drain()
	if len(mails) != 1 || !strings.Contains(mails[0].Message, "done-answer") {
		t.Fatalf("mails %#v", mails)
	}
}

func TestWaitSteer(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "slow", "long", 0); err != nil {
		t.Fatal(err)
	}
	// Steer after spawn is registered (no sleep race: NotifySteer is concurrent-safe).
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Yield once so Wait has a chance to arm; still event-driven via mailbox.
		select {
		case <-time.After(1 * time.Millisecond):
		}
		c.NotifySteer()
	}()
	msg, timedOut := c.Wait(context.Background(), 5000)
	<-done
	if timedOut || !strings.Contains(msg, "interrupted") {
		t.Fatalf("want steered, got %q timed=%v", msg, timedOut)
	}
}

func TestListAndInterrupt(t *testing.T) {
	c := NewControl()
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		select {
		case <-hold:
			return "x", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m", 0)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return int(c.runningCount.Load()) >= 1
	})
	list := c.List(RootPath, "")
	if len(list) < 2 {
		t.Fatalf("list %d", len(list))
	}
	if _, err := c.Interrupt(path); err != nil {
		t.Fatal(err)
	}
	close(hold)
}

func TestListOmitsTerminalAgents(t *testing.T) {
	c := NewControl()
	block := make(chan struct{})
	fastDone := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		if strings.HasSuffix(path, "/slow") {
			<-block
			return "slow-ok", nil
		}
		close(fastDone)
		return "fast-ok", nil
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "fast", "f", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Spawn(context.Background(), RootPath, "slow", "s", 0); err != nil {
		t.Fatal(err)
	}
	// Wait for fast to finish (event), not a fixed sleep.
	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fast agent did not finish")
	}
	waitUntil(t, 2*time.Second, func() bool {
		list := c.List(RootPath, "")
		for _, a := range list {
			if a.AgentName == "/root/fast" || strings.HasSuffix(a.AgentName, "/fast") {
				return false
			}
		}
		// Ensure slow still listed as running.
		for _, a := range list {
			if strings.HasSuffix(a.AgentName, "/slow") {
				return true
			}
		}
		return false
	})
	list := c.List(RootPath, "")
	var names []string
	for _, a := range list {
		names = append(names, a.AgentName)
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, "/root/fast") {
		t.Fatalf("completed agent must not appear in list: %v", names)
	}
	if !strings.Contains(joined, "/root/slow") {
		t.Fatalf("running agent missing: %v", names)
	}
	close(block)
	// Registry still resolves completed for followup.
	if _, err := c.ResolveTarget("fast"); err != nil {
		t.Fatalf("completed agent should remain resolvable: %v", err)
	}
}

func TestListOmitsInterrupted(t *testing.T) {
	c := NewControl()
	started := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "stopme", "m", 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start")
	}
	if _, err := c.Interrupt(path); err != nil {
		t.Fatal(err)
	}
	// Wait until runAgent has observed cancel and set terminal status.
	waitUntil(t, 2*time.Second, func() bool {
		list := c.List(RootPath, "")
		for _, a := range list {
			if a.AgentName == path {
				return false
			}
		}
		return true
	})
	list := c.List(RootPath, "")
	for _, a := range list {
		if a.AgentName == path {
			t.Fatalf("interrupted agent must not appear in list: %+v", list)
		}
	}
	if _, err := c.ResolveTarget("stopme"); err != nil {
		t.Fatalf("interrupted agent should remain resolvable: %v", err)
	}
}

func TestMailboxDrainForRecipient(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		return "ans", nil
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "child", "do it", 0); err != nil {
		t.Fatal(err)
	}
	// Parent wait until completion mail lands.
	if msg, timed := c.Wait(context.Background(), 3000); timed || msg != "Wait completed." {
		t.Fatalf("wait %q timed=%v", msg, timed)
	}
	// Mail for /root only — DrainFor root gets it; other path empty.
	if !c.Mailbox().HasPendingFor(RootPath) {
		t.Fatal("expected pending for root")
	}
	if c.Mailbox().HasPendingFor("/root/other") {
		t.Fatal("other path should not see root mail")
	}
	mails := c.Mailbox().DrainFor(RootPath)
	if len(mails) != 1 {
		t.Fatalf("want 1 mail, got %#v", mails)
	}
	if c.Mailbox().HasPending() {
		t.Fatal("mailbox should be empty after DrainFor")
	}
}

func TestNestedSpawnPath(t *testing.T) {
	c := NewControl()
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		// Nested spawn from child path, then stay running so list can see both.
		if depth == 1 {
			if _, _, err := c.Spawn(ctx, path, "nested", "inner", depth); err != nil {
				return "", err
			}
		}
		select {
		case <-hold:
			return "ok-" + path, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "outer", "work", 0); err != nil {
		t.Fatal(err)
	}
	// Poll until nested path registers (eventual), not fixed sleep.
	var joined string
	waitUntil(t, 2*time.Second, func() bool {
		list := c.List(RootPath, "")
		var names []string
		for _, a := range list {
			names = append(names, a.AgentName)
		}
		joined = strings.Join(names, ",")
		return strings.Contains(joined, "/root/outer") && strings.Contains(joined, "/root/outer/nested")
	})
	close(hold)
	if !strings.Contains(joined, "/root/outer") {
		t.Fatalf("missing outer: %s", joined)
	}
	if !strings.Contains(joined, "/root/outer/nested") {
		t.Fatalf("missing nested path: %s", joined)
	}
}
