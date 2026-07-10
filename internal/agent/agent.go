package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"reasonix/internal/ctxmode"
	"reasonix/internal/diag"
	"reasonix/internal/diff"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/hook"
	"reasonix/internal/instruction"
	"reasonix/internal/jobs"
	"reasonix/internal/memory"
	"reasonix/internal/multiagent"
	"reasonix/internal/nilutil"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// maxToolOutputBytes caps a single tool result before it goes into the model's
// context. ~32KB is roughly 8K tokens — enough for a full file read or a busy
// grep, while preventing one accidental "read this 5 MB log" from blowing the
// window before the next compaction runs.
const maxToolOutputBytes = 32 * 1024

const maxFinalReadinessBlocks = 3
const maxEmptyFinalBlocks = 3
const maxStreamRecoveries = 1
const maxExecutorHandoffNudges = 1

// Renderer redraws the assistant's final-answer text as styled output. It is
// applied only after a turn's text stream completes, so the user sees raw
// markdown stream live, then a single redraw replaces it with formatted
// output. The renderer is intentionally interface-shaped so the agent stays
// independent of the cli's markdown library choice. Consumed by TextSink.
type Renderer interface {
	Render(text string) string
}

// Asker puts structured multiple-choice questions to the user and blocks for the
// answers. The agent consults it for the `ask` tool. It is interface-shaped so
// the agent stays independent of the frontend; a nil asker means no interactive
// user (headless runs), where `ask` returns a "decide for yourself" result. The
// interactive frontends wire the controller in as the Asker.
type Asker interface {
	Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error)
}

// ctrlKey carries the ControllerBridge in the tool call context.
type ctrlKey struct{}

// withCtrl stamps ctx with the ControllerBridge so tools (notably the `task`
// tool) can register job metadata during Execute.
func withCtrl(ctx context.Context, c ControllerBridge) context.Context {
	cctx := context.WithValue(ctx, ctrlKey{}, c)
	return tool.WithCtrl(cctx, c)
}

// CtrlFromContext extracts the ControllerBridge from the context, if any.
func CtrlFromContext(ctx context.Context) (ControllerBridge, bool) {
	cc, ok := ctx.Value(ctrlKey{}).(ControllerBridge)
	return cc, ok
}

// callContextKey carries the executing tool call's identity into Execute.
type callContextKey struct{}

// callContext is the per-call context a tool can read. parentID is the call being
// executed and sink is the agent's event sink (the `task` tool uses both to nest
// a sub-agent's events under this call); asker lets the `ask` tool reach the user.
type callContext struct {
	parentID string
	sink     event.Sink
	asker    Asker
}

// withCallContext stamps ctx with the executing call's ID, the agent's sink, and
// the asker. executeOne sets this before every Execute; `task` reads it (via
// CallContext) to nest sub-agent events, and `ask` reads the asker to prompt.
func withCallContext(ctx context.Context, parentID string, sink event.Sink, asker Asker) context.Context {
	cctx := context.WithValue(ctx, callContextKey{}, callContext{parentID: parentID, sink: sink, asker: asker})
	return tool.WithCallID(cctx, parentID)
}

// CallContext returns the executing call's ID, the agent's sink, and the asker,
// if the context was set by an agent's executeOne. ok is false for a plain
// context (headless tool tests, calls made outside the run loop).
func CallContext(ctx context.Context) (parentID string, sink event.Sink, asker Asker, ok bool) {
	cc, ok := ctx.Value(callContextKey{}).(callContext)
	if !ok {
		return "", nil, nil, false
	}
	return cc.parentID, cc.sink, cc.asker, true
}

// Gate decides, per tool call, whether it may run. The agent consults it at
// execute time (after the gate). It is interface-shaped so the agent
// stays independent of the permission package and of how "ask" is resolved
// (silently in headless runs, interactively in the chat TUI). A nil gate means
// no gating — every call runs, preserving behaviour for callers that don't wire
// one in. reason is fed back to the model when allow is false; a non-nil err
// (e.g. ctx cancelled awaiting approval) is treated as a block for that call.
type Gate interface {
	Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (allow bool, reason string, err error)
}

// ToolHooks fires user-configured shell hooks around each tool call. PreToolUse
// runs before the call and may block it (block=true; message is the reason fed
// back to the model); when modified is non-nil the caller MUST use those args
// instead of the original. PostToolUse runs after and only surfaces output to
// the user (it can't block). It is interface-shaped so the agent stays
// independent of the hook package — a nil hooks field disables hook firing
// entirely.
type ToolHooks interface {
	PreToolUse(ctx context.Context, name string, args json.RawMessage) (block bool, message string, modified json.RawMessage)
	PostToolUse(ctx context.Context, name string, args json.RawMessage, result string)
	// PostLLMCall fires after each model turn completes (streaming finishes)
	// but before reasoning_content is stored. It returns the (possibly
	// translated) reasoning string — the original when no hook is configured.
	// HasPostLLMCall reports whether such a hook exists, so the agent keeps
	// streaming reasoning live when none is wired up.
	PostLLMCall(ctx context.Context, reasoning string, turn int) string
	HasPostLLMCall() bool
	PreCompact(ctx context.Context, trigger string) string
}

// PostToolRewriter is an optional extension to ToolHooks. When the hook
// implementation also satisfies this interface, PostToolRewrite is called
// after PostToolUse and may transform the tool result string before it is
// fed back to the model. Panics are recovered; on panic the original result
// is kept.
type PostToolRewriter interface {
	PostToolRewrite(ctx context.Context, name string, args json.RawMessage, result string) string
}

// sessionCostInfo bundles the cumulative cost and its currency for atomic storage.
type sessionCostInfo struct {
	cost     float64
	currency string
}

// ControllerBridge is the interface the Controller implements so the Agent can
// check for pending tool results and consume job metadata without a direct
// import dependency on the control package.
type ControllerBridge interface {
	// PendingToolResult reports whether a completed background task is waiting
	// to be drained (peek only; does not clear the flag).
	PendingToolResult() bool
	// PendingToolResultCAS atomically compares-and-swaps the pendingToolResult
	// flag. Returns true when the swap succeeded (old value matched).
	PendingToolResultCAS(old, new bool) bool
	// SetPendingToolResult sets the pending-tool-result flag (e.g. re-arm after
	// a drain race).
	SetPendingToolResult(v bool)
	// TakeJobMeta reads and deletes a job's metadata in one atomic step.
	// Returns the tool-call ID that created the job, and whether metadata existed.
	TakeJobMeta(jobID string) (toolCallID string, found bool)
	// RegisterJobMeta stores the tool-call metadata for a background task job
	// so TakeJobMeta can later correlate a completed job with its tool call.
	RegisterJobMeta(jobID, toolCallID string)
}

// subControllerBridge is a lightweight ControllerBridge implementation for
// headless sub-agents (no session-scoped background jobs).
type subControllerBridge struct {
	taskResults       map[string]string
	taskResultsMu     sync.Mutex
	pendingToolResult atomic.Bool
}

func newSubControllerBridge() *subControllerBridge {
	return &subControllerBridge{
		taskResults: make(map[string]string),
	}
}

func (c *subControllerBridge) PendingToolResult() bool {
	return c.pendingToolResult.Load()
}

func (c *subControllerBridge) PendingToolResultCAS(old, new bool) bool {
	return c.pendingToolResult.CompareAndSwap(old, new)
}

func (c *subControllerBridge) SetPendingToolResult(v bool) {
	c.pendingToolResult.Store(v)
}

func (c *subControllerBridge) TakeJobMeta(jobID string) (toolCallID string, found bool) {
	c.taskResultsMu.Lock()
	defer c.taskResultsMu.Unlock()
	toolCallID, found = c.taskResults[jobID]
	if found {
		delete(c.taskResults, jobID)
	}
	return
}

func (c *subControllerBridge) RegisterJobMeta(jobID string, toolCallID string) {
	c.taskResultsMu.Lock()
	c.taskResults[jobID] = toolCallID
	c.taskResultsMu.Unlock()
}

// MakeOnComplete implements OnCompleteProvider. Always nil: sub-agents cannot
// spawn async grandchildren, and parent completion uses SetOnCompletion only.
func (c *subControllerBridge) MakeOnComplete() func(jobID string) {
	return nil
}

// MakeOnMessage is retired.
func (c *subControllerBridge) MakeOnMessage() func(jobID string) {
	return nil
}

// Ask implements Asker. Sub-agents are headless — they cannot prompt the user.
func (c *subControllerBridge) Ask(_ context.Context, _ []event.AskQuestion) ([]event.AskAnswer, error) {
	return nil, fmt.Errorf("sub-agent does not support interactive prompts")
}

