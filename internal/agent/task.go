package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/ctxmode"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/multiagent"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// DefaultTaskSystemPrompt steers a sub-agent toward focused, terse delivery —
// it doesn't see the parent's conversation so it must self-contain.
const DefaultTaskSystemPrompt = `You are a sub-agent invoked by a parent coding agent to carry out one focused task.
Use the provided tools to investigate or act. Return a single final answer that is concise
and self-contained — the parent will see only that answer, not your tool calls or reasoning.
If you need to ask for clarification, fail with a precise question instead of guessing.`

// Meta tools children must not inherit (Codex still allows spawn_agent on children).
var subagentMetaTools = []string{
	"run_skill",
	"install_skill",
	"install_source",
}

// SubagentMetaTools returns the tool names that spawned agents should not inherit
// from the parent registry unless a future call site deliberately opts into a
// different boundary. They can spawn or author more agent work, so excluding them
// preserves one layer of delegation without adding a spawn-count cap.
func SubagentMetaTools() []string {
	out := make([]string, len(subagentMetaTools))
	copy(out, subagentMetaTools)
	return out
}

// TaskTool is the single entry for spawning a background sub-agent — freeform
// prompt only. The sub-agent runs with a filtered tool whitelist; only its final
// assistant message is returned to the parent (via a runtime observation message).
type TaskTool struct {
	prov              provider.Provider
	pricing           *provider.Pricing
	parentReg         *tool.Registry
	contextWindow     int
	softCompactRatio  float64
	compactRatio      float64
	compactForceRatio float64
	temperature       float64
	archiveDir        string
	sysPrompt         string
	gate              Gate
	// resolveProvider picks the child model. role is always "task" for freeform work.
	resolveProvider func(role, modelRef, effort string) (provider.Provider, *provider.Pricing, int, error)
	hooks           ToolHooks
}

// NewTaskTool wires a task tool to the parent agent's environment so its
// sub-agents can use the same provider and tools. sysPrompt is the system
// prompt every sub-agent starts with; pass "" for DefaultTaskSystemPrompt.
// resolveProvider(role, modelRef, effort) selects the child model — role is "task".
// gate is the permission gate sub-agents inherit. hooks is the parent's hook runner.
func NewTaskTool(prov provider.Provider, pricing *provider.Pricing, parentReg *tool.Registry,
	contextWindow int, softCompactRatio, compactRatio, compactForceRatio, temperature float64, archiveDir, sysPrompt string, gate Gate,
	resolveProvider func(string, string, string) (provider.Provider, *provider.Pricing, int, error),
	hooks ToolHooks) *TaskTool {
	if sysPrompt == "" {
		sysPrompt = DefaultTaskSystemPrompt
	}
	return &TaskTool{
		prov:              prov,
		pricing:           pricing,
		parentReg:         parentReg,
		contextWindow:     contextWindow,
		softCompactRatio:  softCompactRatio,
		compactRatio:      compactRatio,
		compactForceRatio: compactForceRatio,
		temperature:       temperature,
		archiveDir:        archiveDir,
		sysPrompt:         sysPrompt,
		gate:              gate,
		resolveProvider:   resolveProvider,
		hooks:             hooks,
	}
}

// TaskTool is an INTERNAL sub-agent kernel only (runSub). It is NOT a model-facing
// tool. Codex MultiAgent V2 uses spawn_agent / wait_agent / …. Do not register this
// type on a tool.Registry — Name is deliberately not a public tool name.
//
// Name is intentionally empty so accidental registry.Add is skipped/ignored.
func (t *TaskTool) Name() string { return "" }

// Description is unused (not model-facing).
func (t *TaskTool) Description() string { return "" }

