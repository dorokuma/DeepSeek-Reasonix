package sandbox

import (
	"strings"
	"testing"
	"time"
)

func TestExecutionEnforcesStepAndToolLimits(t *testing.T) {
	now := time.Now().UTC()
	exec := Start(SandboxContext{MaxSteps: 1, MaxTimeMs: 1000, ToolCallLimit: 1}, now)
	if err := exec.Step(now); err != nil {
		t.Fatal(err)
	}
	if err := exec.Step(now); err == nil || !strings.Contains(err.Error(), "max steps exceeded") {
		t.Fatalf("second step error = %v, want max steps exceeded", err)
	}

	exec = Start(SandboxContext{MaxSteps: 3, MaxTimeMs: 1000, ToolCallLimit: 1}, now)
	if err := exec.AddToolCalls(2, now); err == nil || !strings.Contains(err.Error(), "tool call limit exceeded") {
		t.Fatalf("tool call error = %v, want tool call limit exceeded", err)
	}
}

func TestExecutionKillSwitchTerminatesContext(t *testing.T) {
	now := time.Now().UTC()
	exec := Start(DefaultContext(), now)
	exec.Kill("operator stop", now)
	if err := exec.Step(now); err == nil || !strings.Contains(err.Error(), "operator stop") {
		t.Fatalf("step after kill error = %v, want operator stop", err)
	}
	snap := exec.Snapshot()
	if snap.KillReason != "operator stop" || snap.TerminatedAt.IsZero() {
		t.Fatalf("invalid kill snapshot: %+v", snap)
	}
}