// Agent is the core task loop. It drives a single model — a Provider plus a tool
type Agent struct {
	prov        provider.Provider
	tools       *tool.Registry
	session     *Session
	sessMu      sync.Mutex // guards the session pointer for external Session()/SetSession
	maxSteps    int
	maxStepsKey string
	// mainAgentAllowed, when non-nil, is the whitelist of tools the root
	// (depth-0) agent may invoke. nil means no restriction.
	mainAgentAllowed map[string]bool

	maxMainAgentReadonlyCalls int
	readonlyCallsCount        atomic.Int64

	// executorHandoffGuard is enabled by Coordinator for the executor agent. The
	// per-turn marker check in Run keeps ordinary single-model turns unaffected.
	executorHandoffGuard bool
	temperature          float64
	pricing              *provider.Pricing

	// sink receives the turn's typed event stream (reasoning/text deltas, tool
	// dispatch/results, usage, notices). The agent no longer formats output
	// itself — a frontend's Sink decides how to render. Never nil; New defaults
	// it to event.Discard.
	sink event.Sink

	// lastUsage caches the most recent per-turn telemetry the provider reported so
	// the CLI can expose a context gauge without re-scraping the usage line. The
	// run loop writes it while a frontend's status line reads it, so it is atomic.
	lastUsage atomic.Pointer[provider.Usage]

	// sessCacheHit/sessCacheMiss accumulate cache tokens across every API call
	// this session, so frontends can show the aggregate hit-rate (Σhit/Σ(hit+miss))
	// — a steadier, cost-oriented number than the single-turn rate. They are NOT
	// reset on compaction (compaction only rewrites session.Messages), so the
	// aggregate never craters when the prefix is summarized away. Atomic: the run
	// loop accumulates them while the status line reads them.
	sessCacheHit     atomic.Int64
	sessCacheMiss    atomic.Int64
	sessCostInfo     atomic.Value // stores sessionCostInfo{cost, currency}
	sessCostMu       sync.Mutex   // guards sessCostInfo Load-Modify-Store sequences
	sessPromptTokens atomic.Int64 // cumulative prompt tokens across all API calls
	sessTotalTokens  atomic.Int64 // cumulative total tokens across all API calls

	// lastPrefixShape records the previous provider request's cacheable prefix
	// so usage events can explain prefix churn on the next request.
	lastPrefixShape     PrefixShape
	haveLastPrefixShape bool

	// gate, when non-nil, is the per-call permission gate consulted for each
	// tool call. nil disables gating entirely.
	gate Gate

	// hooks, when non-nil, fires PreToolUse / PostToolUse shell hooks around each
	// tool call. nil disables hook firing.
	hooks ToolHooks

	// asker, when non-nil, lets the `ask` tool put questions to the user. nil in
	// headless runs (no interactive user). Set via SetAsker.
	asker Asker

	// onPreEdit, when non-nil, is called with a writer tool's previewed change
	// just before it runs — the seam the checkpoint store uses to snapshot a
	// file's pre-edit content. Only fires for non-ReadOnly tools that implement
	// tool.Previewer (so bash, whose targets are unknowable, is never tracked).
	// Set via SetPreEditHook.
	onPreEdit func(diff.Change)

	// jobs, when non-nil, is the session's background-job manager. executeOne
	// stamps it onto each tool call's context so the background tools (bash
	// run_in_background, peek-job/cancel-job) can reach it. nil leaves those
	// tools to degrade gracefully.
	jobs *jobs.Manager

	// multiAgent is Codex MultiAgent V2 control (spawn_agent / wait_agent / ...).
	multiAgent *multiagent.Control

	// agentPath is this agent's canonical multi-agent path (Codex AgentPath).
	// Root session is multiagent.RootPath; spawned children get e.g. /root/explore.
	agentPath string

	// ctrl, when non-nil, is the Controller bridge for auto-reentry and job
	// metadata lookup. Set via Options.
	ctrl ControllerBridge

	// projectChecks are structured project instructions the agent gates tool
	// calls against after each turn that wrote files.
	projectChecks []instruction.VerifyCheck

	// writeFailureVerifier appends a footer when write/edit tools failed this turn.
	writeFailureVerifier bool

	// memQueue, when non-nil, lets the remember/forget tools fold a turn-tail note
	// about a just-made memory change into the next turn, so it applies this
	// session without touching the cache-stable prefix. Set via SetMemoryQueue.
	memQueue memory.Queue

	// ctxStore holds sandboxed tool output for ctx_read/ctx_search/ctx_run. nil when REASONIX_CTX=off.
	ctxStore *ctxmode.Store

	// sentFragments tracks fragment ID → last-sent content across turns.
	// It is NOT persisted to checkpoint; on cold start it resets and all
	// fragments are re-sent once (acceptable trade-off).
	sentFragments map[string]string

	// Context management: when a turn's prompt nears contextWindow, the older
	// middle of the session is summarized away, keeping a token-bounded recent
	// tail verbatim (recentKeep is the message floor) and archiving the originals
	// under archiveDir. compactStuck latches when compaction can't get the prompt
	// under the window (consecutiveCompacts crosses the limit), so auto-compaction
	// pauses instead of looping. softCompactNoticed gates the one-shot soft-ratio
	// notice so it fires once per approach, not every turn.
	contextWindow       int
	softCompactRatio    float64
	compactRatio        float64
	compactForceRatio   float64
	softCompactNoticed  bool
	recentKeep          int
	archiveDir          string
	compactStuck        bool
	consecutiveCompacts int

	// stormSig / stormCount track a run of turns that keep failing the same way so
	// the loop can break a death-spiral. The signature is each call's (tool, error)
	// in order, NOT (tool, args): a stuck model reliably reworks the arguments
	// cosmetically (a re-worded essay, a reordered object) while the call fails
	// identically every time — keying on args misses the loop entirely (observed
	// live against truncated tool-call arguments). Because errors that embed their
	// subject (e.g. "file not found: /x") differ per target, genuine varied probing
	// does not collapse to one signature. Reset whenever a turn does anything else
	// (a different failure shape, or any success). See applyStormBreaker.
	stormSig   string
	stormCount int

	// repeatSuccessCounts tracks write-like tool calls that have already
	// succeeded in this user turn. This catches the complementary loop shape to
	// stormSig: a model keeps doing the same successful write, so there is no
	// error for the failure-only storm breaker to see.
	repeatSuccessCounts map[string]int
	repeatSuccessMu     sync.Mutex

	// steerCh receives external user messages injected via Steer() while Run()
	// is executing. Buffered 8; drops when full.
	steerCh chan string

	toolsDynamic map[string]bool

	// diagnosticRequested exposes dynamic tools (e.g. peek-job) for one turn.
	diagnosticRequested atomic.Bool
}

// SetGate installs the per-call permission gate. Used by `reasonix chat` to swap the
// headless gate built in setup for an interactive one that prompts the user;
// nil disables gating. Safe to call before the run loop starts.
func (a *Agent) SetGate(g Gate) {
	if nilutil.IsNil(g) {
		g = nil
	}
	a.gate = g
}

// SetAsker installs the asker the `ask` tool uses to question the user.
// Interactive frontends wire one in; headless runs leave it nil.
func (a *Agent) SetAsker(as Asker) { a.asker = as }

// SetMemoryQueue installs the sink the remember/forget tools use to apply a
// memory change in the current session. The controller wires itself in.
func (a *Agent) SetMemoryQueue(q memory.Queue) { a.memQueue = q }

// SetPreEditHook installs the pre-edit snapshot hook (see onPreEdit). The
// controller wires it to its per-session checkpoint store; nil disables capture.
func (a *Agent) SetPreEditHook(fn func(diff.Change)) { a.onPreEdit = fn }

// SetControllerBridge wires a ControllerBridge for auto-reentry and job metadata
// lookup. Must be called before Run if auto-reentry is desired.
func (a *Agent) SetControllerBridge(c ControllerBridge) { a.ctrl = c }

// SetDiagnosticRequested enables dynamic tools (e.g. peek-job) for the next call.
func (a *Agent) SetDiagnosticRequested(v bool) { a.diagnosticRequested.Store(v) }

// DiagnosticRequested reports whether dynamic tools should be visible.
func (a *Agent) DiagnosticRequested() bool { return a.diagnosticRequested.Load() }

// Session returns the agent's current conversation, useful for persistence
// hooks that need to read the message log between turns. sessMu serialises this
// pointer read against SetSession, so a frontend (serve's concurrent /history and
// /new handlers) can't race the swap. The run loop touches a.session directly and
// only swaps it via SetSession while idle, so its reads need no lock.
func (a *Agent) Session() *Session {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	return a.session
}

// SetSession replaces the agent's conversation wholesale. Used by
// `reasonix chat --resume` to load a saved JSONL transcript before the first turn,
// so the model picks up exactly where it left off. Callers serialise it against a
// running turn (it only fires while idle); sessMu guards the pointer swap itself.
func (a *Agent) SetSession(s *Session) {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	a.session = s
}

