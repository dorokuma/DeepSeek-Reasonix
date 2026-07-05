package agent

import (
	"testing"

	"reasonix/internal/provider"
)

func TestSurfaceBackgroundHandoffIfNeeded(t *testing.T) {
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleUser, Content: `<background-task-result job="task-1">
hello world
</background-task-result>`})
	ag := &Agent{session: s}
	out := ag.surfaceBackgroundHandoffIfNeeded(true, "")
	if out == "" || !containsSubstr(out, "hello world") {
		t.Fatalf("got %q", out)
	}
}
