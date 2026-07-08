package agent

import (
	"context"
	"io"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

func TestSemanticDuplicateDetectsParaphrase(t *testing.T) {
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	pol := jobs.DefaultManagerPolicies()
	pol.SemanticDedup.Enabled = true
	pol.SemanticDedup.Threshold = 0.6
	jm.Configure(pol)

	job, _ := jm.Start(context.Background(), "task", "explore", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil)
	RegisterBackgroundDispatchMeta(jm, job.ID, "explore", "please help me find all uses of foo in the repo")

	if got := findRunningDuplicateTask(jm, "explore", "find all uses of foo in the repository"); got == "" {
		t.Fatal("expected semantic duplicate for paraphrased prompt")
	}
	if got := findRunningDuplicateTask(jm, "explore", "audit database migration scripts"); got != "" {
		t.Fatalf("unrelated prompt should not duplicate, got %q", got)
	}
}