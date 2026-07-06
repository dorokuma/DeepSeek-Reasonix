package ctxmode

import "context"

type storeKey struct{}

// WithStore stamps ctx with the session store so ctx_read/ctx_search can reach it.
func WithStore(ctx context.Context, s *Store) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, storeKey{}, s)
}

// FromContext returns the store set by the agent, if any.
func FromContext(ctx context.Context) (*Store, bool) {
	s, ok := ctx.Value(storeKey{}).(*Store)
	return s, ok && s != nil
}
