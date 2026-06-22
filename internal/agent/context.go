package agent

import "context"

// agentKey is the context key for the parent Agent reference.
type agentKey struct{}

// depthKey is the context key for agent nesting depth.
type depthKey struct{}

// optionsKey is the context key for Agent Options.
type optionsKey struct{}

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
