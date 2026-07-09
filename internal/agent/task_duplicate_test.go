package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

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
		t.Fatal("expected duplicate when prompt matches fingerprint")
	}
	// Different display label, same goal → still duplicate (goal-primary).
	if got := findRunningDuplicateTask(jm, "task", "do the thing"); got == "" {
		t.Fatal("expected cross-label duplicate for the same prompt")
	}
	jm.Kill(job.ID)

	job2, _ := jm.Start(context.Background(), "task", "task", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	RegisterBackgroundDispatchMeta(jm, job2.ID, "task", "do the thing")
	if got := findRunningDuplicateTask(jm, "explore", "do the thing"); got == "" {
		t.Fatal("expected goal-primary duplicate across labels")
	}
	if got := findRunningDuplicateTask(jm, "task", "unique prompt xyz"); got != "" {
		t.Fatalf("unexpected dup %q", got)
	}
	jm.Kill(job2.ID)
}

func TestFindDuplicateIncludesCompletedPendingDelivery(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	finished := make(chan struct{})
	job, err := jm.Start(context.Background(), "task", "explore", func(ctx context.Context, _ io.Writer) (string, error) {
		close(finished)
		return "answer", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	RegisterBackgroundDispatchMeta(jm, job.ID, "explore", "map the auth package")
	<-finished
	// Wait until the job leaves Running but stays in ActiveJobs (delivery not yet taken).
	deadline := time.Now().Add(2 * time.Second)
	for {
		stillRunning := false
		for _, v := range jm.Running() {
			if v.ID == job.ID {
				stillRunning = true
				break
			}
		}
		if !stillRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := findRunningDuplicateTask(jm, "task", "map the auth package"); got != job.ID {
		t.Fatalf("completed-but-retained job must still block re-dispatch, got %q want %q", got, job.ID)
	}
	// Paraphrase should also hit via semantic key while the job is retained.
	pol := jobs.DefaultManagerPolicies()
	pol.SemanticDedup.Enabled = true
	pol.SemanticDedup.Threshold = 0.5
	jm.Configure(pol)
	if got := findRunningDuplicateTask(jm, "task", "map auth package"); got == "" {
		t.Fatal("expected semantic duplicate against retained completed job")
	}
	jm.RemoveJob(job.ID)
	if got := findRunningDuplicateTask(jm, "task", "map the auth package"); got != "" {
		t.Fatalf("after RemoveJob re-dispatch should be free, got %q", got)
	}
}

func TestFingerprintDoesNotCollideOnLongSharedPrefix(t *testing.T) {
	head := strings.Repeat("same-prefix-block ", 40)
	a := taskDispatchFingerprint("explore", head+"TAIL-A")
	b := taskDispatchFingerprint("explore", head+"TAIL-B")
	if a == "" || b == "" {
		t.Fatal("expected non-empty fingerprints")
	}
	if a == b {
		t.Fatal("long prompts that only share a prefix must not share fingerprint")
	}
}
