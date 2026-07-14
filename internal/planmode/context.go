package planmode

import "context"

type activeCtxKey struct{}

// WithActive stamps ctx with whether the executing tool call runs under the
// agent's plan-mode gate. The agent's call-context constructor is the single
// production writer, so the flag stays in lockstep with the gate itself.
// Leaf-package tools (which must not import the agent package) read it via
// Active to defer follow-up work that belongs to an execution turn.
func WithActive(ctx context.Context, active bool) context.Context {
	return context.WithValue(ctx, activeCtxKey{}, active)
}

// Active reports whether ctx carries an active plan-mode flag.
func Active(ctx context.Context) bool {
	active, _ := ctx.Value(activeCtxKey{}).(bool)
	return active
}
