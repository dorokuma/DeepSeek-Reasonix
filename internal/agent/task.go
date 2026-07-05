package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"reasonix/internal/ctxmode"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// DefaultTaskSystemPrompt steers a sub-agent toward focused, terse delivery —
// it doesn't see the parent's conversation so it must self-contain.
const DefaultTaskSystemPrompt = `You are a sub-agent invoked by a parent coding agent to carry out one focused task.
Use the provided tools to investigate or act. Return a single final answer that is concise
and self-contained — the parent will see only that answer, not your tool calls or reasoning.
If you need to ask for clarification, fail with a precise question instead of guessing.`

var subagentMetaTools = []string{
	"task",
	"run_skill",
	"read_skill",
	"install_skill",
	"install_source",
	"explore",
	"research",
	"review",
	"security_review",
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

// TaskTool spawns a sub-agent in its own session for a focused sub-task. The
// sub-agent runs with a filtered tool whitelist and the same step budget shape
// as the parent (see Execute); its tool calls are forwarded to the parent's
// event stream nested under this call, while only its final assistant message is
// returned to the parent model. Use cases: keep noisy tool sequences (multi-file
// exploration, repeated grep / read_file) out of the parent's context budget, or
// parallel research across independent areas (the parallel-dispatch path picks
// these up only when readOnly, which task is not).
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
	resolveProvider   func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error)
	hooks             ToolHooks
}