// Schema is unused (not model-facing).
func (t *TaskTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

// ReadOnly is unused (not model-facing).
func (t *TaskTool) ReadOnly() bool { return false }

// Execute refuses if ever invoked as a tool.
func (t *TaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	_ = ctx
	_ = args
	return "", fmt.Errorf("tool \"task\" was removed; use spawn_agent (Codex multi-agent V2)")
}

// buildSubReg returns the sub-agent's tool set: the named whitelist (minus
// meta-tools that could re-spawn work), or every parent tool except those
// meta-tools. When allowMeta is true, meta-tools are included.
func (t *TaskTool) buildSubReg(names []string, allowMeta bool) *tool.Registry {
	if allowMeta {
		return FilterRegistry(t.parentReg, names)
	}
	return FilterRegistry(t.parentReg, names, SubagentMetaTools()...)
}

// FilterRegistry builds a sub-registry from parent: the named whitelist (empty =
// every parent tool), minus any excluded names. Used to scope what a spawned
// sub-agent may call, e.g. excluding `task` to bar recursive nesting.
func FilterRegistry(parent *tool.Registry, names []string, exclude ...string) *tool.Registry {
	ex := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		ex[e] = true
	}
	sub := tool.NewRegistry()
	src := names
	if len(src) == 0 {
		src = parent.Names()
	}
	for _, name := range src {
		if ex[name] {
			continue
		}
		if tl, ok := parent.Get(name); ok {
			sub.Add(tl)
		}
	}
	return sub
}

var plannerNonResearchTools = []string{
	"ask",
	"slash_command",
	"todo_write",
}

// PlannerToolRegistry returns the tool set exposed to the two-model planner:
// read-only research tools only. It deliberately excludes workflow/meta tools
// that are technically read-only but can prompt the user, update visible task
// state, wait on jobs, or expand commands instead of inspecting context.
func PlannerToolRegistry(parent *tool.Registry) *tool.Registry {
	exclude := append(SubagentMetaTools(), plannerNonResearchTools...)
	return FilterReadOnlyRegistry(parent, exclude...)
}

// FilterReadOnlyRegistry builds a sub-registry containing only tools whose
// ReadOnly contract is true, minus explicit exclusions.
func FilterReadOnlyRegistry(parent *tool.Registry, exclude ...string) *tool.Registry {
	ex := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		ex[e] = true
	}
	sub := tool.NewRegistry()
	if parent == nil {
		return sub
	}
	for _, name := range parent.Names() {
		if ex[name] {
			continue
		}
		tl, ok := parent.Get(name)
		if !ok || !tl.ReadOnly() {
			continue
		}
		sub.Add(tl)
	}
	return sub
}

// runSub builds a sub-agent over subReg, runs prompt to completion emitting to
// sink, and returns its final assistant answer. sysPrompt/role/model/effort
// configure the child (playbook or freeform default).
func (t *TaskTool) runSub(ctx context.Context, prompt string, subReg *tool.Registry, sink event.Sink, maxSteps int, sysPrompt, role, modelRef, effort string) (string, error) {
	const subAgentMaxSteps = 200
	if maxSteps <= 0 || maxSteps > subAgentMaxSteps {
		maxSteps = subAgentMaxSteps
	}
	if t.resolveProvider == nil {
		return "", fmt.Errorf("subagent model resolver not configured")
	}
	if strings.TrimSpace(sysPrompt) == "" {
		sysPrompt = t.sysPrompt
	}
	if strings.TrimSpace(role) == "" {
		role = "task"
	}
	prov, pricing, ctxWin, err := t.resolveProvider(role, modelRef, effort)
	if err != nil {
		return "", err
	}
	var shared *ctxmode.Store
	if s, ok := ctxmode.FromContext(ctx); ok {
		shared = s
	}
	// Derive sub-agent hooks with the subagent agent layer, so hooks scoped
	// to "main" are skipped in sub-agents.
	subHooks := t.hooks
	if r, ok := t.hooks.(*hook.Runner); ok && r != nil {
		subHooks = r.WithAgentLayer(hook.AgentLayerSubagent)
	}
	opts := Options{
		MaxSteps:          maxSteps,
		Temperature:       t.temperature,
		Pricing:           pricing,
		Gate:              t.gate,
		Hooks:             subHooks,
		ContextWindow:     ctxWin,
		SoftCompactRatio:  t.softCompactRatio,
		CompactRatio:      t.compactRatio,
		CompactForceRatio: t.compactForceRatio,
		ArchiveDir:        t.archiveDir,
		CtxStore:          shared,
	}
	// Codex: children share the session MultiAgent control + own agent path.
	if c, ok := multiagent.FromContext(ctx); ok {
		opts.MultiAgent = c
	}
	if p := multiagent.AgentPathFrom(ctx); p != "" {
		opts.AgentPath = p
	}
	return RunSubAgent(ctx, prov, subReg, sysPrompt, prompt, opts, sink)
}

