package agent

import "context"

// agentKey is the context key for the parent Agent reference.
type agentKey struct{}

// depthKey is the context key for agent nesting depth.
type depthKey struct{}

// optionsKey is the context key for Agent Options.
type optionsKey struct{}

// activeChildKey controls whether RunSubAgent should register the sub-agent
// as the parent's active child for steer forwarding. Only foreground (not
// background) task tool invocations set this in their context.
type activeChildKey struct{}

// WithNestingDepth stores a nesting depth value in the context.
func WithNestingDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey{}, depth)
}

// NestingDepthFrom extracts the nesting depth from the context.
// Returns 0 when not set.
func NestingDepthFrom(ctx context.Context) int {
	if v := ctx.Value(depthKey{}); v != nil {
		if d, ok := v.(int); ok {
			return d
		}
	}
	return 0
}

// WithOptions stores Agent Options in the context.
func WithOptions(ctx context.Context, opts *Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, opts)
}

// OptionsFromContext extracts the Agent's Options from the context.
// Returns nil when not set.
func OptionsFromContext(ctx context.Context) *Options {
	if v := ctx.Value(optionsKey{}); v != nil {
		if opts, ok := v.(*Options); ok {
			return opts
		}
	}
	return nil
}

// WithAgent stores an Agent reference in the context so sub-agents can merge
// their accumulated cache/cost statistics back into the parent.
func WithAgent(ctx context.Context, a *Agent) context.Context {
	return context.WithValue(ctx, agentKey{}, a)
}

// AgentFromContext extracts the parent Agent from the context.
// Returns nil when not set.
func AgentFromContext(ctx context.Context) *Agent {
	if v := ctx.Value(agentKey{}); v != nil {
		if a, ok := v.(*Agent); ok {
			return a
		}
	}
	return nil
}

// WithActiveChild returns a context that enables active child registration
// in RunSubAgent, so steer messages from the parent are forwarded to the
// foreground sub-agent instead of being queued on the parent.
func WithActiveChild(ctx context.Context) context.Context {
	return context.WithValue(ctx, activeChildKey{}, struct{}{})
}

// isActiveChild reports whether the context has active-child forwarding
// enabled, i.e. the caller is a foreground (not background) task invocation.
func isActiveChild(ctx context.Context) bool {
	return ctx.Value(activeChildKey{}) != nil
}