// GetLastUserMessage returns the content of the last role=user message in the
// session. Returns "" when the session is nil or no user message exists. Safe
// for concurrent use with SetSession.
func (a *Agent) GetLastUserMessage() string {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	if a.session == nil {
		return ""
	}
	msgs := a.session.Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// LastUsage returns the most recent per-turn token telemetry the provider
// reported (nil if no turn has run yet). The TUI uses it to show a context
// gauge alongside the prompt; the actual cache decisions still live inside
// maybeCompact.
func (a *Agent) LastUsage() *provider.Usage { return a.lastUsage.Load() }

// SessionCache returns the cumulative cache hit/miss prompt tokens across every
// API call this session — the basis for the status line's aggregate hit-rate.
func (a *Agent) SessionCache() (hit, miss int) {
	return int(a.sessCacheHit.Load()), int(a.sessCacheMiss.Load())
}

// SessionTokens returns the cumulative prompt and total tokens across every API
// call this session. Used by the controller to persist and restore session stats.
func (a *Agent) SessionTokens() (prompt, total int64) {
	return a.sessPromptTokens.Load(), a.sessTotalTokens.Load()
}

// SessionCost returns the cumulative conversation cost and its currency symbol.
func (a *Agent) SessionCost() (cost float64, currency string) {
	if v := a.sessCostInfo.Load(); v != nil {
		info, ok := v.(sessionCostInfo)
		if !ok {
			return 0, ""
		}
		return info.cost, info.currency
	}
	return 0, ""
}

// ResetSessionCost zeros the cumulative cost — used on /new session rotation.
func (a *Agent) ResetSessionCost() {
	a.sessCostInfo.Store(sessionCostInfo{})
	a.sessCacheHit.Store(0)
	a.sessCacheMiss.Store(0)
	a.sessPromptTokens.Store(0)
	a.sessTotalTokens.Store(0)
}

// addSessionCost atomically adds cost to the cumulative session cost. The mutex
// protects against concurrent Load-Modify-Store from AddSessionUsage and the
// stream loop's ChunkUsage handler.
func (a *Agent) addSessionCost(cost float64, currency string) {
	a.sessCostMu.Lock()
	defer a.sessCostMu.Unlock()
	prev := a.sessCostInfo.Load()
	var info sessionCostInfo
	if prev != nil {
		info, _ = prev.(sessionCostInfo)
	}
	info.cost += cost
	if info.currency == "" {
		info.currency = currency
	}
	a.sessCostInfo.Store(info)
}

// SetSessionCost restores cumulative cost from a loaded session sidecar.
func (a *Agent) SetSessionCost(cost float64, currency string) {
	a.sessCostInfo.Store(sessionCostInfo{cost: cost, currency: currency})
}

// SetSessionCache restores cumulative cache/token statistics from a loaded
// session sidecar. This is the counterpart of SetSessionCost for cache stats.
func (a *Agent) SetSessionCache(hit, miss, prompt, total int64) {
	a.sessCacheHit.Store(hit)
	a.sessCacheMiss.Store(miss)
	a.sessPromptTokens.Store(prompt)
	a.sessTotalTokens.Store(total)
}

// AddSessionUsage merges a sub-agent's accumulated cache/cost counters into
// the parent agent's session-level totals so frontends see a unified number.
func (a *Agent) AddSessionUsage(hit, miss, prompt, total int64, cost float64, currency string) {
	a.sessCacheHit.Add(hit)
	a.sessCacheMiss.Add(miss)
	a.sessPromptTokens.Add(prompt)
	a.sessTotalTokens.Add(total)
	if cost > 0 || currency != "" {
		a.addSessionCost(cost, currency)
	}
}

// ContextWindow returns the configured context-window size in tokens. 0
// means compaction is disabled for this agent.
func (a *Agent) ContextWindow() int { return a.contextWindow }

// CompactRatio returns the fraction of the window at which auto-compaction
// fires (e.g. 0.8). The status line uses it to show headroom to the next compact.
func (a *Agent) CompactRatio() float64 { return a.compactRatio }

// ResetCtxStore removes the current store and opens a fresh one when ctxmode is on.
// Called on /new session rotation.
func (a *Agent) ResetCtxStore() {
	if a.ctxStore != nil {
		a.ctxStore.Remove()
		a.ctxStore = nil
	}
	if ctxmode.Active() {
		a.ctxStore = ctxmode.NewStore()
	}
}

// CleanupCtxStore removes on-disk sandbox data. Called when the controller closes.
func (a *Agent) CleanupCtxStore() {
	if a.ctxStore != nil {
		a.ctxStore.Remove()
		a.ctxStore = nil
	}
}

// CompactNow runs one compaction pass immediately, regardless of the
// usage-ratio threshold maybeCompact normally honours. Used by the chat
// TUI's `/compact` command so the user can reset the prefix before it
// naturally fills up.
func (a *Agent) CompactNow(ctx context.Context, instructions string) error {
	return a.compact(ctx, "manual", instructions, true)
}

// Options configures an Agent.
type Options struct {
	MaxSteps int
	// MaxStepsKey names the configuration knob shown when the MaxSteps guard is
	// hit. Empty defaults to agent.max_steps.
	MaxStepsKey string
	Temperature float64
	Pricing     *provider.Pricing // optional, for per-turn cost display

	// Gate is the per-call permission gate. nil disables gating.
	Gate Gate

	// Context management. ContextWindow <= 0 disables compaction. Ratios and
	// RecentKeep fall back to defaults when unset.
	ContextWindow     int
	SoftCompactRatio  float64
	CompactRatio      float64
	CompactForceRatio float64
	RecentKeep        int
	ArchiveDir        string

	// Hooks fires PreToolUse / PostToolUse shell hooks around tool calls. nil
	// disables hook firing.
	Hooks ToolHooks

	// Jobs is the session's background-job manager (nil disables background tools).
	Jobs *jobs.Manager

	// MultiAgent is Codex MultiAgent V2 control for the session (nil disables).
	MultiAgent *multiagent.Control

	// AgentPath is this agent's canonical multi-agent path. Empty = /root.
	AgentPath string

	// Ctrl is the optional Controller bridge for auto-reentry and job metadata.
	Ctrl ControllerBridge

	// CtxStore optionally shares a parent session's ctxmode store (sub-agents).
	// When nil and ctxmode is active, New creates a fresh store.
	CtxStore *ctxmode.Store

	// ProjectChecks are host-observable structured checks extracted during boot.
	ProjectChecks []instruction.VerifyCheck

	// KeepMultimodalTurns controls how many recent turns retain their full
	// multimodal content before SanitizeMultiModalParts prunes older parts.
	// Default 3 when unset. 0 or negative disables pruning.
	// TODO(Phase 4): wire into SanitizeHistory / buildRequest paths.
	KeepMultimodalTurns int

	// MaxNestingDepth sets the maximum allowed nesting depth for sub-agents.
	// When this limit is reached, spawning a new sub-agent is blocked.
	// Default 3 when unset. Must be >= 1.
	MaxNestingDepth int

	// MainAgentAllowed is the whitelist of tools the root (depth-0) agent may
	// invoke. When nil, no restriction is applied — all registered tools are
	// available. Set a custom map to restrict which tools the root agent sees.
	MainAgentAllowed map[string]bool

	// ToolsDynamic lists tools hidden until diagnosticRequested (e.g. peek-job).
	ToolsDynamic map[string]bool

	// MaxMainAgentReadonlyCalls limits the maximum number of readonly tool calls
	// the main agent (nesting depth 0) can make. 0 or negative means unlimited.
	MaxMainAgentReadonlyCalls int
}

// New constructs an Agent. MaxSteps <= 0 means no cap — the run loop continues
// until the model gives a final answer, the context is cancelled, or the
// provider errors (compaction keeps the context bounded). A nil sink is replaced
// with event.Discard so the agent can always emit unconditionally.
func New(prov provider.Provider, tools *tool.Registry, session *Session, opts Options, sink event.Sink) *Agent {
	if opts.SoftCompactRatio <= 0 {
		opts.SoftCompactRatio = defaultSoftCompactRatio
	}
	if opts.CompactRatio <= 0 {
		opts.CompactRatio = defaultCompactRatio
	}
	if opts.CompactForceRatio <= 0 {
		opts.CompactForceRatio = defaultCompactForceRatio
	}
	if opts.RecentKeep <= 0 {
		opts.RecentKeep = minRecentKeep
	}
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	if tools == nil {
		tools = tool.NewRegistry()
	}
	gate := opts.Gate
	if nilutil.IsNil(gate) {
		gate = nil
	}
	hooks := opts.Hooks
	if nilutil.IsNil(hooks) {
		hooks = nil
	}
	ctxStore := opts.CtxStore
	if ctxStore == nil && ctxmode.Active() {
		ctxStore = ctxmode.NewStore()
	}

	// Wire RTK pipe compaction into the PostToolRewrite hook.
	if hooks != nil {
		if r, ok := hooks.(*hook.Runner); ok {
			r.SetRTKCompaction(hook.NewRTKRewriter(opts.Jobs))
		}
	}

	maxStepsKey := opts.MaxStepsKey
	if strings.TrimSpace(maxStepsKey) == "" {
		maxStepsKey = "agent.max_steps"
	}
	agentPath := strings.TrimSpace(opts.AgentPath)
	if agentPath == "" {
		agentPath = multiagent.RootPath
	}
	return &Agent{
		prov:                      prov,
		tools:                     tools,
		session:                   session,
		maxSteps:                  opts.MaxSteps,
		maxStepsKey:               maxStepsKey,
		temperature:               opts.Temperature,
		pricing:                   opts.Pricing,
		sink:                      sink,
		gate:                      gate,
		hooks:                     hooks,
		jobs:                      opts.Jobs,
		multiAgent:                opts.MultiAgent,
		agentPath:                 agentPath,
		ctrl:                      opts.Ctrl,
		ctxStore:                  ctxStore,
		sentFragments:             make(map[string]string),
		projectChecks:             append([]instruction.VerifyCheck(nil), opts.ProjectChecks...),
		writeFailureVerifier:      true,
		contextWindow:             opts.ContextWindow,
		softCompactRatio:          opts.SoftCompactRatio,
		compactRatio:              opts.CompactRatio,
		compactForceRatio:         opts.CompactForceRatio,
		recentKeep:                opts.RecentKeep,
		archiveDir:                opts.ArchiveDir,
		mainAgentAllowed:          opts.MainAgentAllowed,
		toolsDynamic:              opts.ToolsDynamic,
		maxMainAgentReadonlyCalls: opts.MaxMainAgentReadonlyCalls,
	}
}

// Run appends the user input and drives the tool loop until the model returns a
// final answer (no tool calls), the context is cancelled, or the provider errors.
// With maxSteps <= 0 the loop is unbounded — the natural termination is the model
// finishing, and the real safety bounds are user cancellation and compaction, not
// a round count. A positive maxSteps imposes an optional hard guard, surfaced as
// a resumable notice when hit.
func (a *Agent) Run(ctx context.Context, input string) error {
	if a.steerCh == nil {
		a.steerCh = make(chan string, 8)
	}
	a.repeatSuccessCounts = nil
	a.sink.Emit(event.Event{Kind: event.TurnStarted})
	// Parse multimodal data URLs embedded in the input text (e.g.
	// [REASONIX_IMAGE:data:image/jpeg;base64,...]) and convert them to
	// ContentPart objects so the model can "see" images directly.
	wakeForBackground := input == "" && a.ctrl != nil && a.ctrl.PendingToolResult()
	if input == "" && a.multiAgent != nil && a.multiAgent.Mailbox().HasPendingFor(a.agentPath) {
		wakeForBackground = true
	}
	if input != "" || !wakeForBackground {
		cleanInput, parts := parseMultimodalInput(input)
		a.session.Add(provider.Message{Role: provider.RoleUser, Content: cleanInput, Parts: parts})
		if a.ctxStore != nil {
			ctxmode.RecordUserPrompt(a.ctxStore.Journal(), input)
		}
	}
	// Codex mailbox: inject any pending inter-agent mail into the session before the model runs.
	a.flushMultiAgentMailbox()

	finalReadinessBlocks := 0
	emptyFinalBlocks := 0
	handoffNudges := 0
	usedAnyTool := false
	streamRecoveries := 0
	executorHandoff := a.executorHandoffGuard && strings.Contains(input, executorHandoffMarker)
	for step := 0; a.maxSteps <= 0 || step < a.maxSteps; step++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// After wait_agent wakes on mailbox activity, inject mail before the next model call.
		a.flushMultiAgentMailbox()

		if parentSteer := jobs.DrainJobSteer(ctx); parentSteer != "" {
			jobs.RecordAck(ctx, "received: "+parentSteer)
			a.session.Add(provider.Message{Role: provider.RoleUser, Content: "[Parent steer]: " + parentSteer})
		}

		// Background wake: multiagent mailbox / pending flag only.
		if wakeForBackground && step == 0 {
			// multiagent mailbox only (jobs no longer auto-deliver into session)
			if !a.sessionHasUnreadTaskResult() {
				if a.ctrl != nil {
					a.ctrl.PendingToolResultCAS(true, false)
				}
				return nil
			}
		} else {
			if userInput := a.drainSteer(); userInput != "" {
				a.session.Add(provider.Message{Role: provider.RoleUser, Content: userInput})
			}
			a.drainNotify()
		}

		schemas := a.getSchemasForContext(ctx)
		prefixShape := a.capturePrefixShape(schemas)
		prevPrefixShape := a.lastPrefixShape
		if !a.haveLastPrefixShape {
			prevPrefixShape = prefixShape
		}

		text, reasoning, signature, calls, usage, interrupted, partialToolStarted, err := a.stream(ctx, step+1)
		if err != nil {
			if interrupted && streamRecoveries < maxStreamRecoveries {
				streamRecoveries++
				if hasVisibleFinalAnswer(text, reasoning) || partialToolStarted {
					if hasVisibleFinalAnswer(text, reasoning) {
						// 如果已生成可见的最终回答，或者已生成了推理思考过程，则需要将当前的 assistant 消息添加进会话 session。
						// 这样做是为了保留已经生成的推理思考过程（reasoning），避免在接下来的重试中丢失。
						// 如果不保存该推理，模型在重试时需要从头重新进行长时间思考（长考），
						// 这不仅耗费资源，还极易在后续生成中再次由于达到 max_tokens 限制而被异常截断。
						a.session.Add(provider.Message{
							Role:               provider.RoleAssistant,
							Content:            text,
							ReasoningContent:   reasoning,
							ReasoningSignature: signature,
						})
					}
					a.session.AddUserNudge(streamRecoveryMessage(hasVisibleFinalAnswer(text, reasoning), partialToolStarted))
				}
				a.sink.Emit(event.Event{Kind: event.Retrying, RetryAttempt: streamRecoveries, RetryMax: maxStreamRecoveries})
				step-- // recovery retries do not consume the tool-round maxSteps budget
				continue
			}
			return err
		}
		streamRecoveries = 0
		cacheDiagnostics := CompareShape(prevPrefixShape, prefixShape, usage)
		a.lastPrefixShape = prefixShape
		a.haveLastPrefixShape = true
		if usage != nil && usage.TotalTokens > 0 {
			var sCost float64
			var sCurrency string
			if v := a.sessCostInfo.Load(); v != nil {
				info, _ := v.(sessionCostInfo) // zero-value on type mismatch
				sCost = info.cost
				sCurrency = info.currency
			}
			a.sink.Emit(event.Event{Kind: event.Usage, Usage: usage, Pricing: a.pricing,
				CacheDiagnostics: &cacheDiagnostics,
				SessionHit:       int(a.sessCacheHit.Load()), SessionMiss: int(a.sessCacheMiss.Load()),
				SessionCost: sCost, SessionCurrency: sCurrency,
				SessionPrompt: int(a.sessPromptTokens.Load()),
				SessionTotal:  int(a.sessTotalTokens.Load())})
		}
		if msg, ok := finishReasonMessage(usage); ok {
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg})
		}

		assistantMsg := provider.Message{
			Role:               provider.RoleAssistant,
			Content:            text,
			ReasoningContent:   reasoning,
			ReasoningSignature: signature,
			ToolCalls:          calls,
		}

		if len(calls) == 0 {
			// text = a.surfaceBackgroundHandoffIfNeeded(wakeForBackground, text)  // removed by owner
			if text == "" {
				if reasoning == "" {
					emptyFinalBlocks++
				}
				if emptyFinalBlocks >= maxEmptyFinalBlocks {
					return fmt.Errorf("model finished without a visible final answer %d times", emptyFinalBlocks)
				}
				// a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "empty final answer blocked: model returned no visible answer text; retrying"})
				// 在重试前，先将已生成的 assistant 消息（包含已生成的 reasoning 思考过程和 signature 等）添加进会话 session。
				// 这样做是为了保留已有的推理思考过程，避免重试时模型需要从头重新进行长时间思考（长考），
				// 如果不保存，模型重新思考不仅耗费资源，还极易在下一次输出时再次因为达到 max_tokens 而被截断。
				if reasoning != "" {
					a.session.Add(assistantMsg)
					a.session.Add(provider.Message{Role: provider.RoleUser, Content: emptyFinalRetryMessage()})
				}
				a.maybeCompact(ctx, usage)
				continue
			}
			if executorHandoff && !usedAnyTool && handoffNudges < maxExecutorHandoffNudges {
				handoffNudges++
				a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "executor answered without taking any action; nudging it to use its tools"})
				a.session.Add(provider.Message{Role: provider.RoleUser, Content: executorHandoffRetryMessage()})
				a.maybeCompact(ctx, usage)
				continue
			}
			readiness := a.finalReadinessCheck()
			if readiness.applies {
				event.RecordReadinessAudit(a.sink, readiness.audit(evidence.ReadinessAllowed, finalReadinessBlocks > 0))
			}
			// Do not finalize while session-scoped background jobs are still running.
			if a.jobs != nil && len(a.jobs.Running()) > 0 {
				a.session.Add(assistantMsg)
				return nil
			}
			a.session.Add(assistantMsg)
			return nil // model gave a normal final answer
		}
		// Keep reasoning_content on tool-call turns for session archive.
		a.session.Add(assistantMsg)
		emptyFinalBlocks = 0
		usedAnyTool = true

		results := a.executeBatch(ctx, calls)
		for i, call := range calls {
			a.session.Add(provider.Message{
				Role:       provider.RoleTool,
				Content:    results[i],
				ToolCallID: call.ID,
				Name:       call.Name,
			})
		}
		if len(calls) > 0 {
			jobs.RecordProgress(ctx, step+1, calls[len(calls)-1].Name)
		}

		// The prompt only grows from here; compact before the next turn so it
		// stays within the model's window.
		a.maybeCompact(ctx, usage)
	}
	// Only reached when a positive maxSteps guard is configured. The work so far
	// is already in the session, so the user can just send another message to pick
	// up where it left off.
	return fmt.Errorf("paused after %d tool-call rounds (%s) — the work so far is saved; send another message to continue, or set %s higher or to 0 for no limit", a.maxSteps, a.maxStepsKey, a.maxStepsKey)
}

