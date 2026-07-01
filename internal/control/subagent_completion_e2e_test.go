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
	}, ctrl.MakeOnComplete())
	if err != nil {
		t.Fatal(err)
	}
	ctrl.RegisterJobMeta(job.ID, "tool-call-1")
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
