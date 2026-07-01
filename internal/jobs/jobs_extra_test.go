package jobs

import (
	"context"
	"io"
	"testing"
	"time"

	"reasonix/internal/event"
)

type typedNilJobSink struct{}

func (*typedNilJobSink) Emit(event.Event) {}

func TestNewManagerTreatsTypedNilSinkAsDiscard(t *testing.T) {
	var sink *typedNilJobSink
	m := NewManager(sink)
	defer m.Close()

	j, _ := m.Start(context.Background(), "bash", "typed nil sink", func(context.Context, io.Writer) (string, error) {
		return "done", nil
	}, nil)
	<-j.done
	_, st, ok := m.Output(j.ID)
	if !ok || st != Done {
		t.Fatalf("job status = %s, want Done", st)
	}
}

// --- Output with unknown id ---

func TestOutputUnknownID(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	_, _, ok := m.Output("nonexistent-id")
	if ok {
		t.Error("Output for unknown id should return ok=false")
	}
}

// --- Kill with unknown id ---

func TestKillUnknownID(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	if m.Kill("nonexistent-id") {
		t.Error("Kill for unknown id should return false")
	}
}

// --- startedText ---

func TestStartedTextWithLabel(t *testing.T) {
	got := startedText("bash", "bash-1", "my-label")
	if got != "background bash started: bash-1 (my-label)" {
		t.Errorf("startedText = %q", got)
	}
}

func TestStartedTextWithoutLabel(t *testing.T) {
	got := startedText("task", "task-1", "")
	if got != "background task started: task-1" {
		t.Errorf("startedText = %q", got)
	}
}

// --- Close is idempotent ---

func TestCloseIdempotent(t *testing.T) {
	m := NewManager(event.Discard)
	_, _ = m.Start(context.Background(), "bash", "", func(_ context.Context, _ io.Writer) (string, error) {
		return "", nil
	}, nil)
	m.Close()
	m.Close() // should not panic
}

// --- Running with no jobs ---

func TestRunningEmpty(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()
	if r := m.Running(); len(r) != 0 {
		t.Errorf("Running() = %d, want 0", len(r))
	}
}

// --- Job with error sets Failed ---

func TestJobFailed(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, _ := m.Start(context.Background(), "bash", "", func(_ context.Context, _ io.Writer) (string, error) {
		return "", io.ErrUnexpectedEOF
	}, nil)
	<-j.done
	_, st, ok := m.Output(j.ID)
	if !ok || st != Failed {
		t.Fatalf("want Failed, got %s", st)
	}
}

// --- Job with result and no error sets Done ---

func TestJobWithResult(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, _ := m.Start(context.Background(), "task", "", func(_ context.Context, _ io.Writer) (string, error) {
		return "final answer", nil
	}, nil)
	<-j.done
	txt, st, ok := m.Output(j.ID)
	if !ok || st != Done {
		t.Fatalf("want Done, got status=%s ok=%v", st, ok)
	}
	if txt != "final answer" {
		t.Errorf("output = %q, want \"final answer\"", txt)
	}
}

// --- Context injection ---

func TestWithManagerFromContext(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	ctx := WithManager(context.Background(), m)
	got, ok := FromContext(ctx)
	if !ok || got != m {
		t.Error("FromContext should return the manager")
	}
}

func TestFromContextEmpty(t *testing.T) {
	_, ok := FromContext(context.Background())
	if ok {
		t.Error("plain context should return ok=false")
	}
}

// --- Status constants ---

func TestStatusConstants(t *testing.T) {
	if Running != "running" {
		t.Errorf("Running = %q, want running", Running)
	}
	if Done != "done" {
		t.Errorf("Done = %q, want done", Done)
	}
	if Failed != "failed" {
		t.Errorf("Failed = %q, want failed", Failed)
	}
	if Killed != "killed" {
		t.Errorf("Killed = %q, want killed", Killed)
	}
}

// --- Peek and ActiveJobs ---

func TestPeekActiveJobs(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	release := make(chan struct{})
	j, _ := m.Start(context.Background(), "task", "peek-test", func(ctx context.Context, _ io.Writer) (string, error) {
		<-release
		return "ok", nil
	}, nil)

	// Peek while running
	js, err := m.Peek(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if js.Status != string(Running) {
		t.Errorf("Peek status = %q, want running", js.Status)
	}

	// ActiveJobs includes this job
	ids := m.ActiveJobs()
	found := false
	for _, id := range ids {
		if id == j.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ActiveJobs missing job %q", j.ID)
	}

	close(release)
	<-j.done
}

// --- NotifyChannels ---

func TestNotifyChannels(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, _ := m.Start(context.Background(), "task", "notify", func(_ context.Context, _ io.Writer) (string, error) {
		return "result", nil
	}, nil)
	<-j.done

	ch := m.NotifyChannels(j.ID)
	if ch == nil {
		t.Fatal("NotifyChannels returned nil for existing job")
	}
	if ch.Ack == nil || ch.Result == nil || ch.Progress == nil {
		t.Error("one or more channels are nil")
	}
}

func TestNotifyChannelsUnknownID(t *testing.T) {
	m := NewManager(event.Discard)
	if ch := m.NotifyChannels("no-such-job"); ch != nil {
		t.Error("NotifyChannels for unknown id should return nil")
	}
}

// --- Steer ---

func TestSteer(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	release := make(chan struct{})
	gotSteer := make(chan string, 1)
	j, _ := m.Start(context.Background(), "task", "steer-test", func(ctx context.Context, _ io.Writer) (string, error) {
		// Retrieve job from context to avoid capturing outer 'j' (data race).
		job := ctx.Value(jobKey{}).(*Job)
		select {
		case msg := <-job.steerCh:
			gotSteer <- msg
		case <-release:
		}
		return "ok", nil
	}, nil)

	if err := m.Steer(j.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-gotSteer:
		if msg != "hello" {
			t.Errorf("steer message = %q, want hello", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for steer message")
	}
	close(release)
	<-j.done
}

func TestSteerUnknownID(t *testing.T) {
	m := NewManager(event.Discard)
	if err := m.Steer("no-such-job", "msg"); err != ErrJobNotFound {
		t.Errorf("Steer unknown id: want ErrJobNotFound, got %v", err)
	}
}

// --- RemoveJob ---

func TestRemoveJob(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, _ := m.Start(context.Background(), "bash", "", func(_ context.Context, _ io.Writer) (string, error) {
		return "", nil
	}, nil)
	<-j.done

	m.RemoveJob(j.ID)
	if _, err := m.Peek(j.ID); err != ErrJobNotFound {
		t.Errorf("after RemoveJob, Peek should return ErrJobNotFound, got %v", err)
	}
}
