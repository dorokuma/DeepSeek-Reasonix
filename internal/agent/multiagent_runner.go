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
	// Children may spawn further agents (Codex V2); inherit multi-agent tools, drop skill meta.
	subReg := r.Tool.buildSubReg(nil, false)
	bgCtx := WithNestingDepth(ctx, depth)
	if parentAgent := AgentFromContext(ctx); parentAgent != nil {
		bgCtx = WithAgent(bgCtx, parentAgent)
	}
	if opts := OptionsFromContext(ctx); opts != nil {
		bgCtx = WithOptions(bgCtx, opts)
	}
	// Canonical path so list_agents / nested spawn see live tree.
	bgCtx = multiagent.WithAgentPath(bgCtx, path)
	bgCtx = multiagent.WithControl(bgCtx, nil) // 确保子代理没有 Control，禁止嵌套
	return r.Tool.runSub(bgCtx, message, subReg, event.Discard, 0, r.Tool.sysPrompt, "task", "", "")
}

// Ensure MultiAgentRunner implements multiagent.Runner.
var _ multiagent.Runner = (*MultiAgentRunner)(nil)

// filter keeps tool.Registry usage explicit for staticcheck.
var _ = tool.NewRegistry
