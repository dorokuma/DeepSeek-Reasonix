package jobs

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
)

// TestConcurrentCompletionAndDrain simulates agent/TUI drainNotify racing job completion.
func TestConcurrentCompletionAndDrain(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()

	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		n := n
		go func() {
			defer wg.Done()
			j, err := m.Start(context.Background(), KindBash, "x", func(ctx context.Context, _ io.Writer) (string, error) {
				time.Sleep(time.Duration(1+n%5) * time.Millisecond)
				return "ok", nil
			}, nil)
			if err != nil {
				return
			}
			<-j.done
			ch := m.NotifyChannels(j.ID)
			if ch != nil {
				select {
				case _, ok := <-ch.Result:
					_ = ok
				default:
				}
			}
			_, _ = m.CompletedResult(j.ID)
			m.RemoveJob(j.ID)
		}()
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during concurrent completion: %v", r)
		}
	}()
	wg.Wait()
}
