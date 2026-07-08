package jobs

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestKilledJobSurfacesSyntheticResult(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	j, err := m.Start(context.Background(), "task", "x", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(j.ID)

	deadline := time.After(3 * time.Second)
	for {
		text, st, ok := m.Output(j.ID)
		if ok && st == Killed && text != "" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected killed synthetic result, last ok=%v st=%v text=%q", ok, st, text)
		case <-time.After(10 * time.Millisecond):
		}
	}
}