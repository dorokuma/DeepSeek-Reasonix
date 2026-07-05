package jobs

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

// TestKillAfterRunReturnsDeliversDone verifies that a job which already produced
// a result is recorded as Done (deliverable) even if Kill races before the
// completion goroutine publishes status.
func TestKillAfterRunReturnsDeliversDone(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	started := make(chan string, 1)
	release := make(chan struct{})

	j, err := m.Start(context.Background(), "task", "race", func(ctx context.Context, _ io.Writer) (string, error) {
		started <- "ok"
		<-release
		return "FINAL-ANSWER", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	_ = m.Kill(j.ID)
	close(release)

	deadline := time.After(3 * time.Second)
	for {
		if n, ok := m.CompletedResult(j.ID); ok && n.Output == "FINAL-ANSWER" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("expected CompletedResult with FINAL-ANSWER after kill/run race")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestWaitRunning(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	_, err := m.Start(context.Background(), "task", "wait", func(ctx context.Context, _ io.Writer) (string, error) {
		defer wg.Done()
		time.Sleep(200 * time.Millisecond)
		return "ok", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.WaitRunning(ctx); err != nil {
		t.Fatalf("WaitRunning: %v", err)
	}
	wg.Wait()
}
