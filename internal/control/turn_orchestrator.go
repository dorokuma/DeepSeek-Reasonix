package control

import "context"

// turnOrchestrator owns foreground turn execution. The first slice introduces
// the boundary; subsequent slices move the ordered lifecycle behind it.
type turnOrchestrator struct {
	c *Controller
}

func newTurnOrchestrator(c *Controller) *turnOrchestrator {
	return &turnOrchestrator{c: c}
}

func (o *turnOrchestrator) runTurnWithRawDisplay(ctx context.Context, input, raw, display string) error {
	return o.c.runTurnWithRawDisplay(ctx, input, raw, display)
}
