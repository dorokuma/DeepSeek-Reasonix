package multiagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/tool"
)

type fakeRunner struct {
	fn func(ctx context.Context, path, message string) (string, error)
}

func (f fakeRunner) Run(ctx context.Context, path, message string) (string, error) {
	if f.fn != nil {
		return f.fn(ctx, path, message)
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
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		select {
		case <-hold:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	for _, name := range []string{"a", "b", "c"} {
		if _, _, err := c.Spawn(context.Background(), RootPath, name, "prompt-"+name+" "+strings.Repeat("x", 100)); err != nil {
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
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "done-answer", nil
	}})
	path, nick, err := c.Spawn(context.Background(), RootPath, "explore", "find X")
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
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
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
	if _, _, err := c.Spawn(context.Background(), RootPath, "fast", "f"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Spawn(context.Background(), RootPath, "slow", "s"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return c.activeUnderCount(RootPath) >= 2
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
	// Mail to a non-descendant path (/other, not under /root) should not be taken by root wait.
	c.mailbox.Enqueue(Mail{From: "/other", To: "/other", Message: "not for root"})
	res := c.WaitFor(context.Background(), RootPath)
	if res.Interrupted {
		t.Fatalf("unexpected interrupt: %+v", res)
	}
	if res.MailCount != 0 {
		t.Fatalf("must not take non-descendant mail: %+v", res)
	}
	if !c.Mailbox().HasPendingFor("/other") {
		t.Fatal("foreign mail should remain")
	}
	// Direct mail to root is collected.
	c.mailbox.Enqueue(Mail{From: "/root/child", To: RootPath, Message: "done"})
	res = c.WaitFor(context.Background(), RootPath)
	if res.MailCount != 1 || !strings.Contains(res.Results, "done") {
		t.Fatalf("want root mail collected, got %+v", res)
	}
}

func TestWaitReceivesDescendantMail(t *testing.T) {
	c := NewControl()
	// Mail to a descendant agent (/root/a/b) should be visible to root wait.
	c.mailbox.Enqueue(Mail{From: "/root/a/b", To: "/root/a/b", Message: "nested done"})
	res := c.WaitFor(context.Background(), RootPath)
	if res.Interrupted {
		t.Fatalf("unexpected interrupt: %+v", res)
	}
	if res.MailCount != 1 || !strings.Contains(res.Results, "nested done") {
		t.Fatalf("want descendant mail collected by root, got %+v", res)
	}
}

func TestWaitSteer(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "slow", "long"); err != nil {
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
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		select {
		case <-hold:
			return "x", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m")
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

func TestListKeepsCompletedUntilClose(t *testing.T) {
	// Codex: completed agents remain open until close_agent.
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "fast-ok", nil
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "fast", "f"); err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)
	list := c.List(RootPath, "")
	found := false
	for _, a := range list {
		if strings.HasSuffix(a.AgentName, "/fast") {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed agent must stay listed until close: %+v", list)
	}
	if _, _, err := c.CloseAgent("fast"); err != nil {
		t.Fatal(err)
	}
	list = c.List(RootPath, "")
	for _, a := range list {
		if strings.HasSuffix(a.AgentName, "/fast") {
			t.Fatalf("closed agent must leave list: %+v", list)
		}
	}
}

func TestListShowsInterrupted(t *testing.T) {
	c := NewControl()
	started := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "stopme", "m")
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
		st, _, _ := c.GetStatus(path)
		return st == StatusInterrupted
	})
	list := c.List(RootPath, "")
	found := false
	for _, a := range list {
		if a.AgentName == path {
			found = true
			if s, ok := a.AgentStatus.(string); ok && s != string(StatusInterrupted) {
				t.Fatalf("want interrupted status, got %v", a.AgentStatus)
			}
		}
	}
	if !found {
		t.Fatalf("interrupted agent should appear in list: %+v", list)
	}
	if _, err := c.ResolveTarget("stopme"); err != nil {
		t.Fatalf("interrupted agent should remain resolvable: %v", err)
	}
}

func TestMailboxDrainForRecipient(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "ans", nil
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "child", "do it"); err != nil {
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
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		select {
		case <-hold:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "m1"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return c.activeUnderCount(RootPath) >= 1 })
	_, _, err := c.Spawn(context.Background(), RootPath, "job", "m2")
	if err == nil || (!strings.Contains(err.Error(), "still running") && !strings.Contains(err.Error(), "still open")) {
		t.Fatalf("want refuse while live, got %v", err)
	}
	close(hold)
	_ = c.WaitFor(context.Background(), RootPath)
}

