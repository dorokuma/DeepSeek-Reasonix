package agent

import (
	"context"
	"io"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

func TestFindRunningDuplicateTask(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	job, _ := jm.Start(context.Background(), "task", "explore api", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	jm.SetDispatchDigest(job.ID, taskDispatchFingerprint("task", "do the thing"))
	if got := findRunningDuplicateTask(jm, "explore api", "anything"); got == "" {
		t.Fatal("expected duplicate by label")
	}
	if got := findRunningDuplicateTask(jm, "task", "do the thing"); got == "" {
		t.Fatal("expected duplicate by prompt digest")
	}
	if got := findRunningDuplicateTask(jm, "task", "unique prompt xyz"); got != "" {
		t.Fatalf("unexpected dup %q", got)
	}
	jm.Kill(job.ID)
}
