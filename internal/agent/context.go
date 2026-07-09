package agent

import (
	"context"
)

// OnCompleteProvider is retained for type compatibility. MakeOnComplete should
// return nil; session completion uses jobs.Manager.SetOnCompletion only.
type OnCompleteProvider interface {
	MakeOnComplete() func(jobID string)
	MakeOnMessage() func(jobID string)
}

// onCompleteKey carries the OnCompleteProvider in the tool call context.
type onCompleteKey struct{}

// WithOnCompleteProvider stamps ctx with the provider (legacy; callbacks are nil).
func WithOnCompleteProvider(ctx context.Context, p OnCompleteProvider) context.Context {
	return context.WithValue(ctx, onCompleteKey{}, p)
}

// OnCompleteProviderFrom extracts the provider from the context, if any.
func OnCompleteProviderFrom(ctx context.Context) (OnCompleteProvider, bool) {
	p, ok := ctx.Value(onCompleteKey{}).(OnCompleteProvider)
	return p, ok
}

// OnCompleteCallbackFrom always returns nil. Prefer Manager.SetOnCompletion.
func OnCompleteCallbackFrom(ctx context.Context) func(jobID string) {
	_ = ctx
	return nil
}

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

// MainAgentDepth is the nesting depth of the session root (main) agent.
const MainAgentDepth = 0

// MaySpawnAsyncSubagent reports whether this context may start a background
// sub-agent job. Async delegation is parent→child only: the main agent may
// spawn children; an already-running sub-agent may not spawn further async jobs.
func MaySpawnAsyncSubagent(ctx context.Context) bool {
	return NestingDepthFrom(ctx) == MainAgentDepth
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
