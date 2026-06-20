package builtin

import (
	"context"
	"testing"
)

func TestProbeBashCommandEnv(t *testing.T) {
	ctx := context.Background()
	env := bashCommandEnv(ctx)
	t.Logf("env has %d entries", len(env))
	hasPath := false
	for _, e := range env {
		if len(e) > 5 && e[:5] == "PATH=" {
			hasPath = true
			t.Logf("PATH=%s", e[5:])
			break
		}
	}
	if !hasPath {
		t.Error("PATH not found in env")
	}
}