// NewTaskTool wires a task tool to the parent agent's environment so its
// sub-agents can use the same provider and tools. sysPrompt is the system
// prompt every sub-agent starts with; pass "" for DefaultTaskSystemPrompt. gate
// is the permission gate sub-agents inherit — pass the headless variant so
// deny rules still bite while autonomous sub-agents are never blocked on an
// interactive prompt (there is no UI to answer one). hooks is the parent's hook
// runner; the task tool derives a sub-agent copy from it.
func NewTaskTool(prov provider.Provider, pricing *provider.Pricing, parentReg *tool.Registry,
	contextWindow int, softCompactRatio, compactRatio, compactForceRatio, temperature float64, archiveDir, sysPrompt string, gate Gate,
	resolveProvider func(string, string) (provider.Provider, *provider.Pricing, int, error),
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

func (t *TaskTool) Name() string { return "task" }

func (t *TaskTool) Description() string {
	return "Spawn a sub-agent as a background job for a focused sub-task. Returns immediately with Started task <id>. When the sub-agent finishes, the runtime patches that tool result and may auto-continue the turn with the delivered answer — no polling tools on the main agent. The sub-agent runs in its own session with a filtered tool list."
}

func (t *TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "prompt":{"type":"string","description":"What the sub-agent should accomplish. Be specific about the deliverable — the sub-agent does not see this conversation."},
  "description":{"type":"string","description":"Short label for the sub-task (3-7 words). Surfaced in the dispatch line so the user sees what's running."}
},
"required":["prompt"]
}`)
}

// ReadOnly returns false because a sub-agent can invoke any whitelisted tool,
// including writers. Concurrent() returns true because each sub-agent runs in
// an isolated session, so parallel dispatch is safe.
func (t *TaskTool) ReadOnly() bool { return false }

// Concurrent reports that the task tool is safe to run concurrently because
// each sub-agent operates in an isolated session.
func (t *TaskTool) Concurrent() bool { return true }

// PostCallGuidance implements tool.PostCallGuidance.
func (t *TaskTool) PostCallGuidance(args json.RawMessage) string {
	return taskPostCallGuidance("")
}

// PostCallGuidanceAfter implements tool.PostCallGuidanceWithResult.
func (t *TaskTool) PostCallGuidanceAfter(args json.RawMessage, result string) string {
	return taskPostCallGuidance(extractJobID(result))
}

func extractJobID(result string) string {
	m := startedJobLine.FindStringSubmatch(strings.TrimSpace(result))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func taskPostCallGuidance(jobID string) string {
	rule := `Background job results auto-deliver. The result is appended as a new tool message (name=task) at the conversation tail when finished — see the tailmost tool message for your job_id.`
	idClause := " job_id=task-N (from the Started line above)"
	if jobID != "" {
		idClause = fmt.Sprintf(" job_id=%q (from the Started line above)", jobID)
	}
	return rule + "\n" + idClause
}

func (t *TaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// Depth guard: enforce nesting limit from Agent Options.
	depth := NestingDepthFrom(ctx)
	const defaultMaxNestingDepth = 3
	maxDepth := defaultMaxNestingDepth
	if opts := OptionsFromContext(ctx); opts != nil && opts.MaxNestingDepth > 0 {
		maxDepth = opts.MaxNestingDepth
	}
	if depth >= maxDepth {
		return "", fmt.Errorf("sub-agent blocked: nesting depth limit (%d) reached", maxDepth)
	}

	var p struct {
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}

	maxSteps := 0
	// Default is no limit (0). Only capped when explicitly set by the caller.

	// When the next depth level is still below the limit, allow recursive
	// nesting by including meta-tools in the sub-registry. At the limit we
	// keep the default behaviour which excludes them.
	allowMeta := depth+1 < maxDepth
	subReg := t.buildSubReg(nil, allowMeta)

	// Always run as a background job so the sub-agent survives across turns.
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background execution is not available in this context")
	}
	parentID, _, _, _ := CallContext(ctx)
	nested := event.Discard
	label := p.Description
	if label == "" {
		label = "task"
	}
	onComplete := OnCompleteCallbackFrom(ctx)
	var provider OnCompleteProvider
	if p, ok := OnCompleteProviderFrom(ctx); ok {
		provider = p
	} else if ctrl, ok := CtrlFromContext(ctx); ok {
		provider, _ = ctrl.(OnCompleteProvider)
	}
	var registerMeta jobs.BeforeRunFunc
	if ctrl, ok := CtrlFromContext(ctx); ok {
		registerMeta = func(jobID string) { ctrl.RegisterJobMeta(jobID, parentID) }
	}
	job, err := jm.Start(ctx, "task", label, func(jobCtx context.Context, _ io.Writer) (string, error) {
		// Heartbeat: keep lastActive fresh so the stale monitor (120s inactivity)
		// won't kill a busy task sub-agent whose output doesn't flow through the writer.
		heartbeatDone := make(chan struct{})
		defer close(heartbeatDone)
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatDone:
					return
				case <-ticker.C:
					jobs.UpdateJobActivity(jobCtx)
				}
			}
		}()

		bgCtx := WithNestingDepth(jobCtx, depth+1)
		if parentAgent := AgentFromContext(ctx); parentAgent != nil {
			bgCtx = WithAgent(bgCtx, parentAgent)
		}
		if opts := OptionsFromContext(ctx); opts != nil {
			bgCtx = WithOptions(bgCtx, opts)
		}
		return t.runSub(bgCtx, p.Prompt, subReg, nested, maxSteps)
	}, onComplete, registerMeta)
	if err != nil {
		return "", err
	}
	if provider != nil {
		job.SetOnMessage(provider.MakeOnMessage())
	}
	return fmt.Sprintf("Started task %s (%s)", job.ID, label), nil
}

// buildSubReg returns the sub-agent's tool set: the named whitelist (minus
// subagent/skill meta-tools, to bar recursive nesting), or every parent tool
// except those meta-tools. When allowMeta is true, meta-tools are included
// to permit deeper recursive nesting.
func (t *TaskTool) buildSubReg(names []string, allowMeta bool) *tool.Registry {
	if allowMeta {
		return FilterRegistry(t.parentReg, names)
	}
	return FilterRegistry(t.parentReg, names, SubagentMetaTools()...)
}

// FilterRegistry builds a sub-registry from parent: the named whitelist (empty =
// every parent tool), minus any excluded names. Used to scope what a spawned
// sub-agent — a `task` sub-agent or a subagent skill — may call, e.g. excluding
// `task` to bar recursive nesting, or restricting to a skill's allowed-tools.
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
	if sm, ok := parent.Get("send_message"); ok {
		sub.Add(sm)
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
// sink, and returns its final assistant answer. Shared by the foreground and
// background paths. effort overrides the parent default when non-empty.
func (t *TaskTool) runSub(ctx context.Context, prompt string, subReg *tool.Registry, sink event.Sink, maxSteps int) (string, error) {
	prov, pricing, ctxWin := t.prov, t.pricing, t.contextWindow
	if t.resolveProvider != nil {
		if p, pr, cw, err := t.resolveProvider("", "max"); err == nil {
			prov, pricing, ctxWin = p, pr, cw
		}
		// On error, keep using parent provider (t.prov) — gracefully degrades
		// for provider kinds that don't support effort configuration.
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
	return RunSubAgent(ctx, prov, subReg, t.sysPrompt, prompt, Options{
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
	}, sink)
}

// RunSubAgent runs prompt to completion in a fresh sub-agent session over reg,
// emitting tool activity to sink, and returns the sub-agent's final assistant
// answer. It is the shared core behind the `task` tool and subagent skills: a
// caller supplies the system prompt (the task persona or the skill body), the
// tool registry (already filtered), and the run Options (model budget, gate).
func RunSubAgent(ctx context.Context, prov provider.Provider, reg *tool.Registry, sysPrompt, prompt string, opts Options, sink event.Sink) (string, error) {
	sess := NewSession(sysPrompt)

	// Create an independent jobs manager and ControllerBridge for this
	// sub-agent so it can manage its own background jobs (grandchildren)
	// without sharing state with the parent.
	subJobs := jobs.NewManager(sink)
	subCtrl := newSubControllerBridge()
	subCtrl.jobs = subJobs
	subJobs.SetOnCompletion(func(id string) {
		subCtrl.pendingToolResult.Store(true)
	})
	defer func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		if err := subJobs.WaitRunning(waitCtx); err != nil {
			slog.Warn("sub-agent background jobs still running at shutdown", "err", err)
		}
		subJobs.Close()
	}()

	opts.Jobs = subJobs
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