// Steer injects a user message into the running agent's message loop.
// Non-blocking: silently drops when the buffer is full.
// Also notifies MultiAgent wait_agent (Codex Steer activity).
func (a *Agent) Steer(input string) {
	if a.multiAgent != nil {
		a.multiAgent.NotifySteer()
	}
	if a.steerCh == nil {
		return
	}
	select {
	case a.steerCh <- input:
	default:
	}
}

// drainSteer drains one pending steer message and returns it.
// Returns empty string when nothing is pending.
func (a *Agent) drainSteer() string {
	select {
	case msg := <-a.steerCh:
		return msg
	default:
		return ""
	}
}

// flushMultiAgentMailbox drains Codex-style inter-agent mail addressed to this agent.
func (a *Agent) flushMultiAgentMailbox() {
	if a == nil || a.multiAgent == nil || a.session == nil {
		return
	}
	path := a.agentPath
	if path == "" {
		path = multiagent.RootPath
	}
	mails := a.multiAgent.Mailbox().DrainFor(path)
	if len(mails) == 0 {
		return
	}
	body := multiagent.FormatMailsForSession(mails)
	if body == "" {
		return
	}
	a.session.Add(provider.Message{Role: provider.RoleUser, Content: body})
}

// drainNotify is a no-op (legacy mid-turn job drain removed).
func (a *Agent) drainNotify() bool {
	return false
}

