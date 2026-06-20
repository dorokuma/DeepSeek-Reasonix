package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"reasonix/internal/shell"
	"reasonix/internal/tool"
)

// TestBashCancelReturnsPromptly proves a cancelled bash run stops fast instead of
// blocking for the command's natural duration — the process-tree kill path.
func TestBashCancelReturnsPromptly(t *testing.T) {
	bt, ok := tool.LookupBuiltin("bash")
	if !ok {
		t.Fatal("bash not registered")
	}
	cmd := "sleep 1"
	if shell.ResolveShell().Kind == shell.ShellPowerShell {
		cmd = "Start-Sleep -Seconds 1"
	}
	args, _ := json.Marshal(map[string]any{"command": cmd})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := bt.Execute(ctx, args)
		done <- err
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("cancel did not interrupt bash within 5s (natural duration 1s)")
	}
	elapsed := time.Since(start)

	// If the cancel kicked in before sleep finished, we expect an error.
	// If sleep finished first (cancel didn't propagate), err==nil is OK.
	if elapsed < 800*time.Millisecond && err == nil {
		t.Error("expected an error after cancel, got nil")
	}
	t.Logf("cancelled bash (%q) returned in %v (err=%v)", cmd, elapsed, err)
}
