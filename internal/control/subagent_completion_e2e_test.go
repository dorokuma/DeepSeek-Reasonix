package control

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
)

type recordingRunner struct {
	mu     sync.Mutex
	inputs []string
}

func (r *recordingRunner) Run(_ context.Context, input string) error {
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()
	return nil
}
func (r *recordingRunner) Steer(_ string) {}

func waitJobDone(t *testing.T, jm *jobs.Manager, id string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		st, err := jm.Peek(id)
		if err == jobs.ErrJobNotFound {
			return // result flushed and job removed from manager
		}
		if err == nil && st.Status != "running" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("job %s did not finish", id)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSubagentCompletionNoticeAndAutoReentry verifies the headless feedback
// chain that mirrors the TUI crash path: job finish Notice + pendingToolResult
// + autoReenter scheduling — without racing channel close.
func TestSubagentCompletionNoticeAndAutoReentry(t *testing.T) {
	evCh := make(chan event.Event, 64)
	sink := event.Sync(event.FuncSink(func(e event.Event) {
		select {
		case evCh <- e:
		default:
		}
	}))

	jm := jobs.NewManager(sink)
	defer jm.Close()

	runner := &recordingRunner{}
	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)

	ctrl := New(Options{
		Runner:     runner,
		Executor:   ag,
		Sink:       sink,
		SessionDir: t.TempDir(),
		Label:      "test",
		Jobs:       jm,
	})
	ag.SetControllerBridge(ctrl)

	job, err := jm.Start(context.Background(), "task", "e2e", func(ctx context.Context, _ io.Writer) (string, error) {
		jobs.PostMessage(ctx, "mid-flight report")
		time.Sleep(2 * time.Millisecond)
		return "sub-result", nil
	}, ctrl.MakeOnComplete(), func(id string) { ctrl.RegisterJobMeta(id, "tool-call-1") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	deadline := time.After(3 * time.Second)
	var gotStart, gotFinish bool
	for !(gotStart && gotFinish) {
		select {
		case e := <-evCh:
			if e.Kind != event.Notice {
				continue
			}
			t.Logf("notice: %s", e.Text)
			if strings.Contains(e.Text, "started") {
				gotStart = true
			}
			if strings.Contains(e.Text, "finished") {
				gotFinish = true
			}
		case <-deadline:
			t.Fatalf("notices: start=%v finish=%v", gotStart, gotFinish)
		}
	}

	if !ctrl.PendingToolResultCAS(true, false) {
		t.Fatal("pendingToolResult not set after job completion")
	}

	// autoReenter should have been invoked via SetOnCompletion; drain runner input.
	time.Sleep(50 * time.Millisecond)
	runner.mu.Lock()
	n := len(runner.inputs)
	runner.mu.Unlock()
	if n == 0 {
		t.Fatal("autoReenter did not schedule a turn via Send")
	}
}

// TestSubagentCompletionChainNoPanic exercises the full auto-reentry chain:
// job completion -> pendingToolResult -> Agent.Run drains job result without panic.
// Regression for nil-pointer panic in getSchemasForContext when tools is unset.
func TestSubagentCompletionChainNoPanic(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)
	runner := &recordingRunner{}

	ctrl := New(Options{
		Runner:     runner,
		Executor:   ag,
		Sink:       sink,
		SessionDir: t.TempDir(),
		Label:      "test",
		Jobs:       jm,
	})
	ag.SetControllerBridge(ctrl)

	job, err := jm.Start(context.Background(), "task", "e2e", func(ctx context.Context, _ io.Writer) (string, error) {
		jobs.PostMessage(ctx, "mid-flight report")
		time.Sleep(2 * time.Millisecond) // yield so RegisterJobMeta wins instant-completion race
		return "sub-result", nil
	}, nil, func(id string) { ctrl.RegisterJobMeta(id, "tool-call-1") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	ctrl.pendingToolResult.Store(true)

	// SetOnCompletion already wired autoReenter; wait for background Send turn.
	time.Sleep(100 * time.Millisecond)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during auto-reentry: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ag.Run(ctx, ""); err == nil {
		t.Fatal("expected Run to fail with nil provider, got nil error")
	}
}

// TestAutoReenterDefaultsRunnerNoPanic verifies SetOnCompletion autoReenter does not
// panic when Options omitted Runner but provided Executor (regression for nil runner).
func TestAutoReenterDefaultsRunnerNoPanic(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	ag := agent.New(nil, nil, agent.NewSession("test"), agent.Options{Jobs: jm}, sink)
	ctrl := New(Options{Executor: ag, Sink: sink, SessionDir: t.TempDir(), Label: "test", Jobs: jm})
	ag.SetControllerBridge(ctrl)

	_, err := jm.Start(context.Background(), "task", "runner-default", func(ctx context.Context, _ io.Writer) (string, error) {
		return "ok", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	time.Sleep(150 * time.Millisecond)
}

// TestAutoReenterWithoutPerJobOnComplete verifies SetOnCompletion alone activates
// the main agent when per-job onComplete was not wired (regression for silent no-op).
func TestAutoReenterWithoutPerJobOnComplete(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	runner := &recordingRunner{}
	ag := agent.New(nil, nil, agent.NewSession("test"), agent.Options{Jobs: jm}, sink)
	ctrl := New(Options{Runner: runner, Executor: ag, Sink: sink, SessionDir: t.TempDir(), Label: "test", Jobs: jm})
	ag.SetControllerBridge(ctrl)

	job, err := jm.Start(context.Background(), "task", "no-callback", func(ctx context.Context, _ io.Writer) (string, error) {
		return "done", nil
	}, nil, func(id string) { ctrl.RegisterJobMeta(id, "tool-call-1") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	time.Sleep(100 * time.Millisecond)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.inputs) == 0 {
		t.Fatal("SetOnCompletion did not schedule auto-reentry without per-job onComplete")
	}
}

// TestAutoReentryDrainsToolResultIntoSession verifies the auto-reentry turn folds the
// completed sub-agent output into the session as a tool message.
func TestAutoReentryDrainsToolResultIntoSession(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)
	// Controller without Jobs: no SetOnCompletion auto-reenter, so we can test drain in isolation.
	ctrl := New(Options{Executor: ag, Sink: sink, SessionDir: t.TempDir(), Label: "test"})
	ag.SetControllerBridge(ctrl)

	job, err := jm.Start(context.Background(), "task", "drain", func(ctx context.Context, _ io.Writer) (string, error) {
		return "sub-result", nil
	}, nil, func(id string) { ctrl.RegisterJobMeta(id, "tool-call-1") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	ctrl.pendingToolResult.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ag.Run(ctx, ""); err == nil {
		t.Fatal("expected nil-provider Run to error")
	}

	var toolMsgs int
	for _, m := range sess.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == "tool-call-1" && m.Content == "sub-result" {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("expected 1 tool result message, got %d; messages=%d", toolMsgs, len(sess.Messages))
	}
}

// phasedBlockingRunner blocks the first Run call until ReleaseFirst is invoked,
// then delegates to the wrapped runner so auto-reentry exercises the real agent path.
type phasedBlockingRunner struct {
	inner        agent.Runner
	mu           sync.Mutex
	inputs       []string
	firstStarted chan struct{}
	firstRelease chan struct{}
	firstOnce    sync.Once
}

func newPhasedBlockingRunner(inner agent.Runner) *phasedBlockingRunner {
	return &phasedBlockingRunner{
		inner:        inner,
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
}

func (r *phasedBlockingRunner) Run(ctx context.Context, input string) error {
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	phase := len(r.inputs) - 1
	r.mu.Unlock()

	if phase == 0 {
		r.firstOnce.Do(func() { close(r.firstStarted) })
		select {
		case <-r.firstRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.Run(ctx, input)
}

func (r *phasedBlockingRunner) Steer(input string) {
	if r.inner != nil {
		r.inner.Steer(input)
	}
}

func (r *phasedBlockingRunner) Inputs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.inputs))
	copy(out, r.inputs)
	return out
}

// TestAutoReenterDeferredWhileMainTurnBusy verifies that when a background job
// completes while the main turn is still running, auto-reentry is queued via
// pendingReentry and only fires Send("") after the busy turn finishes.
func TestAutoReenterDeferredWhileMainTurnBusy(t *testing.T) {
	evCh := make(chan event.Event, 64)
	sink := event.Sync(event.FuncSink(func(e event.Event) {
		select {
		case evCh <- e:
		default:
		}
	}))

	jm := jobs.NewManager(sink)
	defer jm.Close()

	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)
	runner := newPhasedBlockingRunner(ag)

	ctrl := New(Options{
		Runner:     runner,
		Executor:   ag,
		Sink:       sink,
		SessionDir: t.TempDir(),
		Label:      "test",
		Jobs:       jm,
	})
	ag.SetControllerBridge(ctrl)

	ctrl.Send("main-turn")
	select {
	case <-runner.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("main turn did not start")
	}

	job, err := jm.Start(context.Background(), "task", "deferred", func(ctx context.Context, _ io.Writer) (string, error) {
		return "sub-result", nil
	}, ctrl.MakeOnComplete(), func(id string) { ctrl.RegisterJobMeta(id, "tool-call-deferred") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	ctrl.mu.Lock()
	queueLen := len(ctrl.pendingReentryQueue)
	gotRunning := ctrl.running
	ctrl.mu.Unlock()
	if queueLen == 0 {
		t.Fatal("pendingReentryQueue should have deferred auto-reentry while main turn is busy")
	}
	if !gotRunning {
		t.Fatal("main turn should still be running when job completes")
	}
	if !ctrl.pendingToolResult.Load() {
		t.Fatal("pendingToolResult should be set after job completion")
	}

	close(runner.firstRelease)

	turnDoneCount := 0
	deadline := time.After(5 * time.Second)
	for turnDoneCount < 2 {
		select {
		case e := <-evCh:
			if e.Kind == event.TurnDone {
				turnDoneCount++
			}
		case <-deadline:
			t.Fatalf("timed out waiting for turns; turnDoneCount=%d inputs=%v", turnDoneCount, runner.Inputs())
		}
	}

	inputs := runner.Inputs()
	if len(inputs) < 2 {
		t.Fatalf("expected at least 2 runner inputs (main + auto-reentry), got %v", inputs)
	}
	if inputs[0] != "main-turn" {
		t.Fatalf("first turn input = %q, want main-turn", inputs[0])
	}
	if inputs[1] != "" {
		t.Fatalf("deferred auto-reentry should Send(\"\"), got %q", inputs[1])
	}

	var toolMsgs int
	for _, m := range sess.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == "tool-call-deferred" && m.Content == "sub-result" {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("auto-reentry turn should drain tool result into session, got %d tool messages; messages=%d", toolMsgs, len(sess.Messages))
	}
}

// TestAutoReentryPatchesStartedToolPlaceholder verifies async completion updates
// the in-flight "Started task …" tool row instead of appending an orphan tool message.
func TestAutoReentryPatchesStartedToolPlaceholder(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)
	ctrl := New(Options{Executor: ag, Sink: sink, SessionDir: t.TempDir(), Label: "test"})
	ag.SetControllerBridge(ctrl)

	sess.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "tool-call-1", Name: "task"}}})
	sess.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "tool-call-1", Name: "task", Content: "Started task task-1 (explore)"})

	job, err := jm.Start(context.Background(), "task", "explore", func(_ context.Context, _ io.Writer) (string, error) {
		return "explore-result", nil
	}, nil, func(id string) { ctrl.RegisterJobMeta(id, "tool-call-1") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	ctrl.pendingToolResult.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ag.Run(ctx, "")

	var toolRows int
	var lastContent string
	for _, m := range sess.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == "tool-call-1" {
			toolRows++
			lastContent = m.Content
		}
	}
	if toolRows != 1 {
		t.Fatalf("want one tool row (started replaced by result), got %d", toolRows)
	}
	if lastContent != "explore-result" {
		t.Fatalf("last tool content = %q, want explore-result", lastContent)
	}
}

// TestInstantCompletionDrainsToolResult verifies RegisterJobMeta runs before the
// job goroutine starts, so a zero-delay sub-agent still lands its result in session.
func TestInstantCompletionDrainsToolResult(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)
	// No Jobs on ctrl: avoid SetOnCompletion auto-reenter racing waitJobDone/RemoveJob.
	ctrl := New(Options{Executor: ag, Sink: sink, SessionDir: t.TempDir(), Label: "test"})
	ag.SetControllerBridge(ctrl)

	job, err := jm.Start(context.Background(), "task", "instant", func(_ context.Context, _ io.Writer) (string, error) {
		return "instant-result", nil
	}, nil, func(id string) { ctrl.RegisterJobMeta(id, "tool-call-instant") })
	if err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, jm, job.ID)

	ctrl.pendingToolResult.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ag.Run(ctx, ""); err == nil {
		t.Fatal("expected nil-provider Run to error")
	}

	var toolMsgs int
	for _, m := range sess.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == "tool-call-instant" && m.Content == "instant-result" {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("expected 1 instant tool result, got %d; messages=%d", toolMsgs, len(sess.Messages))
	}
}