func TestSendInputAfterInterrupt(t *testing.T) {
	c := NewControl()
	started := make(chan struct{})
	var turns int32
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		n := atomic.AddInt32(&turns, 1)
		if n == 1 {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "after-redirect:" + message, nil
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "first")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	got, err := c.SendInput(path, "correct direction", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path %s", got)
	}
	res := c.WaitFor(context.Background(), RootPath)
	if res.MailCount < 1 || !strings.Contains(res.Results, "after-redirect") {
		t.Fatalf("wait after send_input: %+v", res)
	}
	// Spawn under same name while open should fail until close.
	_, _, err = c.Spawn(context.Background(), RootPath, "job", "again")
	if err == nil {
		t.Fatal("spawn same open path should fail; use send_input or close_agent")
	}
	if _, _, err := c.CloseAgent(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "fresh"); err != nil {
		t.Fatalf("spawn after close: %v", err)
	}
}

func TestSpawnAfterCloseReusesPath(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "done", nil
	}})
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "first"); err != nil {
		t.Fatal(err)
	}
	res := c.WaitFor(context.Background(), RootPath)
	if res.MailCount != 1 {
		t.Fatalf("first wait %+v", res)
	}
	// Completed still open — must close before re-spawn same name.
	_, _, err := c.Spawn(context.Background(), RootPath, "job", "second")
	if err == nil {
		t.Fatal("want refuse while completed agent still open")
	}
	if _, _, err := c.CloseAgent("job"); err != nil {
		t.Fatal(err)
	}
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "second")
	if err != nil {
		t.Fatalf("re-spawn after close: %v", err)
	}
	if path != "/root/job" {
		t.Fatalf("want /root/job, got %s", path)
	}
}

func TestNoDoubleStartTurn(t *testing.T) {
	c := NewControl()
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		select {
		case <-hold:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "first")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return c.activeUnderCount(RootPath) >= 1 })
	// Second start without interrupt while running must soft-queue or fail, not double-run.
	// Without a Steerer on fakeRunner, soft-queue fails — must not start a second turn.
	_, err = c.SendInput(path, "second", false)
	if err == nil {
		t.Fatal("expected error when soft-queue unavailable on fake runner")
	}
	if c.runningCount.Load() != 1 {
		t.Fatalf("runningCount=%d want 1", c.runningCount.Load())
	}
	close(hold)
	_ = c.WaitFor(context.Background(), RootPath)
}

func TestWaitBlocksOnInterruptedUntilClose(t *testing.T) {
	c := NewControl()
	started := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m")
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
	// Wait should not complete while interrupted (Codex is_final).
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	res := c.WaitFor(ctx, RootPath)
	if !res.Interrupted {
		t.Fatalf("wait should time out / cancel while agent interrupted, got %+v", res)
	}
	// Close frees wait.
	if _, _, err := c.CloseAgent(path); err != nil {
		t.Fatal(err)
	}
	res = c.WaitFor(context.Background(), RootPath)
	if res.Interrupted {
		t.Fatalf("after close wait should complete, got %+v", res)
	}
}

func TestSendInputCloseResume(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "ans:" + message, nil
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)
	reg := tool.NewRegistry()
	RegisterTools(reg)
	ctx := WithControl(context.Background(), c)
	sTool, ok := reg.Get("send_input")
	if !ok {
		t.Fatal("send_input not registered")
	}
	if _, err := sTool.Execute(ctx, json.RawMessage(`{"target":"job","message":"more"}`)); err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)
	cTool, ok := reg.Get("close_agent")
	if !ok {
		t.Fatal("close_agent not registered")
	}
	if _, err := cTool.Execute(ctx, json.RawMessage(`{"target":"job"}`)); err != nil {
		t.Fatal(err)
	}
	if c.OpenCount() != 0 {
		t.Fatalf("open count after close = %d", c.OpenCount())
	}
	rTool, ok := reg.Get("resume_agent")
	if !ok {
		t.Fatal("resume_agent not registered")
	}
	if _, err := rTool.Execute(ctx, json.RawMessage(`{"id":"job"}`)); err != nil {
		t.Fatal(err)
	}
	if c.OpenCount() != 1 {
		t.Fatalf("open count after resume = %d", c.OpenCount())
	}
	if _, err := c.SendInput(path, "after-resume", false); err != nil {
		t.Fatal(err)
	}
	res := c.WaitFor(context.Background(), RootPath)
	if !strings.Contains(res.Results, "after-resume") {
		t.Fatalf("wait after resume: %+v", res)
	}
}

// --- Direct unit tests for SendInput / CloseAgent / ResumeAgent / activeUnderCount ---

