package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	// Legacy dedicated wrappers (no longer registered on main agent; still excluded
	// if a session still carries them or a parent registry is reused).
	"explore",
	"research",
	"review",
	"security_review",
}

// SkillPlaybook is a runAs=subagent skill resolved for the unified task entry.
type SkillPlaybook struct {
	Name   string
	Body   string // system prompt body (caller may already append shared sections)
	Model  string
	Effort string
}

// SkillLookup resolves a subagent playbook by name. ok is false when unknown or not subagent.
type SkillLookup func(name string) (SkillPlaybook, bool)

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
	// resolveProvider picks the child model. role is "task" or a skill name for config maps.
	resolveProvider func(role, modelRef, effort string) (provider.Provider, *provider.Pricing, int, error)
	hooks           ToolHooks
	skillLookup     SkillLookup
}

// NewTaskTool wires a task tool to the parent agent's environment so its
// sub-agents can use the same provider and tools. sysPrompt is the system
// prompt every freeform sub-agent starts with; pass "" for DefaultTaskSystemPrompt.
// resolveProvider(role, modelRef, effort) selects the child model — role is "task"
// or a skill name. gate is the permission gate sub-agents inherit. hooks is the
// parent's hook runner; the task tool derives a sub-agent copy from it.
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

// SetSkillLookup enables optional skill= playbooks on the unified task entry.
func (t *TaskTool) SetSkillLookup(fn SkillLookup) {
	if t != nil {
		t.skillLookup = fn
	}
}

func (t *TaskTool) Name() string { return "task" }

func (t *TaskTool) Description() string {
	return "Single entry to spawn a background sub-agent. Returns immediately with a JSON started stub (job_id); the final answer is delivered later as a tool result (name=task) — wait for it, do not start another job for the same goal. Freeform: pass prompt only. Playbook: also set skill to a subagent playbook name from the Skills index (e.g. explore, research, review, security-review). Never pair this with run_skill for the same goal."
}

func (t *TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "prompt":{"type":"string","description":"What the sub-agent should accomplish. Be specific about the deliverable — the sub-agent does not see this conversation."},
  "description":{"type":"string","description":"Short label for the sub-task (3-7 words). Surfaced in the dispatch line so the user sees what's running."},
  "skill":{"type":"string","description":"Optional subagent playbook name from the Skills index (e.g. explore, research, review, security-review). Uses that playbook as the child system prompt. Omit for freeform work."}
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
	return ExtractJobIDFromStartedResult(result)
}

func taskPostCallGuidance(jobID string) string {
	rule := `⚠ BACKGROUND JOB STARTED — RESULT AUTO-DELIVERS

The task is running in the background. When it finishes you will receive a user message tagged <background-task-result job="…"> at the END of this conversation (the original Started tool row is also updated). Use that tail message as the authoritative answer — do not re-dispatch.

While waiting, do NOT:
• Call peek-job to check progress (results arrive without polling)
• Call steer-job to ask "are you done" (steer is for new instructions only)
• Dispatch another task for the same goal

Polling wastes context and delays responses. Continue other work or reply to the user instead.`
	idClause := " job_id=task-N (from the started stub above)"
	if jobID != "" {
		idClause = fmt.Sprintf(" job_id=%q (from the started stub above)", jobID)
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
	if !MaySpawnAsyncSubagent(ctx) {
		return "", fmt.Errorf("async sub-agents are parent→child only: a running sub-agent cannot start another background task")
	}

	var p struct {
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
		Skill       string `json:"skill"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	skillName := normalizeTaskSkillName(p.Skill)
	label := strings.TrimSpace(p.Description)
	sysPrompt := t.sysPrompt
	role := "task"
	modelRef, effort := "", ""
	if skillName != "" {
		if t.skillLookup == nil {
			return "", fmt.Errorf("skill playbooks are not available in this session")
		}
		pb, ok := t.skillLookup(skillName)
		if !ok {
			return "", fmt.Errorf("unknown subagent skill %q — use task without skill for freeform work, or a name from the Skills index tagged subagent", skillName)
		}
		sysPrompt = pb.Body
		role = pb.Name
		if role == "" {
			role = skillName
		}
		modelRef, effort = pb.Model, pb.Effort
		if label == "" {
			label = role
		}
	}
	if label == "" {
		label = "task"
	}

	maxSteps := 0
	// Default is no limit (0). Only capped when explicitly set by the caller.

	// Background sub-agents are one level deep only: children never receive
	// task / meta-tools that could spawn async grandchildren.
	subReg := t.buildSubReg(nil, false)

	// Always run as a background job so the sub-agent survives across turns.
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background execution is not available in this context")
	}
	if err := CheckBackgroundDuplicate(jm, label, p.Prompt); err != nil {
		return "", err
	}
	parentID, _, _, _ := CallContext(ctx)
	nested := event.Discard
	onComplete := OnCompleteCallbackFrom(ctx)
	var registerMeta jobs.BeforeRunFunc
	if ctrl, ok := CtrlFromContext(ctx); ok {
		registerMeta = func(jobID string) { ctrl.RegisterJobMeta(jobID, parentID) }
	}
	job, err := jm.Start(ctx, "task", label, func(jobCtx context.Context, _ io.Writer) (string, error) {
		// Heartbeat: keep lastActive fresh so the stale monitor (per-kind idle
		// kill, default 1h for task) won't kill a busy sub-agent whose output
		// doesn't flow through the writer.
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
		return t.runSub(bgCtx, p.Prompt, subReg, nested, maxSteps, sysPrompt, role, modelRef, effort)
	}, onComplete, registerMeta)
	if err != nil {
		return "", err
	}
	RegisterBackgroundDispatchMeta(jm, job.ID, label, p.Prompt)
	return FormatStartedTaskResult(job.ID, label), nil
}

// normalizeTaskSkillName maps model-facing aliases to skill identifiers.
func normalizeTaskSkillName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "_", "-")
	return name
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
	return RunSubAgent(ctx, prov, subReg, sysPrompt, prompt, Options{
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

	// Sub-agents do not get a session jobs manager: async work is parent→child
	// only (main agent → one background child). No grandchildren.
	subCtrl := newSubControllerBridge()
	opts.Jobs = nil
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
