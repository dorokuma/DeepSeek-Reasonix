package control

import (
	"context"
	"log/slog"
	"time"
)

func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		// closeCancel is a no-op placeholder reserved for future use.
		c.Cancel() // cancel the currently running turn so wg.Wait() unblocks

		// wg.Wait with 30-second timeout to prevent hanging on shutdown.
		// Timed-out workers may still be running; we still finish cleanup below.
		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			slog.Warn("controller: shutdown timed out waiting for goroutines; continuing cleanup (workers may still exit later)")
		}

		c.mu.Lock()
		started := c.startedOnce
		c.mu.Unlock()
		if started {
			c.hooks.SessionEnd(context.Background())
		}
		if c.executor != nil {
			c.executor.CleanupCtxStore()
		}
		if c.cleanup != nil {
			c.cleanup()
		}
	})
}

// Wait blocks until any in-flight turns have finished.
func (c *Controller) Wait() {
	c.wg.Wait()
}

// SetBypass turns YOLO/bypass mode on or off for the session: while on, every
// approval prompt is auto-allowed (writers and bash run without asking). Deny
// rules still block. Runtime-only — never written to config.
func (c *Controller) SetBypass(on bool) {
	c.mu.Lock()
	c.bypass = on
	c.mu.Unlock()
}

// Bypass reports whether YOLO/bypass mode is on, for the status-bar indicator.
func (c *Controller) Bypass() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bypass
}
