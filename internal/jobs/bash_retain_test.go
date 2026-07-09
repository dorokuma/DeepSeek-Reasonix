package jobs

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"reasonix/internal/event"
)

func TestCompletedBashRetainedThenGCByAge(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	j, err := m.Start(context.Background(), KindBash, "echo", func(_ context.Context, w io.Writer) (string, error) {
		_, _ = w.Write([]byte("out\n"))
		return "", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if n, ok := m.CompletedResult(j.ID); ok {
			_ = n
			break
		}
		select {
		case <-deadline:
			t.Fatal("bash did not complete")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Still peekable immediately after finish.
	if _, err := m.Peek(j.ID); err != nil {
		t.Fatalf("should retain completed bash: %v", err)
	}
	// Force age past retention and run cleaner.
	job := m.get(j.ID)
	if job == nil {
		t.Fatal("missing job")
	}
	job.mu.Lock()
	job.finishedAt = time.Now().Unix() - completedBashRetainSec - 10
	job.mu.Unlock()
	m.checkAndClean()
	if _, err := m.Peek(j.ID); err != ErrJobNotFound {
		t.Fatalf("aged bash should be GC'd, peek err=%v", err)
	}
}

func TestCompletedBashCountCap(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()
	// Seed finished bash jobs past the cap by inserting completed entries directly.
	m.mu.Lock()
	base := time.Now().Unix()
	for i := 0; i < maxRetainedCompletedBash+5; i++ {
		id := fmt.Sprintf("bash-seed-%d", i)
		j := &Job{
			ID: id, Kind: KindBash, Label: "x",
			status: Done, completed: true, finishedAt: base + int64(i),
			done: make(chan struct{}), cancel: func() {},
		}
		close(j.done)
		m.jobs[id] = j
		m.order = append(m.order, id)
	}
	m.mu.Unlock()
	m.checkAndClean()
	n := 0
	for _, id := range m.ActiveJobs() {
		if k, ok := m.Kind(id); ok && k == KindBash {
			n++
		}
	}
	if n > maxRetainedCompletedBash {
		t.Fatalf("retained bash=%d want <=%d", n, maxRetainedCompletedBash)
	}
}
