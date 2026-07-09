package agent

import (
	"context"

	"reasonix/internal/event"
	"reasonix/internal/multiagent"
	"reasonix/internal/tool"
)

// MultiAgentRunner adapts TaskTool/runSub machinery to multiagent.Runner (Codex child thread).
type MultiAgentRunner struct {
	Tool    *TaskTool
	Control *multiagent.Control
}

// Run executes a spawned agent to completion.
func (r *MultiAgentRunner) Run(ctx context.Context, path, message string, depth int) (string, error) {
	if r == nil || r.Tool == nil {
		return "", context.Canceled
	}
	// Children may spawn further agents (Codex V2); keep multi-agent tools + exclude skill meta only.
	subReg := r.Tool.buildSubReg(nil, false)
	// Ensure multi-agent tools exist on child registry if parent had them.
	if r.Control != nil {
		// tools already on parentReg via boot RegisterTools
		_ = r.Control
	}
	bgCtx := WithNestingDepth(ctx, depth)
	if parentAgent := AgentFromContext(ctx); parentAgent != nil {
		bgCtx = WithAgent(bgCtx, parentAgent)
	}
	if opts := OptionsFromContext(ctx); opts != nil {
		bgCtx = WithOptions(bgCtx, opts)
	}
	bgCtx = multiagent.WithAgentPath(bgCtx, path)
	if r.Control != nil {
		bgCtx = multiagent.WithControl(bgCtx, r.Control)
	}
	// Isolated session: no parent jobs manager for async grandchildren via old jobs path.
	return r.Tool.runSub(bgCtx, message, subReg, event.Discard, 0, r.Tool.sysPrompt, "task", "", "")
}

// Ensure MultiAgentRunner implements multiagent.Runner.
var _ multiagent.Runner = (*MultiAgentRunner)(nil)

// filter keeps tool.Registry usage explicit for staticcheck.
var _ = tool.NewRegistry
