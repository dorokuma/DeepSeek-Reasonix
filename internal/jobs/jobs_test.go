package jobs

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// A job runs to completion.
func TestStartDone(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, _ := m.Start(context.Background(), "bash", "echo", func(_ context.Context, out io.Writer) (string, error) {
		io.WriteString(out, "hello\n")
		return "", nil
	}, nil)
	<-j.done
	_, st, ok := m.Output(j.ID)
	if !ok || st != Done {
		t.Fatalf("want Done, got status=%s ok=%v", st, ok)
	}
}

// Output returns only the bytes produced since the previous read.
func TestOutputStreamsIncrementally(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	release := make(chan struct{})
	j, _ := m.Start(context.Background(), "bash", "", func(_ context.Context, out io.Writer) (string, error) {
		io.WriteString(out, "first\n")
		<-release
		io.WriteString(out, "second\n")
		return "", nil
	}, nil)

	waitFor(t, func() bool {
		txt, _, _ := m.Output(j.ID)
		return strings.Contains(txt, "first")
	})
	close(release)
	<-j.done

	txt, st, ok := m.Output(j.ID)
	if !ok || st != Done {
		t.Fatalf("Output after done: ok=%v status=%s", ok, st)
	}
	if !strings.Contains(txt, "second") || strings.Contains(txt, "first") {
		t.Errorf("incremental output = %q, want only the new 'second' chunk", txt)
	}
}

// Kill cancels a running job; a second Kill is a no-op once it has finished.
func TestKill(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, _ := m.Start(context.Background(), "bash", "", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	if !m.Kill(j.ID) {
		t.Fatal("Kill on a running job returned false")
	}
	<-j.done
	_, st, ok := m.Output(j.ID)
	if !ok || st != Killed {
		t.Fatalf("want Killed, got %+v", st)
	}
	if m.Kill(j.ID) {
		t.Error("Kill on a finished job should return false")
	}
}

// Killed status is observable as soon as Kill returns, before the run goroutine
// unwinds.
func TestKillStatusObservableBeforeGoroutineReturns(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	release := make(chan struct{})
	j, _ := m.Start(context.Background(), "bash", "", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		<-release // simulate a teardown that hasn't returned yet
		return "", ctx.Err()
	}, nil)
	if !m.Kill(j.ID) {
		t.Fatal("Kill on a running job returned false")
	}

	// Status should be Killed immediately (set by Kill), before the goroutine returns.
	_, st, ok := m.Output(j.ID)
	if !ok || st != Killed {
		t.Fatalf("want Killed before the goroutine returns, got %s", st)
	}
	if n := len(m.Running()); n != 0 {
		t.Fatalf("a killed job should not still be Running(), got %d", n)
	}

	close(release)
	<-j.done
}

// Close cancels every still-running job.
func TestCloseCancels(t *testing.T) {
	m := NewManager(event.Discard)

	started := make(chan struct{})
	j, _ := m.Start(context.Background(), "task", "", func(ctx context.Context, _ io.Writer) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	<-started
	m.Close()

	<-j.done
	_, st, ok := m.Output(j.ID)
	if !ok || st != Killed {
		t.Fatalf("want Killed after Close, got %s", st)
	}
}

// Running reflects only in-flight jobs.
func TestRunning(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	release := make(chan struct{})
	j, _ := m.Start(context.Background(), "task", "label", func(ctx context.Context, _ io.Writer) (string, error) {
		<-release
		return "answer", nil
	}, nil)
	waitFor(t, func() bool { return len(m.Running()) == 1 })
	if r := m.Running()[0]; r.ID != j.ID || r.Label != "label" {
		t.Errorf("running view = %+v, want id=%s label=label", r, j.ID)
	}
	close(release)
	<-j.done
	waitFor(t, func() bool { return len(m.Running()) == 0 })
}
