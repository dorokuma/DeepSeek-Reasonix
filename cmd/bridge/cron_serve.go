package main

import (
	"context"
	"time"
)

// contextTimeout returns a cancelable context with a reasonable timeout for
// cron task execution (5 minutes per task). It derives from parent so that
// bridge shutdown cascades cancellation to in-flight cron tasks.
func contextTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 5*time.Minute)
}

// backgroundContext returns a context derived from parent for non-critical
// sends (failure notifications, etc.). Using parent ensures these outbound
// sends are also cancelled when the bridge shuts down.
func backgroundContext(parent context.Context) context.Context {
	return parent
}
