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
		select {
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestListShowsRunningAgents(t *testing.T) {
	c := NewControl()
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
	list := c.List(RootPath, "")
	if len(list) < 4 {
		t.Fatalf("want root+3 agents, got %d: %+v", len(list), list)
	}
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
	for _, a := range list {
		if a.AgentName == RootPath {
			continue
		}
		if msg, ok := a.LastTaskMessage.(string); ok && len([]rune(msg)) > lastTaskListCap+5 {
			t.Fatalf("last_task_message not capped: %d", len([]rune(msg)))
		}
	}
	if c.Mailbox().HasPending() {
		t.Fatal("mailbox should be empty while agents still running")
	}
	close(hold)
}

func TestSpawnWaitMailbox(t *testing.T) {
	c := NewControl()
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
	res := c.WaitFor(context.Background(), RootPath)
	if res.Interrupted || res.Message != "Wait completed." {
		t.Fatalf("wait got %+v", res)
	}
	if res.MailCount != 1 || !strings.Contains(res.Results, "done-answer") {
		t.Fatalf("want results in wait payload, got %+v", res)
	}
	if c.Mailbox().HasPendingFor(RootPath) {
		t.Fatal("mail should already be taken by wait")
	}
}

func TestWaitCollectsParallelBatch(t *testing.T) {
	c := NewControl()
	fastHold := make(chan struct{})
	slowHold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		switch {
		case strings.HasSuffix(path, "/fast"):
			<-fastHold
			return "fast-ans", nil
		case strings.HasSuffix(path, "/slow"):
			<-slowHold
			return "slow-ans", nil
		default:
			return "x", nil
		}
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "fast", "f", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Spawn(context.Background(), RootPath, "slow", "s", 0); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return c.liveUnderCount(RootPath) >= 2
	})

	done := make(chan WaitResult, 1)
	go func() {
		done <- c.WaitFor(context.Background(), RootPath)
	}()

	// Release fast first; wait must stay blocked until slow also finishes.
	close(fastHold)
	select {
	case res := <-done:
		t.Fatalf("wait returned before slow finished: %+v", res)
	case <-time.After(80 * time.Millisecond):
	}
	close(slowHold)

	select {
	case res := <-done:
		if res.Interrupted || res.MailCount != 2 {
			t.Fatalf("want 2 mails one wait, got %+v", res)
		}
		if !strings.Contains(res.Results, "fast-ans") || !strings.Contains(res.Results, "slow-ans") {
			t.Fatalf("missing answers: %s", res.Results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not finish after both agents")
	}
}

func TestWaitIgnoresMailForOtherRecipient(t *testing.T) {
	c := NewControl()
	c.mailbox.Enqueue(Mail{From: "/root/other", To: "/root/other", Message: "not for root"})
	// No live agents under root → wait returns immediately; foreign mail stays.
	res := c.WaitFor(context.Background(), RootPath)
	if res.Interrupted {
		t.Fatalf("unexpected interrupt: %+v", res)
	}
	if res.MailCount != 0 {
		t.Fatalf("must not take other-recipient mail: %+v", res)
	}
	if !c.Mailbox().HasPendingFor("/root/other") {
		t.Fatal("foreign mail should remain")
	}
	c.mailbox.Enqueue(Mail{From: "/root/child", To: RootPath, Message: "done"})
	res = c.WaitFor(context.Background(), RootPath)
	if res.MailCount != 1 || !strings.Contains(res.Results, "done") {
		t.Fatalf("want root mail collected, got %+v", res)
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-time.After(1 * time.Millisecond):
		}
		c.NotifySteer()
	}()
	res := c.WaitFor(context.Background(), RootPath)
	<-done
	if !res.Interrupted || !strings.Contains(res.Message, "interrupted") {
		t.Fatalf("want steered, got %+v", res)
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
	res := c.WaitFor(context.Background(), RootPath)
	if res.Interrupted || res.MailCount != 1 || !strings.Contains(res.Results, "ans") {
		t.Fatalf("wait %+v", res)
	}
	if c.Mailbox().HasPendingFor(RootPath) {
		t.Fatal("wait should have taken root mail")
	}
	if c.Mailbox().HasPending() {
		t.Fatal("mailbox should be empty")
	}
}

func TestNestedSpawnPath(t *testing.T) {
	c := NewControl()
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
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

func TestWaitAgentSchemaHasNoTimeout(t *testing.T) {
	s := string(waitAgent{}.Schema())
	if strings.Contains(s, "timeout") {
		t.Fatalf("timeout must not appear in wait_agent schema: %s", s)
	}
	d := waitAgent{}.Description()
	if strings.Contains(strings.ToLower(d), "timeout_ms") {
		t.Fatalf("timeout_ms must not appear in description: %s", d)
	}
}

func TestSpawnRefusesWhileLive(t *testing.T) {
	c := NewControl()
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		select {
		case <-hold:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "m1", 0); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return c.liveUnderCount(RootPath) >= 1 })
	_, _, err := c.Spawn(context.Background(), RootPath, "job", "m2", 0)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("want refuse while live, got %v", err)
	}
	close(hold)
	_ = c.WaitFor(context.Background(), RootPath)
}

func TestSpawnRefusesAfterRecentInterrupt(t *testing.T) {
	c := NewControl()
	started := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m", 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	if _, err := c.Interrupt(path); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		st, _, _ := c.GetStatus(path)
		return st == StatusInterrupted
	})
	_, _, err = c.Spawn(context.Background(), RootPath, "job", "again", 0)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("want refuse after interrupt, got %v", err)
	}
}

func TestSpawnAllowsAfterCompleted(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		return "done", nil
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "first", 0); err != nil {
		t.Fatal(err)
	}
	res := c.WaitFor(context.Background(), RootPath)
	if res.MailCount != 1 {
		t.Fatalf("first wait %+v", res)
	}
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "second", 0)
	if err != nil {
		t.Fatalf("re-spawn after complete should work: %v", err)
	}
	if path != "/root/job" {
		t.Fatalf("want reused path /root/job, got %s", path)
	}
	res = c.WaitFor(context.Background(), RootPath)
	if res.MailCount != 1 {
		t.Fatalf("second wait %+v", res)
	}
}
