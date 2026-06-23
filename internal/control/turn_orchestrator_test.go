package control

import (
	"context"
	"strings"
	"testing"
)

func TestTurnOrchestratorRunsForegroundUnit(t *testing.T) {
	runner := &fakeTurnRunner{}
	c := New(Options{Runner: runner})
	c.SetPlanMode(true)

	o := newTurnOrchestrator(c)
	if err := o.runTurnWithRawDisplay(context.Background(), "draft the plan", "draft the plan", ""); err != nil {
		t.Fatal(err)
	}

	if len(runner.inputs) != 1 {
		t.Fatalf("runner inputs = %d, want 1", len(runner.inputs))
	}
	if !strings.HasPrefix(runner.inputs[0], PlanModeMarker) {
		t.Fatalf("orchestrator should compose plan marker before running, got %q", runner.inputs[0])
	}
}