type finalReadinessCheck struct {
	applies              bool
	reason               string
	missingProjectChecks int
	incompleteTodos      int
}

func (c finalReadinessCheck) audit(result evidence.ReadinessAuditResult, recovered bool) evidence.ReadinessAudit {
	return evidence.ReadinessAudit{
		Result:                 result,
		Recovered:              recovered,
		MissingProjectChecks:   c.missingProjectChecks,
		IncompleteTodos:        c.incompleteTodos,
		CommandMismatchMissing: c.missingProjectChecks,
	}
}

func (a *Agent) finalReadinessCheck() finalReadinessCheck {
	var missing []string
	out := finalReadinessCheck{}
	hasProjectChecks := len(a.projectChecks) > 0
	if !hasProjectChecks {
		if len(missing) > 0 {
			out.reason = strings.Join(missing, "; ")
		}
		return out
	}
	out.applies = true
	for _, check := range a.projectChecks {
		command := strings.TrimSpace(check.Command)
		if command == "" {
			continue
		}
		out.missingProjectChecks++
		missing = append(missing, fmt.Sprintf("run %q from %s after the latest write", command, finalReadinessCheckSource(check)))
	}

	if len(missing) == 0 {
		return out
	}
	out.reason = strings.Join(missing, "; ")
	return out
}

func finalReadinessCheckSource(check instruction.VerifyCheck) string {
	source := strings.TrimSpace(check.SourcePath)
	if source == "" {
		source = "project memory"
	}
	if check.Line > 0 {
		return fmt.Sprintf("%s:%d", source, check.Line)
	}
	return source
}

func finalReadinessRetryMessage(reason string) string {
	return "Host final-answer readiness check failed. Before giving a final answer, address the missing host-observable receipts: " + reason + ". Run the required tool calls, then answer when readiness is satisfied."
}

func executorHandoffRetryMessage() string {
	return `You are already in the executor phase. The planner's read-only limitations do not apply to you.

Do not answer as the planner and do not ask how to trigger the executor.
Use your available tools now to carry out the task. If a write or command is blocked by permissions or workspace boundaries, state that specific blocker and ask for the needed approval/path.`
}

func hasVisibleFinalAnswer(text, reasoning string) bool {
	return strings.TrimSpace(text) != "" || strings.TrimSpace(reasoning) != ""
}

func emptyFinalRetryMessage() string {
	return "The previous assistant response finished without any visible answer text. Continue the same task now and provide a concise visible answer to the user. Do not send reasoning only."
}

func streamRecoveryMessage(hasPartialText, hadPartialTool bool) string {
	switch {
	case hadPartialTool:
		return "The previous assistant response was interrupted while a tool call was streaming. Continue the same task now. If a tool is still needed, issue a fresh complete tool call from scratch; do not rely on any partial tool-call arguments from the interrupted stream."
	case hasPartialText:
		return "The previous assistant response was interrupted during streaming. Continue the same task from immediately after the partial assistant message above. Do not repeat text that is already visible."
	default:
		return "The previous assistant response was interrupted during streaming before visible answer text was completed. Continue the same task now and provide the next useful response."
	}
}

// stream runs one completion, emitting reasoning and text deltas as typed
// events and collecting complete tool calls. A Message event closes the text
// stream so a sink can re-render the streamed raw text as styled markdown. The
// accumulated text and reasoning are also returned so the caller can round-trip
// reasoning on the next turn.
func (a *Agent) stream(ctx context.Context, turn int) (string, string, string, []provider.ToolCall, *provider.Usage, bool, bool, error) {
	if a.prov == nil {
		return "", "", "", nil, nil, false, false, fmt.Errorf("agent: no provider configured")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ctx = provider.WithRetryNotify(ctx, func(info provider.RetryInfo) {
		a.sink.Emit(event.Event{Kind: event.Retrying, RetryAttempt: info.Attempt, RetryMax: info.Max})
	})
	// Apply fragment dedup: replace already-sent fragments with <fragment-ref>
	// tags. This is a transient transformation — we do NOT write the filtered
	// messages back to the Session (would break compaction).
	msgs := a.session.Snapshot()
	msgs, nextSent := CalculateDiffAndFilter(msgs, a.sentFragments)
	ch, err := a.prov.Stream(ctx, provider.Request{
		Messages:    msgs,
		Tools:       a.getSchemasForContext(ctx),
		Temperature: a.temperature,
	})
	if err != nil {
		return "", "", "", nil, nil, false, false, err
	}
	a.sentFragments = nextSent

	// A PostLLMCall hook rewrites the whole reasoning block, so when one is wired
	// up we buffer reasoning silently and emit the transformed text once after the
	// stream. With no such hook the reasoning streams live, chunk by chunk, as
	// before — the common case must not lose its live "thinking…" display.
	transformReasoning := a.hooks != nil && a.hooks.HasPostLLMCall()

	var text, reasoning strings.Builder
	var signature string // provider-issued proof for the reasoning (Anthropic thinking)
	var calls []provider.ToolCall
	var usage *provider.Usage
	var partialToolStarted bool
	finishReasoning := func() (stored, display string) {
		original := reasoning.String()
		display = original
		if transformReasoning && original != "" {
			display = a.hooks.PostLLMCall(ctx, original, turn)
			if display != "" {
				a.sink.Emit(event.Event{Kind: event.Reasoning, Text: display})
			}
		}
		stored = display
		if signature != "" {
			stored = original
		}
		return stored, display
	}
	for {
		var chunk provider.Chunk
		select {
		case <-ctx.Done():
			stored, _ := finishReasoning()
			return text.String(), stored, signature, calls, usage, false, partialToolStarted, ctx.Err()
		case c, ok := <-ch:
			if !ok {
				// With a PostLLMCall hook, the live stream was suppressed above; transform the
				// full reasoning now and emit it once so the sink never sees the untranslated
				// text. Without a hook this is skipped — the chunk-by-chunk events already fired.
				stored, display := finishReasoning()
				// Store the transformed reasoning — except when a provider signature pins it to
				// the original text (Anthropic extended thinking). That signed thinking block is
				// replayed verbatim on the next tool-call turn; re-uploading transformed text
				// under the original signature is rejected, so keep the original for storage
				// while the user still sees the transformed version live. finishReasoning did
				// that choice above.
				// Close the text stream: a sink may re-render the streamed raw text as
				// styled markdown now that it is complete. Reasoning rides along so the sink
				// has the full chain if it wants it.
				if text.Len() > 0 || display != "" {
					a.sink.Emit(event.Event{Kind: event.Message, Text: text.String(), Reasoning: display})
				}
				return text.String(), stored, signature, calls, usage, false, false, nil
			}
			chunk = c
		}
		switch chunk.Type {
		case provider.ChunkReasoning:
			diag.LogHex("agent-reason", chunk.Text)
			reasoning.WriteString(chunk.Text)
			if chunk.Signature != "" {
				signature = chunk.Signature
			}
			if chunk.Text != "" && !transformReasoning {
				a.sink.Emit(event.Event{Kind: event.Reasoning, Text: chunk.Text})
			}
		case provider.ChunkText:
			diag.LogHex("agent-chunk", chunk.Text)
			text.WriteString(chunk.Text)
			a.sink.Emit(event.Event{Kind: event.Text, Text: chunk.Text})
		case provider.ChunkToolCallStart:
			partialToolStarted = true
			// Surface the tool card as soon as the call begins — before its
			// (possibly large) arguments finish streaming — so the user sees it
			// working instead of a stall. executeBatch emits the full dispatch
			// (with args) once the call completes; the frontend merges by ID.
			if tc := chunk.ToolCall; tc != nil {
				a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{
					ID: tc.ID, Name: tc.Name, ReadOnly: a.toolReadOnly(tc.Name), Partial: true,
				}})
			}
		case provider.ChunkToolCall:
			partialToolStarted = true
			calls = append(calls, *chunk.ToolCall)
		case provider.ChunkUsage:
			usage = chunk.Usage
			a.lastUsage.Store(chunk.Usage)
			a.sessCacheHit.Add(int64(chunk.Usage.CacheHitTokens))
			a.sessCacheMiss.Add(int64(chunk.Usage.CacheMissTokens))
			a.sessPromptTokens.Add(int64(chunk.Usage.PromptTokens))
			a.sessTotalTokens.Add(int64(chunk.Usage.TotalTokens))
			if a.pricing != nil {
				a.addSessionCost(a.pricing.Cost(chunk.Usage), a.pricing.Symbol())
			}
		case provider.ChunkError:
			if provider.IsStreamInterrupted(chunk.Err) {
				stored, _ := finishReasoning()
				return text.String(), stored, signature, calls, usage, true, partialToolStarted, chunk.Err
			}
			return "", "", "", nil, nil, false, false, chunk.Err
		}
	}
}

