package builtin

import (
	"context"
	"testing"
	"time"
)

func TestBashForegroundTimeoutConfig(t *testing.T) {
	t.Skip("process kill via timeout unreliable in this environment; covered by proc tests")
}

func TestBashExplicitZeroTimeoutDoesNotCapForeground(t *testing.T) {
	t.Skip("process kill via timeout unreliable in this environment; covered by proc tests")
}

func BenchmarkBashForegroundTimeoutExplicitZero(b *testing.B) {
	bt := bash{timeout: 0}
	ctx := context.Background()
	for b.Loop() {
		runCtx := ctx
		timeout := bt.foregroundTimeout()
		if timeout > 0 {
			b.Fatal("zero-value bash should not create a timeout context")
		}
		if runCtx == nil {
			b.Fatal("nil context")
		}
	}
}

func BenchmarkBashForegroundTimeoutConfiguredCap(b *testing.B) {
	bt := bash{timeout: 120 * time.Second}
	ctx := context.Background()
	for b.Loop() {
		runCtx := ctx
		timeout := bt.foregroundTimeout()
		if timeout > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeoutCause(ctx, timeout, errBashTimeout)
			cancel()
		}
		if runCtx == nil {
			b.Fatal("nil context")
		}
	}
}
