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
	// Started stub stays; kill text lands in synthetic tail tool result.
	if !IsStartedTaskPlaceholder(sess.Messages[1].Content) {
		t.Fatal("Started placeholder must remain after kill delivery")
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != BackgroundDeliveryCallID(job.ID) || last.Content == "" {
		t.Fatalf("want synthetic kill delivery tool row, got %+v", last)
	}
	if IsStartedTaskPlaceholder(last.Content) {
		t.Fatal("delivery content should not be a started stub")
	}
}