func (a *Agent) capturePrefixShape(schemas []provider.ToolSchema) PrefixShape {
	return CaptureShape(a.systemPrompt(), schemas, a.session.RewriteVersion())
}

func (a *Agent) systemPrompt() string {
	var b strings.Builder
	for _, m := range a.session.Messages {
		if m.Role != provider.RoleSystem {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

func (a *Agent) getSchemasForContext(ctx context.Context) []provider.ToolSchema {
	if a.tools == nil {
		return nil
	}
	schemas := a.tools.Schemas()
	depth := NestingDepthFrom(ctx)
	if depth == 0 {
		diagnosticExpose := a.diagnosticRequested.Load()
		filtered := make([]provider.ToolSchema, 0, len(schemas))
		for _, s := range schemas {
			if a.toolsDynamic != nil && a.toolsDynamic[s.Name] {
				if !diagnosticExpose {
					continue
				}
			}
			if t, ok := a.tools.Get(s.Name); ok {
				if sub, ok := t.(tool.OnlyForSubAgent); ok && sub.OnlyForSubAgent() {
					continue
				}
			}
			if allow := a.mainAgentAllowed; allow != nil && !allow[s.Name] {
				continue
			}
			filtered = append(filtered, s)
		}
		return filtered
	}
	return schemas
}

// executeBatch dispatches one model turn's tool calls. A ToolDispatch event is
// emitted for every call up front, in call order, so a frontend can show the
// timeline chronologically. Contiguous known ReadOnly calls fan out across
// goroutines; unknown and writer calls run as single-call serial segments so
// write/read ordering stays provider-ordered. ToolResult events are emitted
// after the batch in call order, so emission stays serial even when execution
// parallelised.
func (a *Agent) executeBatch(ctx context.Context, calls []provider.ToolCall) []string {
	for _, c := range calls {
		t, ok := a.tools.Get(c.Name)
		ev := event.Tool{ID: c.ID, Name: c.Name, Args: c.Arguments, ReadOnly: ok && t.ReadOnly()}
		if ok {
			if ch, ok := tool.PreviewChange(t, json.RawMessage(c.Arguments)); ok {
				ev.FileDiff = event.FileDiff{Diff: ch.Diff, Added: ch.Added, Removed: ch.Removed}
			}
			if pr, ok := t.(interface {
				ResolveProfile(json.RawMessage) *event.Profile
			}); ok {
				ev.Profile = pr.ResolveProfile(json.RawMessage(c.Arguments))
			}
		}
		a.sink.Emit(event.Event{Kind: event.ToolDispatch, Tool: ev})
	}

	results := make([]string, len(calls))
	outcomes := make([]toolOutcome, len(calls))
	durations := make([]int64, len(calls))
	run := func(i int) {
		start := time.Now()
		outcomes[i] = a.executeOne(ctx, calls[i])
		durations[i] = time.Since(start).Milliseconds()
		results[i] = outcomes[i].output
	}

	for _, batch := range partitionToolCalls(a.tools, calls) {
		if batch.parallel && batch.end-batch.start > 1 {
			runParallel(ctx, batch.start, batch.end, run, func(idx int, msg string) {
				outcomes[idx] = toolOutcome{errMsg: msg, output: msg}
				results[idx] = msg
			})
			continue
		}
		for i := batch.start; i < batch.end; i++ {
			run(i)
		}
	}

	for i, c := range calls {
		o := outcomes[i]
		t, ok := a.tools.Get(c.Name)
		a.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{
			ID:         c.ID,
			Name:       c.Name,
			Args:       c.Arguments,
			Output:     o.output,
			Err:        o.errMsg,
			ReadOnly:   ok && t.ReadOnly(),
			Truncated:  o.truncated,
			DurationMs: durations[i],
		}})
		if o.truncated && o.truncMsg != "" {
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: o.truncMsg})
		}
	}
	a.applyStormBreaker(calls, outcomes, results)
	return results
}

type toolCallBatch struct {
	start    int
	end      int
	parallel bool
}

// partitionToolCalls keeps provider order while letting contiguous known
// read-only tools run together. Unknown and writer tools are single-call serial
// batches so they cannot reorder around reads or produce surprising errors.
func partitionToolCalls(r *tool.Registry, calls []provider.ToolCall) []toolCallBatch {
	var batches []toolCallBatch
	for i := 0; i < len(calls); {
		if parallelisable(r, calls[i].Name) {
			start := i
			i++
			for i < len(calls) && parallelisable(r, calls[i].Name) {
				i++
			}
			batches = append(batches, toolCallBatch{start: start, end: i, parallel: true})
			continue
		}
		batches = append(batches, toolCallBatch{start: i, end: i + 1})
		i++
	}
	return batches
}

func parallelisable(r *tool.Registry, name string) bool {
	t, ok := r.Get(name)
	if !ok {
		return false
	}
	if t.ReadOnly() {
		return true
	}
	if c, ok := t.(tool.Concurrenter); ok {
		return c.Concurrent()
	}
	return false
}

func runParallel(ctx context.Context, start, end int, run func(int), onPanic func(int, string)) {
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i := start; i < end; i++ {
		i := i
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Wait for already-launched goroutines to finish before returning
			// so we don't leak them or leave the semaphore in an inconsistent state.
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("tool goroutine panicked",
						"index", i,
						"panic", r,
					)
					if onPanic != nil {
						onPanic(i, fmt.Sprintf("internal error: tool panicked: %v", r))
					}
				}
			}()
			run(i)
		}()
	}
	wg.Wait()
}

// stormBreakThreshold is how many times in a row the same tool may fail the same
// way before the loop stops echoing the raw error back and instead returns a
// directive to change approach. Two natural self-corrections are healthy; the
// third identical failure is a death-spiral — the dominant case being a tool call
// whose arguments are truncated at the output-token ceiling, which the model then
// re-emits (re-worded but still over-long), truncating the same way again.
const stormBreakThreshold = 3

// repeatSuccessBreakThreshold is how many identical write-like successes the
// agent allows before refusing another copy in the same user turn. Two gives the
// model room for a natural self-correction; the third repeat is usually a
// no-op/write loop and should be redirected to a different tool or final answer.
const repeatSuccessBreakThreshold = 2

// applyStormBreaker detects a run of identically-failing turns and, past the
// threshold, rewrites the model-facing result (results[0]) into a directive to
// change approach. It keys on each call's (tool, error) — not its args — because a
// stuck model reworks the arguments cosmetically while failing identically (see
// the stormSig field doc). A turn is a fixation candidate only when every one of
// its calls errored and none was merely blocked by permissions (those
// carry a clear, distinct message the model can already act on). Any success, any
// block, or a different batch shape is varied work, so it resets the counter. This
// covers both the single-call spiral and a repeated multi-call batch. The hard
// maxSteps guard remains the ultimate backstop; this just keeps the loop from
// burning that whole budget bouncing off the same failure.
func (a *Agent) applyStormBreaker(calls []provider.ToolCall, outcomes []toolOutcome, results []string) {
	sig, ok := batchStormSignature(calls, outcomes)
	if !ok {
		a.stormSig, a.stormCount = "", 0
		return
	}
	if sig != a.stormSig {
		a.stormSig, a.stormCount = sig, 1
		return
	}
	a.stormCount++
	if a.stormCount < stormBreakThreshold {
		return
	}
	subject := fmt.Sprintf("%q", calls[0].Name)
	short := calls[0].Name
	if len(calls) > 1 {
		subject = fmt.Sprintf("this batch of %d tool calls", len(calls))
		short = fmt.Sprintf("a batch of %d calls", len(calls))
	}
	results[0] = outcomes[0].output + fmt.Sprintf(
		"\n\n[loop guard] %s has now failed %d times in a row with the same error. Re-sending it — even with the wording changed — will not help: the calls keep failing the same way. Change approach: if an argument is being truncated, write less in one call and split the work into several smaller calls; otherwise fix the arguments, use a different tool, or explain the blocker in your final answer.",
		subject, a.stormCount)
	a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: fmt.Sprintf(
		"loop guard: %s failed %d× the same way — nudging the model to change approach",
		short, a.stormCount)})
}

// batchStormSignature returns a per-turn fixation signature — each call's
// (name, error) in order — and ok=true only when every call errored and none was
// merely blocked. ok=false (any success or block) means the turn made varied
// progress, so the caller resets the counter. Keying on the error rather than the
// args is deliberate: a stuck model reworks the arguments while failing the same
// way, so identical-args matching would miss the loop.
func batchStormSignature(calls []provider.ToolCall, outcomes []toolOutcome) (string, bool) {
	if len(calls) == 0 {
		return "", false
	}
	var sb strings.Builder
	for i := range calls {
		if outcomes[i].errMsg == "" || outcomes[i].blocked {
			return "", false
		}
		sb.WriteString(calls[i].Name)
		sb.WriteByte(0)
		sb.WriteString(outcomes[i].errMsg)
		sb.WriteByte(0)
	}
	return sb.String(), true
}

