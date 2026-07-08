package agent

import (
	"context"
	"io"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
)

func TestCompleteBackgroundJobAfterKill(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()

	job, err := jm.Start(context.Background(), "task", "x", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil, func(id string) {})
	if err != nil {
		t.Fatal(err)
	}
	sess := agentNewSessionWithStartedTask("call-k", job.ID)
	ag := New(nil, nil, sess, Options{Jobs: jm}, event.Discard)
	ag.SetControllerBridge(newSubControllerBridge())

	_ = jm.Kill(job.ID)
	deadline := time.After(3 * time.Second)
	for {
		if ag.CompleteBackgroundJob(job.ID) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("CompleteBackgroundJob after kill did not commit")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if sess.Messages[1].Role != provider.RoleTool || sess.Messages[1].Content == "" {
		t.Fatalf("tool row should contain killed message, got %+v", sess.Messages[1])
	}
	if IsStartedTaskPlaceholder(sess.Messages[1].Content) {
		t.Fatal("started placeholder should be replaced")
	}
}