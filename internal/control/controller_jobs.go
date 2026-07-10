package control

import (
	"log/slog"
)

func (c *Controller) MakeOnComplete() func(jobID string) {
	return nil
}

// MakeOnMessage is retired (mid-flight sub-agent reports removed).
func (c *Controller) MakeOnMessage() func(jobID string) {
	return nil
}

// autoReentryDepthCap limits chained empty auto-reentries. Completions still arm
// pendingToolResult when capped, so a later user turn can drain.
const autoReentryDepthCap = 32

// autoReenter starts a turn when a background task completes so the model can
// report results without waiting for the user's next message.
// Multiple concurrent completions coalesce into a single empty reentry.
func (c *Controller) autoReenter() {
	c.mu.Lock()
	if !c.pendingToolResult.Load() {
		c.mu.Unlock()
		return
	}
	// Coalesce: one empty wake is enough even if N tasks finish together.
	if c.running {
		for _, q := range c.pendingReentryQueue {
			if q == "" {
				c.mu.Unlock()
				return
			}
		}
		c.pendingReentryQueue = append(c.pendingReentryQueue, "")
		c.mu.Unlock()
		return
	}
	if c.autoReentryDepth >= autoReentryDepthCap {
		// Do not drop work: mark for retry when the current empty-turn chain unwinds.
		c.reentryCapPending = true
		c.mu.Unlock()
		slog.Warn("auto-reentry depth cap reached; will retry after current turn ends",
			"cap", autoReentryDepthCap)
		return
	}
	c.autoReentryDepth++
	c.mu.Unlock()
	c.Send("")
}




// PendingToolResult implements agent.ControllerBridge.
func (c *Controller) PendingToolResult() bool {
	return c.pendingToolResult.Load()
}

// PendingToolResultCAS implements agent.ControllerBridge by delegating to the
// Controller's pendingToolResult atomic flag.
func (c *Controller) PendingToolResultCAS(old, new bool) bool {
	return c.pendingToolResult.CompareAndSwap(old, new)
}

// SetPendingToolResult implements agent.ControllerBridge.
func (c *Controller) SetPendingToolResult(v bool) {
	c.pendingToolResult.Store(v)
}

// Send starts a turn with an uncomposed message. The controller applies
// memory and background-job framing inside the async turn path so frontends
// do not block.