// toolOutcome is one tool call's result, split into the model-facing output and
// the display-facing notice bits. errMsg is the short failure reason (empty on
// success) — a refused call, an unknown tool, or an execution error — so a sink
// renders the result as failed ("⊘ name <errMsg>" / a red card) instead of OK;
// blocked narrows that to a refusal (permission policy). truncMsg is set
// (without the "· " prefix) when the output was head+tailed.
type toolOutcome struct {
	output    string
	blocked   bool
	errMsg    string
	truncated bool
	truncMsg  string
}

// executeOne runs a single tool call. It is pure with respect to the event sink
// — the caller emits ToolDispatch/ToolResult — so it is safe to invoke from
// parallel goroutines.

func (a *Agent) executeOne(ctx context.Context, call provider.ToolCall) toolOutcome {
	t, ok := a.tools.Get(call.Name)
	if !ok {
		// Tool not found — auto-correct via fuzzy matching instead of just
		// reporting an error. Prevents the model from looping on hallucinated
		// tool names (e.g. context vs mcp_context).
		if suggestion, found := a.tools.Suggest(call.Name); found {
			slog.Info("tool auto-correct", "from", call.Name, "to", suggestion)
			t, ok = a.tools.Get(suggestion)
		}
	}
	if !ok {
		errMsg := fmt.Sprintf(
			"error: unknown tool %q. Available tools: %v. "+
				"Pick the correct tool and retry.",
			call.Name, a.tools.Names())
		return toolOutcome{
			output: errMsg,
			errMsg: fmt.Sprintf("unknown tool %q", call.Name),
		}
	}
	if sub, ok := t.(tool.OnlyForSubAgent); ok && sub.OnlyForSubAgent() && NestingDepthFrom(ctx) == 0 {
		return toolOutcome{
			output: fmt.Sprintf("error: tool %q is only available to sub-agents", call.Name),
			errMsg: fmt.Sprintf("tool %q sub-agent only", call.Name),
		}
	}
	if a.toolsDynamic != nil && a.toolsDynamic[call.Name] && NestingDepthFrom(ctx) == 0 && !a.diagnosticRequested.Load() {
		return toolOutcome{
			output:  fmt.Sprintf("permission denied: tool %q is not currently available", call.Name),
			blocked: true,
			errMsg:  fmt.Sprintf("permission denied: tool %q not available", call.Name),
		}
	}
	// Main-agent whitelist: when nesting depth is 0 (root/main agent),
	// only allow explicitly permitted tools (if the option is set).
	if allow := a.mainAgentAllowed; allow != nil && NestingDepthFrom(ctx) == 0 && !allow[call.Name] && !(a.toolsDynamic != nil && a.toolsDynamic[call.Name] && a.diagnosticRequested.Load()) {
		return toolOutcome{
			output:  fmt.Sprintf("permission denied: tool %q not allowed for main agent", call.Name),
			blocked: true,
			errMsg:  fmt.Sprintf("permission denied: tool %q not allowed for main agent", call.Name),
		}
	}
	// Main-agent readonly calls limit: when nesting depth is 0 (root/main agent),
	// enforce maximum limit of readonly tool calls.
	if t.ReadOnly() && NestingDepthFrom(ctx) == 0 && a.maxMainAgentReadonlyCalls > 0 {
		count := a.readonlyCallsCount.Add(1)
		if count > int64(a.maxMainAgentReadonlyCalls) {
			return toolOutcome{
				output:  fmt.Sprintf("permission denied: main agent readonly call limit reached (%d)", a.maxMainAgentReadonlyCalls),
				blocked: true,
				errMsg:  fmt.Sprintf("main agent readonly call limit reached (%d)", a.maxMainAgentReadonlyCalls),
			}
		}
	}
	if out, blocked := a.repeatedSuccessBlock(call, t); blocked {
		return toolOutcome{
			output:  out,
			blocked: true,
			errMsg:  "blocked by loop guard",
		}
	}
	if a.gate != nil {
		allow, reason, err := a.gate.Check(ctx, call.Name, json.RawMessage(call.Arguments), t.ReadOnly())
		if err != nil {
			return toolOutcome{
				output:  fmt.Sprintf("blocked: %s (%v)", reason, err),
				blocked: true,
				errMsg:  fmt.Sprintf("blocked: %v", err),
			}
		}
		if !allow {
			return toolOutcome{
				output:  "blocked: " + reason,
				blocked: true,
				errMsg:  "blocked by permission policy",
			}
		}
	}
	// PreToolUse hooks run after permission is granted but before the call: a
	// gating hook (exit 2) refuses it, surfaced to the model like a gate denial.
	// A hook may also emit replacement args on stdout; if so the tool executes
	// with those instead of the model's original Arguments.
	effectiveArgs := json.RawMessage(call.Arguments)
	if a.hooks != nil {
		if block, msg, modified := a.hooks.PreToolUse(ctx, call.Name, effectiveArgs); block {
			if msg == "" {
				msg = "blocked by a PreToolUse hook"
			}
			return toolOutcome{
				output:  "blocked: " + msg,
				blocked: true,
				errMsg:  "blocked by PreToolUse hook",
			}
		} else if modified != nil {
			effectiveArgs = modified
		}
	}
	// Checkpoint the file this writer is about to change, so the turn can be
	// rewound. Fires after all gating (the edit is cleared to run) and only for
	// tools that can describe their change; a Preview error means the edit will
	// likely fail anyway, so we skip rather than snapshot a stale state.
	if a.onPreEdit != nil && !t.ReadOnly() {
		if pv, ok := t.(tool.Previewer); ok {
			if change, perr := pv.Preview(effectiveArgs); perr == nil {
				a.onPreEdit(change)
			}
		}
	}
	cctx := withCallContext(ctx, call.ID, a.sink, a.asker)
	cctx = WithSession(cctx, a.session)
	cctx = WithAgent(cctx, a)
	if len(a.projectChecks) > 0 {
		cctx = instruction.WithChecks(cctx, a.projectChecks)
	}
	if a.jobs != nil {
		cctx = jobs.WithManager(cctx, a.jobs)
	}
	if a.multiAgent != nil {
		cctx = multiagent.WithControl(cctx, a.multiAgent)
		path := a.agentPath
		if path == "" {
			path = multiagent.RootPath
		}
		cctx = multiagent.WithAgentPath(cctx, path)
	}
	if a.ctrl != nil {
		cctx = withCtrl(cctx, a.ctrl)
		if p, ok := a.ctrl.(OnCompleteProvider); ok {
			cctx = WithOnCompleteProvider(cctx, p)
		}
	}
	if a.memQueue != nil {
		cctx = memory.WithQueue(cctx, a.memQueue)
	}
	if a.ctxStore != nil {
		cctx = ctxmode.WithStore(cctx, a.ctxStore)
	}
	if p, ok := a.asker.(OnCompleteProvider); ok {
		cctx = WithOnCompleteProvider(cctx, p)
	}
	callID := call.ID
	cctx = tool.WithProgress(cctx, func(chunk string) {
		a.sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: callID, Output: chunk}})
	})
	result, err := t.Execute(cctx, effectiveArgs)
	if err == nil {
		filePath := TryExtractPath(effectiveArgs)
		if filePath != "" {
			if t.ReadOnly() {
				globalFileStateRegistry.RecordRead(a.session, filePath)
			} else {
				globalFileStateRegistry.RecordWrite(a.session, filePath)
			}
		}
	}
	if a.ctxStore != nil {
		ctxmode.RecordTool(a.ctxStore.Journal(), call.Name, effectiveArgs, result, err)
	}

	// PostToolUse hooks observe the result (they can't block); fired whether the
	// call succeeded or errored, since the tool did run. We pass the original
	// args here (not effectiveArgs) so the hook sees what the model intended, not
	// what a previous hook rewrote it to.
	if a.hooks != nil {
		a.hooks.PostToolUse(ctx, call.Name, json.RawMessage(call.Arguments), result)
	}
	// PostToolRewrite: optional hook-level result transformation.
	// Panics are recovered; on panic the original result is kept.
	if a.hooks != nil {
		if rewriter, ok := a.hooks.(PostToolRewriter); ok {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Warn("PostToolRewriter panicked, using original result",
							"tool", call.Name,
							"panic", r,
						)
					}
				}()
				result = rewriter.PostToolRewrite(ctx, call.Name, json.RawMessage(call.Arguments), result)
			}()
		}
	}
	if err != nil {
		detail := result
		// Malformed-args failures are a transient model JSON glitch (e.g. options
		// written as ["a":"b"] → "invalid character ':' after array element"). The
		// args can't be safely re-parsed, but echoing the tool's schema makes the
		// retry land valid instead of repeating the same broken shape.
		if !json.Valid([]byte(call.Arguments)) {
			detail = strings.TrimRight(detail, "\n") + "\nThe arguments were not valid JSON. Re-emit them exactly per this schema:\n" + string(t.Schema())
		}
		body, truncMsg := compactToolOutput(a.ctxStore, call.Name, json.RawMessage(call.Arguments), fmt.Sprintf("error: %v\n%s", err, detail))
		return toolOutcome{output: body, errMsg: firstLine(err.Error()), truncated: truncMsg != "" || strings.Contains(body, "[truncated "), truncMsg: truncMsg}
	}
	a.recordRepeatSuccess(call, t)
	body, truncMsg := compactToolOutput(a.ctxStore, call.Name, json.RawMessage(call.Arguments), result)
	// PostCallGuidance: if the tool teaches a post-call workflow, append it
	// to the result so the model is explicitly reminded what to do next.
	if pg, ok := t.(tool.PostCallGuidance); ok {
		prefix := "⚠ **Post-call requirements**"
		if gp, ok := t.(tool.GuidancePrefixer); ok {
			if p := strings.TrimSpace(gp.GuidancePrefix()); p != "" {
				prefix = p
			}
		}
		var guidance string
		if pgr, ok := t.(tool.PostCallGuidanceWithResult); ok {
			guidance = strings.TrimSpace(pgr.PostCallGuidanceAfter(json.RawMessage(call.Arguments), result))
		} else {
			guidance = strings.TrimSpace(pg.PostCallGuidance(json.RawMessage(call.Arguments)))
		}
		if guidance != "" {
			body += "\n\n---\n" + prefix + "\n" + guidance
		}
	}
	return toolOutcome{output: body, truncated: truncMsg != "" || strings.Contains(body, "[truncated "), truncMsg: truncMsg}
}

