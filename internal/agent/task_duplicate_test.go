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
	RegisterBackgroundDispatchMeta(jm, job.ID, "explore api", "do the thing")
	if got := findRunningDuplicateTask(jm, "explore api", "anything"); got != "" {
		t.Fatalf("same label different prompt should not duplicate, got %q", got)
	}
	if got := findRunningDuplicateTask(jm, "explore api", "do the thing"); got == "" {
		t.Fatal("expected duplicate when label and prompt match fingerprint")
	}
	jm.Kill(job.ID)

	job2, _ := jm.Start(context.Background(), "task", "task", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	RegisterBackgroundDispatchMeta(jm, job2.ID, "task", "do the thing")
	if got := findRunningDuplicateTask(jm, "task", "do the thing"); got == "" {
		t.Fatal("expected duplicate by label+prompt fingerprint")
	}
	if got := findRunningDuplicateTask(jm, "task", "unique prompt xyz"); got != "" {
		t.Fatalf("unexpected dup %q", got)
	}
	jm.Kill(job2.ID)
}
