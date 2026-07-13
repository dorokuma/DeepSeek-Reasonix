package main

import (
	"context"
	"time"
)

// contextTimeout returns a cancelable context with a reasonable timeout for
// cron task execution (5 minutes per task). A placeholder; real implementations
// should derive from the bridge's context.
func contextTimeout(_ string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// backgroundContext returns a background context for non-critical sends
// (failure notifications, etc.).
func backgroundContext() context.Context {
	return context.Background()
}