func (a *Agent) repeatedSuccessBlock(call provider.ToolCall, t tool.Tool) (string, bool) {
	sig, ok := repeatSuccessSignature(call, t)
	if !ok {
		return "", false
	}
	a.repeatSuccessMu.Lock()
	count := a.repeatSuccessCounts[sig]
	a.repeatSuccessMu.Unlock()
	if count < repeatSuccessBreakThreshold {
		return "", false
	}
	return fmt.Sprintf(
		"blocked: [loop guard] %q has already succeeded %d times with the same write-like arguments in this user turn. Re-running it is unlikely to help and may burn tokens or repeat file writes. Change approach: use a file editing tool for file changes, verify with a read/test command, or explain the blocker in your final answer.",
		call.Name, count), true
}

func (a *Agent) recordRepeatSuccess(call provider.ToolCall, t tool.Tool) {
	sig, ok := repeatSuccessSignature(call, t)
	if !ok {
		return
	}
	a.repeatSuccessMu.Lock()
	if a.repeatSuccessCounts == nil {
		a.repeatSuccessCounts = make(map[string]int)
	}
	a.repeatSuccessCounts[sig]++
	a.repeatSuccessMu.Unlock()
}

func repeatSuccessSignature(call provider.ToolCall, t tool.Tool) (string, bool) {
	if t.ReadOnly() {
		return "", false
	}
	switch call.Name {
	case "write_file", "edit_file", "multi_edit", "move_file", "notebook_edit":
		return call.Name + "\x00" + canonicalToolArgs(call.Arguments), true
	case "bash":
		var p struct {
			Command         string `json:"command"`
			RunInBackground bool   `json:"run_in_background"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &p); err != nil {
			return "", false
		}
		if p.RunInBackground || !isShellFileWriteCommand(p.Command) {
			return "", false
		}
		return "bash\x00" + normalizeShellCommand(p.Command), true
	default:
		return "", false
	}
}

func canonicalToolArgs(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return strings.TrimSpace(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		return string(b)
	}
	return compact.String()
}

func normalizeShellCommand(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

func isShellFileWriteCommand(command string) bool {
	lower := strings.ToLower(command)
	switch {
	case shellPythonOpenWrites(lower):
		return true
	case strings.Contains(lower, "set-content") || strings.Contains(lower, "add-content") || strings.Contains(lower, "out-file"):
		return true
	case strings.Contains(lower, "sed -i") || strings.Contains(lower, "perl -pi"):
		return true
	case hasShellWriteRedirect(command):
		return true
	default:
		return false
	}
}

func shellPythonOpenWrites(lower string) bool {
	if !strings.Contains(lower, "open(") {
		return false
	}
	if strings.Contains(lower, ".write(") {
		return true
	}
	for _, marker := range []string{", 'w", `, "w`, ", 'a", `, "a`, ", 'x", `, "x`, "mode='w", `mode="w`, "mode='a", `mode="a`, "mode='x", `mode="x`} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasShellWriteRedirect(command string) bool {
	var quote rune
	var prev rune
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			prev = r
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			prev = r
			continue
		}
		if r == '>' {
			if prev == '2' {
				prev = r
				continue
			}
			return true
		}
		prev = r
	}
	return false
}

// toolReadOnly reports a tool's ReadOnly classification by name (false for an
// unknown tool), for stamping early ToolDispatch events.
func (a *Agent) toolReadOnly(name string) bool {
	t, ok := a.tools.Get(name)
	return ok && t.ReadOnly()
}

// firstLine returns s up to its first newline — a one-line failure summary for
// the display Err, while the full error stays in the model-facing output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// truncateToolOutput head+tails s when it exceeds maxToolOutputBytes, slicing
// on rune boundaries so we never split a multibyte glyph. Returns the possibly
// trimmed body (which includes an internal "[truncated ...]" marker).
// The one-line user-facing notice is suppressed (not emitted as Notice event
// to avoid chat spam); truncation events are always logged via slog for debugging.
func truncateToolOutput(s string) (string, string) {
	if len(s) <= maxToolOutputBytes {
		return s, ""
	}
	keep := maxToolOutputBytes / 2
	head := snapToRuneBoundary(s, 0, keep)
	tail := snapToRuneBoundary(s, len(s)-keep, len(s))
	omitted := len(s) - len(head) - len(tail)
	slog.Info("tool output truncated", "omitted", omitted, "total", len(s))
	body := head + fmt.Sprintf("\n\n…[truncated %d of %d bytes — rerun with narrower args to see the middle]…\n\n", omitted, len(s)) + tail
	return body, ""
}

// snapToRuneBoundary returns s[lo:hi] with the bounds nudged outward until
// both land on rune-start positions.
func snapToRuneBoundary(s string, lo, hi int) string {
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	return s[lo:hi]
}

// finishReasonMessage maps an abnormal finish_reason to a one-line warning,
// returning ok=false for the normal terminations ("stop", "tool_calls") and a
// nil usage. The sink renders the message; the "! " prefix is presentation.
func finishReasonMessage(u *provider.Usage) (string, bool) {
	if u == nil {
		return "", false
	}
	switch u.FinishReason {
	case "length":
		return "response truncated: hit max output tokens", true
	case "content_filter":
		return "response blocked by content filter", true
	case "repetition_truncation":
		return "response truncated: model repetition detected", true
	default:
		return "", false
	}
}

// parseMultimodalInput scans the input text for [REASONIX_IMAGE:data:...]
// markers, extracts each as a ContentPart with Type PartTypeImage, and returns
// the cleaned text (with markers removed) together with the extracted parts.
// If no markers are found, returns the original text and nil parts.
func parseMultimodalInput(input string) (string, []provider.ContentPart) {
	const prefix = "[REASONIX_IMAGE:"
	const suffix = "]"

	var parts []provider.ContentPart
	cleaned := input

	for {
		start := strings.Index(cleaned, prefix)
		if start < 0 {
			break
		}
		end := strings.Index(cleaned[start+len(prefix):], suffix)
		if end < 0 {
			break
		}
		end = start + len(prefix) + end + len(suffix)

		// Extract the data URL between markers
		dataURL := cleaned[start+len(prefix) : end-len(suffix)]

		// Parse the data URL into MIME type and base64 data
		mime, data := parseDataURL(dataURL)
		if mime != "" && data != "" {
			parts = append(parts, provider.ContentPart{
				Type: provider.PartTypeImage,
				Image: &provider.ImagePart{
					Data: data,
					Mime: mime,
				},
			})
		}

		// Remove the marker from the text (including the newline around it)
		cleaned = cleaned[:start] + cleaned[end:]
	}

	// Clean up leftover blank lines from marker removal
	cleaned = strings.TrimSpace(cleaned)

	if len(parts) == 0 {
		return input, nil
	}
	return cleaned, parts
}

// parseDataURL extracts the MIME type and base64 data from a data URL.
// Input: "data:image/jpeg;base64,/9j/4AAQ..."
// Output: "image/jpeg", "/9j/4AAQ..."
func parseDataURL(dataURL string) (mime, data string) {
	rest := strings.TrimPrefix(dataURL, "data:")
	if rest == dataURL {
		return "", ""
	}
	idx := strings.Index(rest, ";base64,")
	if idx < 0 {
		return "", ""
	}
	mime = rest[:idx]
	data = rest[idx+len(";base64,"):]
	return
}

// sessionHasUnreadTaskResult is true when the conversation tail is a runtime
// background delivery that the main agent has not yet answered.
// sessionHasUnreadTaskResult reports pending multiagent mailbox work for the parent model.
func (a *Agent) sessionHasUnreadTaskResult() bool {
	if a.multiAgent != nil && a.multiAgent.Mailbox().HasPendingFor(a.agentPath) {
		return true
	}
	if a.session == nil {
		return false
	}
	msgs := a.session.Snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case provider.RoleUser:
			return strings.Contains(m.Content, "[multi_agent_mailbox]")
		case provider.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0 {
				return false
			}
		}
	}
	return false
}


