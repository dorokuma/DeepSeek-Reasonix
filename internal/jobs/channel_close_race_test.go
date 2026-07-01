package jobs

import (
	"context"
	"io"
	"testing"

	"reasonix/internal/event"
)

// TestJobCompletionKeepsResultChannelOpen verifies the fix for TUI panics:
// upstream jobs.go closed resultCh before the run() goroutine finished, which
// could panic with "send on closed channel" when result delivery raced completion.
func TestJobCompletionKeepsResultChannelOpen(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, err := m.Start(context.Background(), "task", "keep-open", func(ctx context.Context, _ io.Writer) (string, error) {
		return "done", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-j.done

	ch := m.NotifyChannels(j.ID)
	if ch == nil {
		t.Fatal("NotifyChannels returned nil")
	}

	// Channel must still be open: a closed channel returns (zero, false) immediately.
	select {
	case got, ok := <-ch.Result:
		if !ok {
			t.Fatal("resultCh was closed after job completion (upstream bug)")
		}
		if got.Output != "done" {
			t.Fatalf("result = %q, want done", got.Output)
		}
	default:
		if notify, ok := m.CompletedResult(j.ID); !ok || notify.Output != "done" {
			t.Fatal("resultCh not readable and CompletedResult missing")
		}
	}
}