// RunSubAgent runs prompt to completion in a fresh sub-agent session over reg,
// emitting tool activity to sink, and returns the sub-agent's final assistant
// answer. It is the shared core behind the `task` tool: the caller supplies the
// system prompt, tool registry (already filtered), and run Options.
func RunSubAgent(ctx context.Context, prov provider.Provider, reg *tool.Registry, sysPrompt, prompt string, opts Options, sink event.Sink) (string, error) {
	sess := NewSession(sysPrompt)

	// Sub-agents do not get a session jobs manager: async work is parent→child
	// only (main agent → one background child). No grandchildren.
	subCtrl := newSubControllerBridge()
	opts.Ctrl = subCtrl

	sub := New(prov, reg, sess, opts, sink)
	sub.SetAsker(subCtrl)

	// mergeSubUsage merges the sub-agent's accumulated cache/cost stats into
	// the parent agent. Called on both success and failure paths so token
	// consumption is never lost.
	mergeSubUsage := func() {
		if parentAgent := AgentFromContext(ctx); parentAgent != nil {
			hit := sub.sessCacheHit.Load()
			miss := sub.sessCacheMiss.Load()
			prompt := sub.sessPromptTokens.Load()
			total := sub.sessTotalTokens.Load()
			var cost float64
			var currency string
			if v := sub.sessCostInfo.Load(); v != nil {
				info, _ := v.(sessionCostInfo)
				cost = info.cost
				currency = info.currency
			}
			parentAgent.AddSessionUsage(hit, miss, prompt, total, cost, currency)
		}
	}
	if err := sub.Run(ctx, prompt); err != nil {
		mergeSubUsage()
		return "", fmt.Errorf("sub-agent: %w", err)
	}
	mergeSubUsage()
	// Walk the session backwards for the last assistant message with content —
	// that's the sub-agent's final answer. Intermediate assistant messages with
	// tool_calls but no text don't count.
	var ans string
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		m := sess.Messages[i]
		if m.Role == provider.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			ans = m.Content
			break
		}
	}
	if ans == "" {
		return "", fmt.Errorf("sub-agent finished without producing a final answer")
	}

	parentSess := SessionFromContext(ctx)
	if parentSess != nil {
		parentReads := globalFileStateRegistry.GetReads(parentSess)
		subWrites := globalFileStateRegistry.GetWrites(sess)
		var overlap []string
		readMap := make(map[string]bool)
		for _, r := range parentReads {
			readMap[r] = true
		}
		for _, w := range subWrites {
			if readMap[w] {
				overlap = append(overlap, w)
			}
		}
		if len(overlap) > 0 {
			ans = fmt.Sprintf("%s\n\n[NOTE: sub-agent modified files %v ... please re-read before editing]", ans, overlap)
		}
	}
	return ans, nil
}

// subSinkFor builds the nesting sink from an already-captured parent ID + stream,
// for the background path where the job runs under a context that no longer
// carries the call context. Falls back to Discard when there's no parent stream.
func subSinkFor(parentID string, parent event.Sink) event.Sink {
	if parent == nil {
		return event.Discard
	}
	return event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.ToolDispatch, event.ToolResult:
			e.Tool.ParentID = parentID
			e.Tool.ID = parentID + "/" + e.Tool.ID
			parent.Emit(e)
		}
	})
}
