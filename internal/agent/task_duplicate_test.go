package agent

import (
	"context"
	"io"
	"strings"
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
