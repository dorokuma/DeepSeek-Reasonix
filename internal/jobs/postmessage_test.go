package jobs

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
)

func TestPostMessageAfterJobCompleteNoPanic(t *testing.T) {
	m := NewManager(event.Discard)
	var wg sync.WaitGroup
	_, err := m.Start(context.Background(), "task", "race", func(ctx context.Context, _ io.Writer) (string, error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(20 * time.Millisecond)
			PostMessage(ctx, "late")
		}()
		time.Sleep(5 * time.Millisecond)
		return "done", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	wg.Wait()
	time.Sleep(50 * time.Millisecond)
}
