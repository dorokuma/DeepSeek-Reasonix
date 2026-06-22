package agent

import "context"

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
