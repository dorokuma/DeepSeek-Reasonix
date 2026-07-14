package multiagent

import "context"

type ctxKey struct{}

// WithControl stamps Control on the tool context.
// Passing nil explicitly clears any previously set Control, preventing
// parent Control from leaking into sub-agents.
func WithControl(ctx context.Context, c *Control) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns Control if present.
func FromContext(ctx context.Context) (*Control, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Control)
	return c, ok && c != nil
}

type pathKey struct{}

// WithAgentPath stamps the current agent's canonical path (for child spawns).
func WithAgentPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, pathKey{}, path)
}

// AgentPathFrom returns current path or RootPath.
func AgentPathFrom(ctx context.Context) string {
	if v, ok := ctx.Value(pathKey{}).(string); ok && v != "" {
		return v
	}
	return RootPath
}