func TestSendInputToCompletedAgent(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "ans:" + message, nil
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "first")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath) // completes

	got, err := c.SendInput(path, "second-msg", false)
	if err != nil {
		t.Fatalf("SendInput on completed agent: %v", err)
	}
	if got != path {
		t.Fatalf("SendInput path = %q, want %q", got, path)
	}
	res := c.WaitFor(context.Background(), RootPath)
	if !strings.Contains(res.Results, "second-msg") {
		t.Fatalf("wait after SendInput: %+v", res)
	}
}

func TestSendInputErrors(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{})
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "x"); err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)

	// Empty message
	if _, err := c.SendInput("job", "", false); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("want message-required error, got %v", err)
	}
	// Root target resolves to /root which is not a spawned agent
	if _, err := c.SendInput(RootPath, "hi", false); err == nil {
		t.Fatal("expected error for root target")
	}
	// Non-existent target
	if _, err := c.SendInput("nonexistent", "hi", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestCloseAgentReopensWithSpawn(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "done", nil
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)

	prev, closePath, err := c.CloseAgent("job")
	if err != nil {
		t.Fatalf("CloseAgent: %v", err)
	}
	if closePath != path {
		t.Fatalf("CloseAgent path = %q, want %q", closePath, path)
	}
	if prev == nil {
		t.Fatal("CloseAgent returned nil previous status")
	}
	if c.OpenCount() != 0 {
		t.Fatalf("open count after close = %d", c.OpenCount())
	}
	// Must be able to spawn again under same name.
	if _, _, err := c.Spawn(context.Background(), RootPath, "job", "fresh"); err != nil {
		t.Fatalf("re-spawn after close: %v", err)
	}
}

func TestCloseAgentOnUnknownReturnsNotFound(t *testing.T) {
	c := NewControl()
	// CloseAgent resolves via ResolveTarget first; unknown name returns error.
	_, _, err := c.CloseAgent("never-existed")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestCloseAgentOnRootFails(t *testing.T) {
	c := NewControl()
	// RootPath resolves but is not a spawned agent — error may come from
	// ResolveTarget (agent not found) or dedicated root check.
	_, _, err := c.CloseAgent(RootPath)
	if err == nil {
		t.Fatal("expected error for root target")
	}
}

func TestResumeAgent(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "done", nil
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)
	if _, _, err := c.CloseAgent("job"); err != nil {
		t.Fatal(err)
	}

	// Resume
	status, resPath, err := c.ResumeAgent("job")
	if err != nil {
		t.Fatalf("ResumeAgent: %v", err)
	}
	if resPath != path {
		t.Fatalf("path = %q, want %q", resPath, path)
	}
	if status == nil {
		t.Fatal("status is nil")
	}
	if c.OpenCount() != 1 {
		t.Fatalf("open count after resume = %d", c.OpenCount())
	}
	// SendInput must work on resumed agent.
	if _, err := c.SendInput(path, "after-resume", false); err != nil {
		t.Fatalf("SendInput after resume: %v", err)
	}
	_ = c.WaitFor(context.Background(), RootPath)
}

func TestResumeAgentErrors(t *testing.T) {
	c := NewControl()
	// Empty id
	if _, _, err := c.ResumeAgent(""); err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("want id-required error, got %v", err)
	}
	// Non-existent agent
	if _, _, err := c.ResumeAgent("ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestResumeAgentAlreadyOpen(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		return "ok", nil
	}})
	_, _, err := c.Spawn(context.Background(), RootPath, "job", "m")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WaitFor(context.Background(), RootPath)
	// Agent is still open (completed, not closed). Resume should report current status.
	status, path, err := c.ResumeAgent("job")
	if err != nil {
		t.Fatalf("ResumeAgent on open agent: %v", err)
	}
	if status == nil {
		t.Fatal("status is nil")
	}
	if path != "/root/job" {
		t.Fatalf("path = %q", path)
	}
}

func TestActiveUnderCount(t *testing.T) {
	c := NewControl()
	hold := make(chan struct{})
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string) (string, error) {
		select {
		case <-hold:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	if n := c.activeUnderCount(RootPath); n != 0 {
		t.Fatalf("expected 0 active before spawn, got %d", n)
	}
	if _, _, err := c.Spawn(context.Background(), RootPath, "a1", "m1"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return c.activeUnderCount(RootPath) >= 1
	})
	if _, _, err := c.Spawn(context.Background(), RootPath, "a2", "m2"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return c.activeUnderCount(RootPath) >= 2
	})
	close(hold)
	_ = c.WaitFor(context.Background(), RootPath)
	// After completion, no longer active.
	if n := c.activeUnderCount(RootPath); n != 0 {
		t.Fatalf("expected 0 active after both complete, got %d", n)
	}
}


