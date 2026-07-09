// Package control is the transport-agnostic session driver. A Controller owns
// the agent run loop and session lifecycle, takes commands (Send/Cancel/Approve/
// Compact/NewSession/…), and emits everything that happens —
// reasoning, tool calls, approvals, turn completion — as a typed event stream to
// a single event.Sink.
//
// The point is one orchestration layer behind every frontend: a terminal TUI, a
// desktop webview, or an HTTP/SSE server each drive the Controller identically
// (issue commands, render events) and none of them re-implement turn lifecycle,
// cancellation, or approval. The Controller depends on no frontend.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/billing"
	"reasonix/internal/checkpoint"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/ctxmode"
	"reasonix/internal/diff"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/i18n"
	"reasonix/internal/jobs"
	"reasonix/internal/memory"
	"reasonix/internal/nilutil"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/shell"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
)

// Controller drives one chat session. Construct with New; drive with the command
// methods; observe through the Sink passed in Options.
type Controller struct {
	runner       agent.Runner
	executor     *agent.Agent
	subAgentGate *permission.Gate
	sink         event.Sink
	policy       permission.Policy

	label         string
	systemPrompt  string
	sessionDir    string
	host          *plugin.Host
	commands      []command.Command
	skills        []skill.Skill
	allSkills     []skill.Skill
	skillStore    *skill.Store
	allSkillStore *skill.Store
	hooks         *hook.Runner // session hook runner; nil-safe (no hooks configured)
	mem           *memory.Set
	cleanup       func()
	startedOnce   bool              // guards the one-shot SessionStart hook on first turn
	onRemember    func(rule string) // set via Options; invoked when user picks "always allow"

	// balanceURL/balanceKey target the active provider's optional wallet-balance
	// endpoint (empty when the provider declares none). Captured at build so a
	// model/key switch — which rebuilds the controller — refreshes them.
	balanceURL    string
	balanceKey    string
	balanceClient *http.Client

	// cfg holds the boot-time config with live-fetched model lists, so
	// modelListText (and other config readers) use the API-provided model list
	// rather than reloading from disk and losing fetchedModels.
	cfg *config.Config

	// jobs is the session-scoped background-job manager. The agent's background
	// tools spawn into it; Compose drains its completion notes into the next turn;
	// Close cancels its still-running jobs.
	jobs *jobs.Manager

	// reg is the live tool registry the executor reads each turn; pluginCtx is the
	// session-scoped context a hot-added stdio server binds its subprocess to.
	// Together they let AddMCPServer connect a server mid-session and have its tools
	// available on the next turn (see AddMCPServer / RemoveMCPServer).
	reg       *tool.Registry
	pluginCtx context.Context

	// know to stop when the controller shuts down.
	closeCtx    context.Context
	closeCancel context.CancelFunc

	// Checkpoints (snapshot-based rewind). cp is the per-session store rebound when
	// the session path changes; cpRoot is the workspace root used to guard restore
	// writes. cpTurn is the monotonic turn counter (decoupled from the store so it
	// never collides after a restructure); cpBound[turn] records len(Session.Messages)
	// at that turn's start — the truncation boundary for a conversation rewind/fork.
	// Boundaries are persisted in each checkpoint and rebuilt from the store on
	// resume (so a reopened session can still rewind conversation / fork), but
	// dropped after a summarize restructures the log so those operations report
	// "unavailable" rather than mis-truncating; code rewind (file-based) is unaffected.
	cp      *checkpoint.Store
	cpRoot  string
	cpTurn  int
	cpBound map[int]int

	// promptMu serialises approval prompts so at most one is outstanding at a
	// time (parallel read-only tool calls don't normally gate, writers run
	// serially — but this keeps the contract explicit). Held across the blocking
	// wait, so it must never be taken by the Approve command path.
	promptMu sync.Mutex

	// mu guards the run state and approval bookkeeping; every critical section
	// under it is short and non-blocking.
	mu                sync.Mutex
	cancel            context.CancelFunc
	running           bool
	sessionPath       string
	approvals         map[string]chan approvalReply
	asks              map[string]chan []event.AskAnswer
	granted           map[string]bool
	taskResults       map[string]jobMeta // jobID → creation-time tool call metadata
	taskResultsMu     sync.Mutex         // guards taskResults
	pendingToolResult atomic.Bool        // sub-agent completion auto-reentry flag
	nextID            int
	// turn counts model turns this session, passed to hooks in their payload.
	turn int

	// bypass is "YOLO" mode: while set, every approval prompt is auto-allowed for
	// the rest of the session (writers and bash run without asking). It is a
	// deliberate, session-scoped opt-in (the --dangerously-skip-permissions flag or
	// a runtime toggle), never persisted. Deny rules are unaffected — they're
	// resolved before the approver, so a denied tool is still blocked in YOLO mode.
	bypass bool

	// pendingMemory holds memory notes added mid-session (via "#" quick-add or a
	// memory edit) that haven't yet been folded into a turn. Compose drains it
	// onto the next outgoing turn — never into the cache-stable system prefix — so
	// a fresh memory takes effect this session without busting the prompt cache;
	// it joins the prefix naturally on the next session.
	pendingMemory []string

	autoReentryDepth int

	pendingReentryQueue []string

	// deferredDeliverIDs are auto-deliver task jobs that finished while a main
	// turn was still running. Session write is deferred until the turn ends so
	// results land at a clean conversation boundary (not mid-stream).
	deferredDeliverIDs []string

	// reentryCapPending is set when auto-reentry hit the depth cap; after the
	// current turn ends we retry wake instead of waiting forever for user input.
	reentryCapPending bool

	wg        sync.WaitGroup
	closeOnce sync.Once
}

type approvalReply struct {
	allow   bool
	session bool
	persist bool // true = write "always allow" rule to config
}

// jobMeta records the tool-call metadata for a background task job when it is
// created, so the Controller can correlate a completed job's result with the
// original tool call that spawned it.
type jobMeta struct {
	ToolCallID string
	StartStep  int
}

// Options carries the already-built pieces setup assembles. Lifecycle metadata
// lets the controller mint and rotate session files; Host/Commands are surfaced
// to frontends that resolve MCP prompts and slash commands.
type Options struct {
	Runner        agent.Runner
	Executor      *agent.Agent
	Sink          event.Sink
	Policy        permission.Policy
	SubAgentGate  *permission.Gate // 子代理门，EnableInteractiveApproval 时注入 Approver
	Label         string
	SystemPrompt  string
	SessionDir    string
	SessionPath   string
	Host          *plugin.Host
	Commands      []command.Command
	Skills        []skill.Skill
	AllSkills     []skill.Skill
	SkillStore    *skill.Store
	AllSkillStore *skill.Store
	Hooks         *hook.Runner
	Memory        *memory.Set
	Cleanup       func()
	// BalanceURL/BalanceKey wire the active provider's optional wallet-balance
	// endpoint and bearer key; empty when the provider declares no balance_url.
	BalanceURL    string
	BalanceKey    string
	BalanceClient *http.Client
	// Jobs is the session-scoped background-job manager (nil disables background jobs).
	Jobs *jobs.Manager
	// Registry is the executor's live tool set, and PluginCtx the session-scoped
	// context; both are needed for hot-adding MCP servers via AddMCPServer.
	Registry  *tool.Registry
	PluginCtx context.Context
	// WorkspaceRoot is the project root checkpoint restores are bound to ("" =
	// no confinement). Frontends pass the cwd they launched the session in.
	WorkspaceRoot string
	// OnRemember, when set, is invoked with a new allow rule the user chose to
	// persist to disk (e.g. "bash(go build*)"). The callback is wired into the
	// permission Gate on EnableInteractiveApproval.
	OnRemember func(rule string)
	// Config is the boot-time configuration with live-fetched model lists.
	// When set, modelListText uses this instead of reloading from disk.
	Config *config.Config
}

// New builds a Controller. A nil Sink is replaced with event.Discard.
func New(opts Options) *Controller {
	sink := opts.Sink
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	pluginCtx := opts.PluginCtx
	if pluginCtx == nil {
		pluginCtx = context.Background()
	}
	if ctxmode.Active() {
		if n, err := ctxmode.PruneOrphanCache(); err == nil && n > 0 {
			slog.Info("ctxmode cache prune", "removed", n)
		}
	}

	c := &Controller{
		runner:        opts.Runner,
		executor:      opts.Executor,
		sink:          sink,
		policy:        opts.Policy,
		subAgentGate:  opts.SubAgentGate,
		label:         opts.Label,
		systemPrompt:  opts.SystemPrompt,
		sessionDir:    opts.SessionDir,
		sessionPath:   opts.SessionPath,
		host:          opts.Host,
		commands:      opts.Commands,
		skills:        opts.Skills,
		allSkills:     opts.AllSkills,
		skillStore:    opts.SkillStore,
		allSkillStore: opts.AllSkillStore,
		hooks:         opts.Hooks,
		mem:           opts.Memory,
		cleanup:       opts.Cleanup,
		onRemember:    opts.OnRemember,
		balanceURL:    opts.BalanceURL,
		balanceKey:    opts.BalanceKey,
		balanceClient: opts.BalanceClient,
		jobs:          opts.Jobs,
		reg:           opts.Registry,
		pluginCtx:     pluginCtx,
		closeCtx:      context.Background(),
		closeCancel:   func() {}, // replaced by Close if ever needed; safe no-op default
		cpRoot:        opts.WorkspaceRoot,
		cfg:           opts.Config,
		approvals:     map[string]chan approvalReply{},
		asks:          map[string]chan []event.AskAnswer{},
		granted:       map[string]bool{},
		taskResults:   make(map[string]jobMeta),
	}
	// Checkpoints: bind a store to the session and route writer pre-edits into it.
	c.rebindCheckpoints(opts.SessionPath)
	if c.runner == nil && c.executor != nil {
		c.runner = c.executor
	}
	if c.executor != nil {
		c.executor.SetPreEditHook(func(ch diff.Change) {
			if c.cp != nil {
				c.cp.Snapshot(ch)
			}
		})
		c.executor.SetMemoryQueue(c)
		c.executor.SetControllerBridge(c)
	}
	if c.jobs != nil {
		// Single completion path for the whole session: only auto-deliver kinds
		// (task) write into the parent session and wake the model. Bash jobs only
		// emit their Notice from jobs.Manager and stay peekable.
		c.jobs.SetOnCompletion(c.handleJobCompletion)
	}
	return c
}

// handleJobCompletion is the sole jobs → parent bridge. Wired via SetOnCompletion.
func (c *Controller) handleJobCompletion(id string) {
	if c.jobs == nil {
		return
	}
	kind, ok := c.jobs.Kind(id)
	if !ok || !jobs.AutoDelivers(kind) {
		return
	}
	c.mu.Lock()
	busy := c.running
	if busy {
		// Defer session injection until the main turn ends (clean tail, no mid-stream splice).
		already := false
		for _, existing := range c.deferredDeliverIDs {
			if existing == id {
				already = true
				break
			}
		}
		if !already {
			c.deferredDeliverIDs = append(c.deferredDeliverIDs, id)
		}
		hasEmpty := false
		for _, q := range c.pendingReentryQueue {
			if q == "" {
				hasEmpty = true
				break
			}
		}
		if !hasEmpty {
			c.pendingReentryQueue = append(c.pendingReentryQueue, "")
		}
		c.mu.Unlock()
		c.pendingToolResult.Store(true)
		return
	}
	c.mu.Unlock()

	committed := false
	if c.executor != nil {
		committed = c.executor.CompleteBackgroundJob(id)
	}
	if !committed {
		slog.Warn("background job finished but result not committed to session", "job", id)
	}
	c.pendingToolResult.Store(true)
	c.autoReenter()
}

// flushDeferredDeliveries commits task results that finished during a busy turn.
// Call only when idle (after running=false).
func (c *Controller) flushDeferredDeliveries() {
	c.mu.Lock()
	ids := c.deferredDeliverIDs
	c.deferredDeliverIDs = nil
	c.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if c.executor == nil {
			break
		}
		if !c.executor.CompleteBackgroundJob(id) {
			slog.Warn("deferred background job delivery failed", "job", id)
		}
	}
	c.pendingToolResult.Store(true)
}

// ckptDir derives a session's checkpoint directory from its file path
// (…/<id>.jsonl → …/<id>.ckpt). Empty path → empty (in-memory checkpoints).
func ckptDir(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return strings.TrimSuffix(sessionPath, ".jsonl") + ".ckpt"
}

// rebindCheckpoints points the store at the (possibly new) session, loading any
// checkpoints already on disk, and resets the turn boundaries. Called on
// construction and whenever the session path changes (NewSession/Resume/SetSessionPath).
func (c *Controller) rebindCheckpoints(sessionPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cp = checkpoint.New(ckptDir(sessionPath), c.cpRoot)
	c.cpTurn = c.cp.NextTurn() // continue numbering past any checkpoints on disk
	c.cpBound = c.cp.Bounds()  // rebuilt from persisted checkpoints so a resumed
	if c.cpBound == nil {      // session can still rewind conversation / fork
		c.cpBound = map[int]int{}
	}
}

// beginCheckpoint opens a checkpoint for the turn about to run, recording the
// current message count as the conversation-rewind boundary. Called at the top of
// runTurn, before the user message is appended.
func (c *Controller) beginCheckpoint(input string) {
	if c.cp == nil || c.executor == nil {
		return
	}
	c.mu.Lock()
	turn := c.cpTurn
	c.cpTurn++
	msgIndex := len(c.executor.Session().Messages)
	c.cpBound[turn] = msgIndex
	c.mu.Unlock()
	c.cp.Begin(turn, input, msgIndex)
}

// --- commands (frontend → controller) ---

// runGuarded runs body on a background goroutine under a fresh cancellable
// context, guarding against concurrent turns and emitting a TurnDone event when
// it finishes (Err set on failure; nil also for a user Cancel). A no-op if a
// turn is already in flight.
func (c *Controller) runGuarded(input string, body func(ctx context.Context) error) {
	c.mu.Lock()
	if c.running {
		c.pendingReentryQueue = append(c.pendingReentryQueue, input)
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer cancel()
		err := body(ctx)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		if input == "" && c.autoReentryDepth > 0 {
			c.autoReentryDepth--
		}
		retryAfterCap := c.reentryCapPending
		c.reentryCapPending = false
		c.mu.Unlock()
		c.sink.Emit(event.Event{Kind: event.TurnDone, Err: explainError(err)})
		// 1) commit deliveries deferred while we were busy
		c.flushDeferredDeliveries()
		// 2) run any queued reentry (including the empty wake for those deliveries)
		c.drainReentryQueue()
		// 3) if depth cap blocked a wake earlier, try again now that depth decremented
		if retryAfterCap && c.pendingToolResult.Load() {
			c.autoReenter()
		}
	}()
}

func (c *Controller) drainReentryQueue() {
	for {
		c.mu.Lock()
		if c.running || len(c.pendingReentryQueue) == 0 {
			c.mu.Unlock()
			return
		}
		// Collapse consecutive empty auto-reentries into one wake.
		next := c.pendingReentryQueue[0]
		c.pendingReentryQueue = c.pendingReentryQueue[1:]
		if next == "" {
			for len(c.pendingReentryQueue) > 0 && c.pendingReentryQueue[0] == "" {
				c.pendingReentryQueue = c.pendingReentryQueue[1:]
			}
		}
		c.mu.Unlock()
		c.Send(next)
		return
	}
}

// MakeOnComplete is a no-op. Completion is handled exclusively by SetOnCompletion
// → handleJobCompletion. Kept so OnCompleteProvider still type-checks; callers
// should pass nil to jobs.Manager.Start.
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

// RegisterJobMeta implements agent.ControllerBridge by storing the spawn tool-call
// id for a background task (correlation / tool naming only). Delivery itself is
// handleJobCompletion; beforeRun registers meta before the job goroutine starts,
// so a late-completion race is rare. If it still happens, re-run the single path.
func (c *Controller) RegisterJobMeta(jobID string, toolCallID string) {
	c.taskResultsMu.Lock()
	c.taskResults[jobID] = jobMeta{ToolCallID: toolCallID}
	c.taskResultsMu.Unlock()
	if c.jobs != nil {
		if _, ok := c.jobs.CompletedResult(jobID); ok {
			c.handleJobCompletion(jobID)
		}
	}
}

// GetJobMeta retrieves the stored metadata for a job without removing it.
func (c *Controller) GetJobMeta(jobID string) (jobMeta, bool) {
	c.taskResultsMu.Lock()
	defer c.taskResultsMu.Unlock()
	meta, ok := c.taskResults[jobID]
	return meta, ok
}

// TakeJobMeta reads and deletes a job's metadata in one atomic step, preventing
// accumulation of metadata for already-completed jobs.
func (c *Controller) TakeJobMeta(jobID string) (toolCallID string, found bool) {
	c.taskResultsMu.Lock()
	defer c.taskResultsMu.Unlock()
	meta, ok := c.taskResults[jobID]
	if ok {
		delete(c.taskResults, jobID)
	}
	if !ok {
		return "", false
	}
	return meta.ToolCallID, true
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
func (c *Controller) Send(input string) {
	c.SendWithRaw(input, input)
}

// SendWithRaw starts a turn with separate model input and raw prompt text.
// The raw parameter is preserved for API compatibility.
func (c *Controller) SendWithRaw(input, raw string) {
	c.mu.Lock()
	if c.running && raw != "" {
		if c.cancel != nil {
			c.cancel()
		}
	}
	c.mu.Unlock()

	c.runGuarded(raw, func(ctx context.Context) error { return c.runTurnWithRaw(ctx, input, raw) })
}

// runTurn runs one model turn.
func (c *Controller) runTurn(ctx context.Context, input string) error {
	return c.runTurnWithRaw(ctx, input, input)
}

func (c *Controller) runTurnWithRaw(ctx context.Context, input, raw string) error {
	c.maybeSessionStart(ctx)
	input = c.Compose(input)
	startMessages := c.messageCount()
	defer c.snapshotActivityIfChanged(startMessages)
	// Open a checkpoint for this turn before the user message is appended, so the
	// recorded message boundary precedes it and pre-edit snapshots land here.
	c.beginCheckpoint(input)
	// UserPromptSubmit / Stop hooks bracket the whole turn: a gating
	// UserPromptSubmit aborts before any model call; Stop fires once when the
	// turn returns.
	if c.hooks.Enabled() {
		c.mu.Lock()
		c.turn++
		turn := c.turn
		c.mu.Unlock()
		if block, _ := c.hooks.PromptSubmit(ctx, input, turn); block {
			return nil // the hook's notify callback already surfaced the reason
		}
		defer func() { c.hooks.Stop(ctx, lastAssistantText(c.History()), turn) }()
	}
	wantsPeek := raw != "" && c.executor != nil && UserRequestsJobPeek(raw)
	if wantsPeek {
		c.executor.SetDiagnosticRequested(true)
	}

	// Block dynamic tools that the model should not call in this turn.
	blocked := map[string]string{}
	if !wantsPeek {
		blocked["peek-job"] = "peek-job is for background shell jobs (or owner diagnostics). The task tool returns its answer when the call finishes — do not poll it."
	}
	if c.jobs == nil || len(c.jobs.Running()) == 0 {
		blocked["steer-job"] = "steer-job needs a running background shell job. No jobs are running."
	}
	if c.subAgentGate != nil && len(blocked) > 0 {
		c.subAgentGate.SetBlockedTools(blocked)
	}
	if err := c.runner.Run(ctx, input); err != nil {
		return err
	}
	return nil

}
// lastAssistantText returns the content of the most recent assistant message with
// non-empty text — the model's final answer for the turn.
func lastAssistantText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// Submit is the one-call entry for a simple frontend: it takes raw user input
// and does everything — slash-command dispatch, @-reference expansion, plan-mode
// composition — emitting all output as events. The HTTP/SSE server uses this so
// a browser client only POSTs the typed line.
//
// Slash commands route to the matching primitive: /compact and /new run their
// session op and emit a Notice; /mcp_server_prompt and custom /commands
// resolve to a turn; an unknown slash emits a Notice. Anything else is a normal
// turn with its @-references resolved first.
func (c *Controller) Submit(input string) {
	c.mu.Lock()
	c.autoReentryDepth = 0
	c.mu.Unlock()
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "!") {
		c.RunShell(trimmed[1:])
		return
	}
	switch {
	case trimmed == "/compact" || strings.HasPrefix(trimmed, "/compact "):
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		if running {
			c.notice("cannot compact while a turn is running")
			return
		}
		focus := strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact"))
		// Cancel any running turn before compacting to avoid session data
		// corruption from concurrent read/write.
		if c.Running() {
			c.Cancel()
		}
		c.runGuarded(trimmed, func(ctx context.Context) error {
			if err := c.Compact(ctx, focus); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("compaction failed: %w", err)
			}
			c.notice("compacted")
			if err := c.Snapshot(); err != nil {
				slog.Warn("controller: snapshot after compact", "err", err)
			}
			return nil
		})
	case trimmed == "/new":
		// Cancel any running turn before creating a new session to avoid
		// the turn operating on a session that's about to be replaced.
		if c.Running() {
			c.Cancel()
		}
		c.runGuarded(trimmed, func(ctx context.Context) error {
			if err := c.NewSession(); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("new session failed: %w", err)
			}
			c.notice("new session")
			return nil
		})
	case strings.HasPrefix(trimmed, "/mcp_"):
		c.runGuarded(trimmed, func(ctx context.Context) error {
			sent, found, err := c.MCPPrompt(ctx, trimmed)
			if err != nil {
				return err
			}
			if !found {
				c.notice("unknown command: " + trimmed)
				return nil
			}
			return c.runTurnWithRaw(ctx, sent, sent)
		})
	case strings.HasPrefix(trimmed, "/"):
		if ref, ok := FileRefLine(trimmed); ok {
			c.runRefTurn(ref)
			return
		}
		// Read-only management verbs (/model /memory /skills /hooks /mcp) emit a
		// listing Notice, so Submit-based frontends (desktop, HTTP) get them with
		// no extra wiring. (The chat TUI handles these itself with richer output.)
		fields := strings.Fields(trimmed)
		switch fields[0] {
		case "/tree":
			c.notice(c.BranchTreeText())
			return
		case "/branch":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if turn, name, fromTurn, err := ParseBranchTarget(args); err != nil {
				c.notice(err.Error())
			} else if fromTurn {
				if _, err := c.ForkNamed(turn-1, name); err != nil {
					c.notice(err.Error())
				}
			} else {
				if _, err := c.Branch(name); err != nil {
					c.notice(err.Error())
				}
			}
			return
		case "/switch":
			ref := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if _, err := c.SwitchBranch(ref); err != nil {
				c.notice(err.Error())
			}
			return
		case "/rewind":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			turn, scope, err := parseRewind(args, c.Checkpoints())
			if err != nil {
				c.notice("usage: /rewind [turn] [code|conversation|both]")
				return
			}
			if err := c.Rewind(turn, scope); err != nil {
				c.notice(err.Error())
			}
			return
		}
		if c.managementNotice(trimmed) {
			return
		}
		// A custom command wins over a skill of the same name; both resolve to a
		// turn. (Built-in slash verbs like /compact are handled above.)
		if sent, ok := c.CustomCommand(trimmed); ok {
			c.runGuarded(trimmed, func(ctx context.Context) error {
				return c.runTurnWithRaw(ctx, sent, sent)
			})
			return
		}
		if sent, ok := c.RunSkill(trimmed); ok {
			c.runGuarded(trimmed, func(ctx context.Context) error {
				return c.runTurnWithRaw(ctx, sent, sent)
			})
			return
		}
		c.notice("unknown command: " + trimmed)
	default:
		c.runRefTurn(input)
	}
}

// shellTimeout is the maximum time a user-invoked "!command" may run. Matches
// the bash tool's timeout so behaviour is consistent across invocation paths.
const shellTimeout = 120 * time.Second

// shellWaitDelay bounds how long cmd.Run() waits after context cancellation for
// the child's pipes to drain, matching the bash tool's WaitDelay.
const shellWaitDelay = 5 * time.Second

// shellWriter forwards each chunk of shell output to a callback, so RunShell
// can stream live progress to the frontend as the command produces output.
type shellWriter struct{ emit func(string) }

func (w *shellWriter) Write(p []byte) (int, error) {
	w.emit(string(p))
	return len(p), nil
}

// RunShell executes a shell command directly (bypassing the model) and streams
// the output as ToolDispatch/ToolProgress/ToolResult events. It uses the same
// bash-tool infrastructure (shell resolution, timeout) and shares the runGuarded
// lock with model turns — only one can run at a time. User-invoked "!" commands
// run without the OS (the user typed the command explicitly).
func (c *Controller) RunShell(command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		c.notice(i18n.M.ShellExecEmpty)
		return
	}
	c.runGuarded(command, func(ctx context.Context) error {
		sh := shell.ResolveShell()
		argv := sh.Argv(command) // false = unsandboxed (user invoked)

		preview := []rune(command)
		if len(preview) > 32 {
			preview = preview[:32]
		}
		id := "shell-" + string(preview)

		c.sink.Emit(event.Event{
			Kind: event.ToolDispatch,
			Tool: event.Tool{
				ID:   id,
				Name: "bash",
				Args: fmt.Sprintf(`{"command":%q}`, command),
			},
		})

		ctx, cancel := context.WithTimeout(ctx, shellTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		setShellKillTree(cmd)
		cmd.WaitDelay = shellWaitDelay
		cmd.Dir = c.cpRoot
		var buf bytes.Buffer
		w := io.MultiWriter(&buf, &shellWriter{emit: func(chunk string) {
			c.sink.Emit(event.Event{
				Kind: event.ToolProgress,
				Tool: event.Tool{ID: id, Output: chunk},
			})
		}})
		cmd.Stdout = w
		cmd.Stderr = w
		start := time.Now()
		err := cmd.Run()
		durationMs := time.Since(start).Milliseconds()
		out := buf.String()

		if ctx.Err() == context.DeadlineExceeded {
			c.sink.Emit(event.Event{
				Kind: event.ToolResult,
				Tool: event.Tool{ID: id, Name: "bash", Output: out, Err: fmt.Sprintf(i18n.M.ShellExecTimeoutFmt, shellTimeout), DurationMs: durationMs},
			})
			return nil
		}
		if err != nil {
			c.sink.Emit(event.Event{
				Kind: event.ToolResult,
				Tool: event.Tool{ID: id, Name: "bash", Output: out, Err: fmt.Sprintf(i18n.M.ShellExecFailedFmt, err), DurationMs: durationMs},
			})
			return nil
		}
		c.sink.Emit(event.Event{
			Kind: event.ToolResult,
			Tool: event.Tool{ID: id, Name: "bash", Output: out, DurationMs: durationMs},
		})
		return nil
	})
}

// runRefTurn resolves a line's @references into a context block and starts a
// turn with it prepended (or the raw line when nothing resolved).
func (c *Controller) runRefTurn(input string) {
	c.runGuarded(input, func(ctx context.Context) error {
		block, errs := c.ResolveRefs(ctx, input)
		for _, e := range errs {
			c.notice(e)
		}
		sent := input
		if block != "" {
			sent = "Referenced context:\n\n" + block + "\n\n" + input
		}
		return c.runTurnWithRaw(ctx, sent, input)
	})
}

// notice emits an informational Notice event.
func (c *Controller) notice(text string) {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text})
}

// Run executes a turn synchronously, returning the agent's error. Used by the
// headless `reasonix run` path, where the Sink renders to stdout and the caller
// just needs the exit status — no TurnDone event, no cancel bookkeeping.
func (c *Controller) Run(ctx context.Context, input string) error {
	c.maybeSessionStart(ctx)
	startMessages := c.messageCount()
	defer c.snapshotActivityIfChanged(startMessages)
	if c.hooks.Enabled() {
		c.turn++
		if block, _ := c.hooks.PromptSubmit(ctx, input, c.turn); block {
			return nil
		}
		defer func() { c.hooks.Stop(context.Background(), lastAssistantText(c.History()), c.turn) }()
	}
	return c.runner.Run(ctx, input)
}

// Steer injects a user message into the current running turn.
// If no turn is in flight, it falls back to Submit to start a new turn.
func (c *Controller) Steer(input string) {
	c.mu.Lock()
	running := c.running
	runner := c.runner
	c.mu.Unlock()

	if !running || runner == nil {
		go c.Submit(input)
		return
	}

	runner.Steer(input)
}

// Cancel aborts the in-flight turn. A goroutine blocked awaiting approval
// unblocks via the cancelled context.
func (c *Controller) Cancel() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Running reports whether a turn is currently in flight.
func (c *Controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Turn returns the current turn number (0 before the first submit).
func (c *Controller) Turn() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turn
}

// Approve answers a pending ApprovalRequest by ID: allow runs the call, session
// also remembers a tool-wide grant for the rest of the session so that tool
// isn't re-prompted. Unknown/expired IDs are ignored.
func (c *Controller) Approve(id string, allow, session, persist bool) {
	c.mu.Lock()
	reply := c.approvals[id]
	delete(c.approvals, id)
	c.mu.Unlock()
	if reply != nil {
		reply <- approvalReply{allow: allow, session: session, persist: persist} // buffered, never blocks
	}
}

// EnableInteractiveApproval swaps the executor's gate for one that routes "ask"
// decisions to the frontend via ApprovalRequest events, and wires the controller
// in as the executor's Asker so the `ask` tool can question the user. Interactive
// frontends (chat, desktop) call this; the headless run keeps the silent gate and
// a nil asker from setup.
func (c *Controller) EnableInteractiveApproval() {
	if c.executor != nil {
		gate := permission.NewGate(c.policy, gateApprover{c})
		gate.OnRemember = c.onRemember // wire "always allow" persistence callback
		c.executor.SetGate(gate)
		c.executor.SetAsker(c)
	}
	// 给子代理也注入交互式审批，让 ask 规则能弹窗
	if c.subAgentGate != nil {
		c.subAgentGate.SetApprover(gateApprover{c})
		c.subAgentGate.OnRemember = c.onRemember
	}
}

// Ask implements agent.Asker: it emits an AskRequest and blocks until
// AnswerQuestion(ID, …) answers or ctx is cancelled. promptMu serialises it
// against tool-approval prompts so at most one user prompt is outstanding.
// Unlike tool-approval gates, Ask is NOT bypassed in YOLO mode — the `ask`
// tool exists to get a genuine user decision, and YOLO only auto-approves
// tool calls; it must not answer the user's questions for them.
func (c *Controller) Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error) {
	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	c.mu.Lock()
	c.nextID++
	id := strconv.Itoa(c.nextID)
	reply := make(chan []event.AskAnswer, 1)
	c.asks[id] = reply
	c.mu.Unlock()

	c.sink.Emit(event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: id, Questions: questions}})

	select {
	case ans := <-reply:
		return ans, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.asks, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// AnswerQuestion resolves a pending AskRequest by ID with the user's selections.
// Unknown/expired IDs are ignored.
func (c *Controller) AnswerQuestion(id string, answers []event.AskAnswer) {
	c.mu.Lock()
	reply := c.asks[id]
	delete(c.asks, id)
	c.mu.Unlock()
	if reply != nil {
		reply <- answers // buffered, never blocks
	}
}

// Compact runs one compaction pass on the executor's session on demand.
// instructions is optional `/compact <focus>` guidance steering what to keep.
func (c *Controller) Compact(ctx context.Context, instructions string) error {
	if c.executor == nil {
		return nil
	}
	return c.executor.CompactNow(ctx, instructions)
}

// maybeSessionStart fires the SessionStart hook exactly once per session, lazily
// on the first turn — by then the sink/notify is wired, and a resumed session
// fires it too (its first post-resume turn).
func (c *Controller) maybeSessionStart(ctx context.Context) {
	c.mu.Lock()
	if c.startedOnce {
		c.mu.Unlock()
		return
	}
	c.startedOnce = true
	c.mu.Unlock()
	c.hooks.SessionStart(ctx)
}

// NewSession snapshots the current conversation, rotates to a fresh file, and
// resets the executor to a clean session carrying the same system prompt. It
// ends the old session and starts the new one for lifecycle hooks.
func (c *Controller) NewSession() error {
	if c.executor == nil {
		return nil
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		return fmt.Errorf("cannot start new session while a turn is running")
	}
	if err := c.Snapshot(); err != nil {
		return err
	}
	c.hooks.SessionEnd(context.Background())
	if c.executor != nil {
		c.executor.ResetCtxStore()
	}
	if c.sessionDir != "" {
		c.mu.Lock()
		c.sessionPath = agent.NewSessionPath(c.sessionDir, c.label)
		c.mu.Unlock()
	}
	c.executor.SetSession(agent.NewSession(c.systemPrompt))
	c.executor.ResetSessionCost()
	c.rebindCheckpoints(c.SessionPath())
	c.mu.Lock()
	c.startedOnce = true // NewSession fires SessionStart itself; don't re-fire on the next turn
	c.mu.Unlock()
	c.hooks.SessionStart(context.Background())
	return nil
}

// RewindScope selects what a Rewind restores.
type RewindScope int

const (
	RewindCode         RewindScope = iota // files only
	RewindConversation                    // message log only
	RewindBoth                            // both
)

// Checkpoints lists the session's rewind points (one per user turn), oldest first.
func (c *Controller) Checkpoints() []checkpoint.Meta {
	if c.cp == nil {
		return nil
	}
	return c.cp.List()
}

// rewindFail emits the error as a Warn notice (so a frontend that swallows the
// returned error — e.g. the desktop bridge's .catch — still shows the user why
// the rewind did nothing) and returns it.
func (c *Controller) rewindFail(err error) error {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: err.Error()})
	return err
}

// Rewind restores the session to the start of `turn`: Code reverts every file that
// turn (or a later one) changed to its pre-turn content; Conversation truncates the
// message log back to that turn; Both does both. Refused while a turn is running.
// Conversation rewind relies on the live boundary recorded at turn start, so it is
// unavailable for turns inherited from a resumed session (code rewind still works).
// Frontends re-render their transcript from History after the call.
func (c *Controller) Rewind(turn int, scope RewindScope) error {
	if c.cp == nil || c.executor == nil {
		return c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	c.mu.Lock()
	running := c.running
	boundary, hasBound := c.cpBound[turn]
	c.mu.Unlock()
	if running {
		return c.rewindFail(fmt.Errorf("cannot rewind while a turn is running"))
	}

	if scope == RewindCode || scope == RewindBoth {
		written, deleted, err := c.cp.RestoreCode(turn)
		if err != nil {
			return c.rewindFail(fmt.Errorf("rewind code: %w", err))
		}
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: fmt.Sprintf("rewound code to turn %d — %d file(s) restored, %d removed", turn, len(written), len(deleted))})
	}
	if scope == RewindConversation || scope == RewindBoth {
		if !hasBound {
			return c.rewindFail(fmt.Errorf("conversation rewind unavailable for turn %d (resumed session)", turn))
		}
		s := c.executor.Session()
		if boundary <= len(s.Messages) {
			s.Messages = s.Messages[:boundary]
			c.mu.Lock()
			c.cpTurn = turn // renumber future turns from here; later turns are gone
			for k := range c.cpBound {
				if k >= turn {
					delete(c.cpBound, k)
				}
			}
			c.mu.Unlock()
			if err := c.Snapshot(); err != nil {
				slog.Warn("controller: snapshot after rewind", "err", err)
			}
		}
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
			Text: fmt.Sprintf("rewound conversation to turn %d", turn)})
	}
	return nil
}

// Fork branches the conversation at the start of turn into a NEW session file,
// preserving the current one as the branch point, and switches to the branch. Code
// is untouched (it's a conversation operation). Like a conversation rewind it needs
// the live boundary, so it is unavailable for resumed-session turns and refused
// while a turn runs. Returns the new session path.
func (c *Controller) Fork(turn int) (string, error) {
	return c.ForkNamed(turn, "")
}

func (c *Controller) ForkNamed(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, true)
}

// ForkSession copies the conversation at the start of turn into a new session
// file without switching this controller to it. Desktop uses this to open the
// branch in a new tab while the source tab keeps its current transcript.
func (c *Controller) ForkSession(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, false)
}

func (c *Controller) forkNamed(turn int, name string, switchToFork bool) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("fork needs session persistence, which is disabled"))
	}
	c.mu.Lock()
	running := c.running
	boundary, hasBound := c.cpBound[turn]
	c.mu.Unlock()
	if running {
		return "", c.rewindFail(fmt.Errorf("cannot fork while a turn is running"))
	}
	if !hasBound {
		return "", c.rewindFail(fmt.Errorf("fork unavailable for turn %d (resumed session)", turn))
	}

	// Persist the current conversation first so the branch point survives, then
	// seed a fresh session with the messages up to the fork and switch to it.
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: pre-fork snapshot", "err", err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	if boundary > len(src) {
		boundary = len(src)
	}
	forked := append([]provider.Message(nil), src[:boundary]...)
	sess := agent.NewSession("")
	sess.Messages = forked

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.Save(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         turn,
		ForkMessageIndex: boundary,
	}); err != nil {
		return "", c.rewindFail(err)
	}
	if switchToFork {
		c.executor.SetSession(sess)
		c.mu.Lock()
		c.sessionPath = newPath
		c.mu.Unlock()
		c.rebindCheckpoints(newPath)
	}
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("forked conversation at turn %d into a new session", turn)})
	return newPath, nil
}

func (c *Controller) CheckpointHasBoundary(turn int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.cpBound[turn]
	return ok
}

// Branch copies the current conversation into a child branch and switches to it.
// Unlike Fork, it branches at the current tip and does not require a checkpoint.
func (c *Controller) Branch(name string) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("branch unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("branch needs session persistence, which is disabled"))
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		return "", c.rewindFail(fmt.Errorf("cannot branch while a turn is running"))
	}
	if !c.executor.Session().HasContent() {
		return "", c.rewindFail(fmt.Errorf("nothing to branch yet"))
	}
	if err := c.Snapshot(); err != nil {
		return "", c.rewindFail(err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	branched := append([]provider.Message(nil), src...)
	sess := agent.NewSession("")
	sess.Messages = branched

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.Save(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         -1,
		ForkMessageIndex: len(branched),
	}); err != nil {
		return "", c.rewindFail(err)
	}
	c.executor.SetSession(sess)
	c.mu.Lock()
	c.sessionPath = newPath
	c.mu.Unlock()
	c.rebindCheckpoints(newPath)
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("created branch %s", agent.BranchID(newPath))})
	return newPath, nil
}

// Branches lists saved conversation branches in this controller's session dir.
func (c *Controller) Branches() ([]agent.BranchInfo, error) {
	if c.sessionDir == "" {
		return nil, fmt.Errorf("session persistence is disabled")
	}
	if err := c.Snapshot(); err != nil {
		return nil, err
	}
	return agent.ListBranches(c.sessionDir)
}

func (c *Controller) SwitchBranch(ref string) (agent.BranchInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("usage: /switch <branch id|name>"))
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("cannot switch branches while a turn is running"))
	}
	branches, err := c.Branches()
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	match, err := resolveBranch(branches, ref)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	loaded, err := agent.LoadSession(match.Path)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	if c.executor != nil {
		c.executor.SetSession(loaded)
	}
	c.mu.Lock()
	c.sessionPath = match.Path
	c.mu.Unlock()
	c.rebindCheckpoints(match.Path)
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("switched to branch %s", branchDisplayName(match))})
	return match, nil
}

func resolveBranch(branches []agent.BranchInfo, ref string) (agent.BranchInfo, error) {
	refLower := strings.ToLower(ref)
	var matches []agent.BranchInfo
	for _, b := range branches {
		nameLower := strings.ToLower(strings.TrimSpace(b.Name))
		switch {
		case b.ID == ref || strings.EqualFold(b.ID, ref):
			return b, nil
		case b.Name != "" && nameLower == refLower:
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(b.ID), refLower):
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(shortBranchID(b.ID)), refLower):
			matches = append(matches, b)
		case b.Path == ref:
			return b, nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return agent.BranchInfo{}, fmt.Errorf("branch %q is ambiguous", ref)
	}
	return agent.BranchInfo{}, fmt.Errorf("branch %q not found", ref)
}

func branchDisplayName(b agent.BranchInfo) string {
	if strings.TrimSpace(b.Name) != "" {
		return fmt.Sprintf("%s (%s)", b.Name, b.ID)
	}
	return b.ID
}

// SummarizeFrom compresses the conversation from turn onward into one summary;
// SummarizeUpTo compresses everything before it. Both are Claude Code's "summarize
// from/up to here" — they restructure the message log (keeping code untouched), so
// afterwards the per-turn boundaries no longer map and conversation rewind/fork
// report "unavailable" until new turns rebuild them (code rewind, file-based, is
// unaffected). Refused while a turn runs; need the live boundary.
func (c *Controller) SummarizeFrom(ctx context.Context, turn int) error {
	return c.summarizeAt(ctx, turn, true)
}

func (c *Controller) SummarizeUpTo(ctx context.Context, turn int) error {
	return c.summarizeAt(ctx, turn, false)
}

func (c *Controller) summarizeAt(ctx context.Context, turn int, from bool) error {
	if c.executor == nil {
		return c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	c.mu.Lock()
	running := c.running
	boundary, hasBound := c.cpBound[turn]
	c.mu.Unlock()
	if running {
		return c.rewindFail(fmt.Errorf("cannot summarize while a turn is running"))
	}
	if !hasBound {
		return c.rewindFail(fmt.Errorf("summarize unavailable for turn %d (resumed session)", turn))
	}
	var err error
	if from {
		err = c.executor.SummarizeFrom(ctx, boundary)
	} else {
		err = c.executor.SummarizeUpTo(ctx, boundary)
	}
	if err != nil {
		return c.rewindFail(err)
	}
	// The log was restructured; existing boundaries no longer map. Drop them (keep
	// cpTurn monotonic so new turns don't collide with the store) — conversation
	// rewind degrades to "unavailable" until fresh turns rebuild boundaries.
	c.mu.Lock()
	c.cpBound = map[int]int{}
	c.mu.Unlock()
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: post-summarize snapshot", "err", err)
	}
	return nil
}

// Resume seeds the session from a loaded transcript and pins the active file to
// its path so auto-save keeps appending there. The system prompt always comes
// from the current boot (latest REASONIX.md / config), not from the saved file —
// stale system messages in JSONL are dropped so resume never loses global rules.
func (c *Controller) Resume(s *agent.Session, path string) {
	if c.executor != nil {
		c.executor.SetSession(mergeResumedSession(c.systemPrompt, s))
		// Restore cumulative cost from sidecar file, if one exists.
		if cost, currency := readSessionCost(path); cost > 0 {
			c.executor.SetSessionCost(cost, currency)
		}
		// Restore cumulative cache/token stats from sidecar file, if one exists.
		if hit, miss, prompt, total := readSessionCache(path); hit > 0 || miss > 0 {
			c.executor.SetSessionCache(hit, miss, prompt, total)
		}
	}
	c.mu.Lock()
	c.sessionPath = path
	c.mu.Unlock()
	c.rebindCheckpoints(path)
}

// sessionCostSidecar is the path convention for cost metadata alongside a
// session JSONL file.
func sessionCostSidecar(sessionPath string) string {
	return sessionPath + ".cost"
}

// readSessionCost reads the cost sidecar written by snapshot. Missing or
// unparseable files are silently treated as "no cost" so resume never breaks.
func readSessionCost(path string) (cost float64, currency string) {
	b, err := os.ReadFile(sessionCostSidecar(path))
	if err != nil {
		return 0, ""
	}
	var v struct {
		Cost     float64 `json:"cost"`
		Currency string  `json:"currency"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0, ""
	}
	return v.Cost, v.Currency
}

// writeSessionCost persists the cumulative cost alongside a session JSONL.
func writeSessionCost(path string, cost float64, currency string) error {
	if cost <= 0 || currency == "" {
		os.Remove(sessionCostSidecar(path))
		return nil
	}
	v := struct {
		Cost     float64 `json:"cost"`
		Currency string  `json:"currency"`
	}{Cost: cost, Currency: currency}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(sessionCostSidecar(path), b, 0o600)
}

// sessionCacheSidecar is the path convention for cache/token metadata alongside
// a session JSONL file.
func sessionCacheSidecar(sessionPath string) string {
	return sessionPath + ".cache"
}

// readSessionCache reads the cache sidecar written by snapshot. Missing or
// unparseable files are silently treated as zeros so resume never breaks.
func readSessionCache(path string) (hit, miss, prompt, total int64) {
	b, err := os.ReadFile(sessionCacheSidecar(path))
	if err != nil {
		return 0, 0, 0, 0
	}
	var v struct {
		Hit    int64 `json:"cacheHit"`
		Miss   int64 `json:"cacheMiss"`
		Prompt int64 `json:"promptTokens"`
		Total  int64 `json:"totalTokens"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0, 0, 0, 0
	}
	return v.Hit, v.Miss, v.Prompt, v.Total
}

// writeSessionCache persists the cumulative cache/token stats alongside a
// session JSONL.
func writeSessionCache(path string, hit, miss, prompt, total int64) error {
	if hit == 0 && miss == 0 && prompt == 0 && total == 0 {
		os.Remove(sessionCacheSidecar(path))
		return nil
	}
	v := struct {
		Hit    int64 `json:"cacheHit"`
		Miss   int64 `json:"cacheMiss"`
		Prompt int64 `json:"promptTokens"`
		Total  int64 `json:"totalTokens"`
	}{Hit: hit, Miss: miss, Prompt: prompt, Total: total}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(sessionCacheSidecar(path), b, 0o600)
}

func mergeResumedSession(systemPrompt string, loaded *agent.Session) *agent.Session {
	merged := agent.NewSession(systemPrompt)
	if loaded == nil {
		return merged
	}
	for _, m := range loaded.Messages {
		if m.Role == provider.RoleSystem {
			continue
		}
		merged.Add(m)
	}
	return merged
}

// Snapshot writes the executor's conversation to the active session file. No-op
// when persistence is unavailable or the session has never been used (no user
// interaction). Called after every turn so a crash loses at most one in-flight
// prompt.
func (c *Controller) Snapshot() error {
	return c.snapshot(false)
}

// SnapshotActivity writes the active conversation and marks the session as
// recently active. Use it only after a real user/model turn changes the
// transcript; switch/close snapshots should call Snapshot so they do not reorder
// recent-session pickers.
func (c *Controller) SnapshotActivity() error {
	return c.snapshot(true)
}

func (c *Controller) snapshot(markActivity bool) error {
	c.mu.Lock()
	path := c.sessionPath
	c.mu.Unlock()
	if c.executor == nil || path == "" {
		return nil
	}
	s := c.executor.Session()
	if !s.HasContent() {
		return nil
	}
	if !markActivity {
		if _, err := agent.EnsureBranchMeta(path); err != nil {
			return err
		}
	}
	if err := s.Save(path); err != nil {
		return err
	}
	// Persist cumulative cost alongside the session so resume restores it.
	if cost, currency := c.executor.SessionCost(); cost > 0 && currency != "" {
		if err := writeSessionCost(path, cost, currency); err != nil {
			slog.Warn("controller: write session cost sidecar", "err", err)
		}
	}
	// Persist cumulative cache/token stats alongside the session so resume
	// restores them (P2b). Always writes when any counter is non-zero.
	if hit, miss := c.executor.SessionCache(); hit > 0 || miss > 0 {
		prompt, total := c.executor.SessionTokens()
		if err := writeSessionCache(path, int64(hit), int64(miss), prompt, total); err != nil {
			slog.Warn("controller: write session cache sidecar", "err", err)
		}
	}
	if markActivity {
		return agent.TouchBranchMeta(path)
	}
	return nil
}

func (c *Controller) messageCount() int {
	if c.executor == nil {
		return 0
	}
	return len(c.executor.Session().Snapshot())
}

func (c *Controller) snapshotActivityIfChanged(startMessages int) {
	if c.messageCount() <= startMessages {
		return
	}
	if err := c.SnapshotActivity(); err != nil {
		slog.Warn("controller: activity snapshot", "err", err)
	}
}

// SetSessionPath pins where auto-save lands (a fresh session file minted by the
// caller when no resume path applies).
func (c *Controller) SetSessionPath(p string) {
	c.mu.Lock()
	c.sessionPath = p
	c.mu.Unlock()
	c.rebindCheckpoints(p)
}

// SessionDir reports the directory new session files land in ("" disables
// persistence), so the caller can decide whether to mint a path.
func (c *Controller) SessionDir() string { return c.sessionDir }

// SessionPath reports the file the current conversation auto-saves to ("" when
// persistence is disabled), so a history view can mark the active session.
func (c *Controller) SessionPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionPath
}

// History returns the executor's current message log (for repopulating a
// resumed frontend's view).
func (c *Controller) History() []provider.Message {
	if c.executor == nil {
		return nil
	}
	return c.executor.Session().Snapshot() // copy — a turn may be appending concurrently
}

// ContextSnapshot returns (promptTokens, contextWindow) from the most recent
// turn. Both zero means no data yet — a gauge hides itself.
func (c *Controller) ContextSnapshot() (int, int) {
	if c.executor == nil {
		return 0, 0
	}
	u := c.executor.LastUsage()
	if u == nil {
		return 0, c.executor.ContextWindow()
	}
	return u.PromptTokens, c.executor.ContextWindow()
}

// CompactRatio returns the auto-compaction threshold as a fraction of the window
// (0 when the executor is unset). The status line shows headroom against it.
func (c *Controller) CompactRatio() float64 {
	if c.executor == nil {
		return 0
	}
	return c.executor.CompactRatio()
}

// LastUsage returns the most recent turn's token telemetry (nil before the first
// turn), so frontends can derive the prompt cache-hit rate for the status line.
func (c *Controller) LastUsage() *provider.Usage {
	if c.executor == nil {
		return nil
	}
	return c.executor.LastUsage()
}

// SessionCache returns cumulative cache hit/miss prompt tokens for the session,
// so a frontend can render the aggregate (session-wide) cache-hit rate — steadier
// than the single-turn rate and unaffected by compaction.
func (c *Controller) SessionCache() (hit, miss int) {
	if c.executor == nil {
		return 0, 0
	}
	return c.executor.SessionCache()
}

// SessionCost returns the cumulative conversation cost and its currency.
func (c *Controller) SessionCost() (cost float64, currency string) {
	if c.executor == nil {
		return 0, ""
	}
	return c.executor.SessionCost()
}

// SetSessionCost restores cumulative cost from a loaded session sidecar.
func (c *Controller) SetSessionCost(cost float64, currency string) {
	if c.executor != nil {
		c.executor.SetSessionCost(cost, currency)
	}
}

// Balance queries the active provider's wallet balance, or (nil, nil) when the
// provider declares no balance_url — so a caller treats "not configured" and
// "fetched" the same and just omits the readout when nil.
func (c *Controller) Balance(ctx context.Context) (*billing.Balance, error) {
	if strings.TrimSpace(c.balanceURL) == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return billing.FetchWithClient(ctx, c.balanceClient, c.balanceURL, c.balanceKey)
}

// Host returns the running MCP host (nil when no plugins), for frontends that
// list servers / resolve MCP prompts.
func (c *Controller) Host() *plugin.Host { return c.host }

// Commands returns the loaded custom slash commands.
func (c *Controller) Commands() []command.Command { return c.commands }

// Skills returns the discoverable skills (for the slash menu and `/skills`).
// When a live Store is available, scan it on demand so skills installed during
// this session appear without rewriting the cache-stable system prompt.
func (c *Controller) Skills() []skill.Skill {
	if c.skillStore != nil {
		return c.skillStore.List()
	}
	return c.skills
}

// AllSkills returns every discoverable skill, including disabled ones, for
// management surfaces that need to re-enable a hidden skill.
func (c *Controller) AllSkills() []skill.Skill {
	if c.allSkillStore != nil {
		return c.allSkillStore.List()
	}
	if len(c.allSkills) > 0 {
		return c.allSkills
	}
	return c.skills
}

// Config returns the boot-time configuration, or nil.
func (c *Controller) Config() *config.Config {
	return c.cfg
}

// DisabledSkills returns all discoverable skills that are disabled in config.
func (c *Controller) DisabledSkills() []skill.Skill {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var out []skill.Skill
	for _, sk := range c.AllSkills() {
		if cfg.IsSkillDisabled(sk.Name) {
			out = append(out, sk)
		}
	}
	return out
}

// SkillEnabled reports whether a discoverable skill is enabled.
func (c *Controller) SkillEnabled(name string) bool {
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	return !cfg.IsSkillDisabled(name)
}

// SetSkillEnabled persists a skill enable/disable preference. The caller should
// rebuild the controller for the prompt/tool registry to reflect it immediately.
func (c *Controller) SetSkillEnabled(name string, enabled bool) error {
	found := false
	for _, sk := range c.AllSkills() {
		if config.SkillNameKey(sk.Name) == config.SkillNameKey(name) {
			name = sk.Name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown skill: %s", name)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if err := cfg.SetSkillEnabled(name, enabled); err != nil {
		return err
	}
	return cfg.SaveTo(config.UserConfigPath())
}

// HookRunner returns the session's hook runner (nil-safe; may hold zero hooks),
// so a frontend can list the active hooks via `/hooks`.
func (c *Controller) HookRunner() *hook.Runner { return c.hooks }

// AddMCPServer connects an MCP server live and persists it to the config file. Its
// tools are registered immediately and become available on the next turn (the
// agent reads the registry per turn). The raw entry — ${VARS} intact — is what's
// written to disk; the live connection uses the expanded form. Returns the number
// of tools the server exposed. A save failure after a successful connect is
// reported but non-fatal: the server still works this session.
func (c *Controller) AddMCPServer(e config.PluginEntry) (int, error) {
	n, err := c.connectMCPServer(e)
	if err != nil {
		return 0, err
	}
	cfg, lerr := config.Load()
	if lerr != nil {
		return n, fmt.Errorf("connected, but reloading config to save failed: %w", lerr)
	}
	if err := cfg.UpsertPlugin(e); err != nil {
		return n, fmt.Errorf("connected, but config rejected the entry: %w", err)
	}
	if err := cfg.Save(); err != nil {
		return n, fmt.Errorf("connected, but saving config failed: %w", err)
	}
	return n, nil
}

// ConnectMCPServer connects an MCP server entry for this session without writing
// it to config. Desktop owns config placement so it can keep user-level settings
// out of project reasonix.toml while preserving the CLI AddMCPServer semantics.
func (c *Controller) ConnectMCPServer(e config.PluginEntry) (int, error) {
	return c.connectMCPServer(e)
}

func (c *Controller) connectMCPServer(e config.PluginEntry) (int, error) {
	exp := e.ExpandedPlugin()
	return c.connectMCPSpec(plugin.Spec{
		Name:    exp.Name,
		Type:    exp.Type,
		Command: exp.Command,
		Args:    exp.Args,
		Env:     exp.Env,
		URL:     exp.URL,
		Headers: exp.Headers,
	})
}

func (c *Controller) connectMCPSpec(s plugin.Spec) (int, error) {
	if c.host == nil {
		c.host = plugin.NewHost()
	}
	c.host.SetRegistry(c.reg)
	tools, err := c.host.Add(c.pluginCtx, s)
	if err != nil {
		return 0, err
	}
	return len(tools), nil
}

// ImportMCPEntries persists selected MCP entries and attempts to connect them
// live. A connection failure does not roll back the config import: the user can
// fix local dependencies and reconnect in a later session.
func (c *Controller) ImportMCPEntries(entries []config.PluginEntry) (total, added, updated, connected, failed, skipped int, err error) {
	cfg, lerr := config.Load()
	if lerr != nil {
		return 0, 0, 0, 0, 0, 0, lerr
	}
	existing := make(map[string]bool, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		existing[p.Name] = true
	}
	for _, e := range entries {
		if existing[e.Name] {
			updated++
		} else {
			added++
		}
		if err := cfg.UpsertPlugin(e); err != nil {
			return 0, 0, 0, 0, 0, 0, err
		}
		existing[e.Name] = true
	}
	if err := cfg.Save(); err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	for _, e := range entries {
		if c.host != nil && containsString(c.host.ServerNames(), e.Name) {
			skipped++
			continue
		}
		if _, err := c.AddMCPServer(e); err != nil {
			failed++
			continue
		}
		connected++
	}
	return len(entries), added, updated, connected, failed, skipped, nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func (c *Controller) ConfiguredMCPNames() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		names = append(names, p.Name)
	}
	return names
}

func (c *Controller) DisconnectedMCPNames() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	connected := map[string]bool{}
	if c.host != nil {
		for _, name := range c.host.ServerNames() {
			connected[name] = true
		}
	}
	var names []string
	for _, p := range cfg.Plugins {
		if !connected[p.Name] {
			names = append(names, p.Name)
		}
	}
	return names
}

func (c *Controller) ConnectConfiguredMCPServer(name string) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return c.connectMCPServer(p)
		}
	}
	return 0, fmt.Errorf("no configured MCP server named %q", name)
}

// RemoveMCPServer disconnects a live MCP server — its tools vanish from the next
// turn — and removes it from the config file. It reports whether a live server was
// disconnected; an error only when the name is neither connected nor in config (or
// the config save fails). A server declared in .mcp.json disconnects for this
// session but returns on the next start, since that file isn't ours to edit.
func (c *Controller) RemoveMCPServer(name string) (disconnected bool, err error) {
	if c.host != nil {
		if _, ok := c.host.Remove(name); ok {
			disconnected = true
		}
	}
	cfg, lerr := config.Load()
	if lerr != nil {
		return disconnected, lerr
	}
	inConfig := cfg.RemovePlugin(name)
	if inConfig {
		if !disconnected && c.reg != nil {
			c.reg.RemovePrefix(plugin.ToolPrefix(name))
		}
		if serr := cfg.Save(); serr != nil {
			return disconnected, serr
		}
	}
	if !disconnected && !inConfig {
		return false, fmt.Errorf("no MCP server named %q", name)
	}
	return disconnected, nil
}

// DisconnectMCPServer disconnects a live server for this session without touching
// config — the connector toggle's "off". Its tools vanish next turn; it reconnects
// on the next session start, or now via ConnectConfiguredMCPServer (the "on").
// Reports whether a live server was actually disconnected.
func (c *Controller) DisconnectMCPServer(name string) bool {
	disconnected := false
	if c.host != nil {
		if _, ok := c.host.Remove(name); ok {
			disconnected = true
		}
	}
	removedPlaceholder := 0
	if !disconnected && c.reg != nil {
		removedPlaceholder = c.reg.RemovePrefix(plugin.ToolPrefix(name))
	}
	return disconnected || removedPlaceholder > 0
}

// Label returns the human-readable model label, e.g. "deepseek-flash".
func (c *Controller) Label() string { return c.label }

// WorkspaceRoot returns the workspace root for this controller's session
// (the directory that file-writers and @-references are scoped to).
// Empty means no scoping is in effect.
func (c *Controller) WorkspaceRoot() string { return c.cpRoot }

// Close stops plugin subprocesses and releases resources. A session that ever
// started fires SessionEnd so a teardown hook runs.
func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		// closeCancel is a no-op placeholder reserved for future use.
		c.Cancel() // cancel the currently running turn so wg.Wait() unblocks

		// wg.Wait with 30-second timeout to prevent hanging on shutdown.
		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			slog.Warn("controller: shutdown timed out waiting for goroutines")
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
		if c.jobs != nil {
			c.jobs.Close() // cancel any still-running background jobs
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

// Jobs returns the still-running background jobs for the status bar (nil when
// background jobs are disabled).
func (c *Controller) Jobs() []jobs.View {
	if c.jobs == nil {
		return nil
	}
	return c.jobs.Running()
}

// SteerBackgroundJob sends a steer message to one session background job (task/bash).
func (c *Controller) SteerBackgroundJob(jobID, message string) error {
	if c.jobs == nil {
		return fmt.Errorf("background jobs are not enabled")
	}
	return c.jobs.Steer(jobID, message)
}

// KillBackgroundJob cancels one background job by id.
func (c *Controller) KillBackgroundJob(jobID string) bool {
	if c.jobs == nil {
		return false
	}
	return c.jobs.Kill(jobID)
}

// PeekBackgroundJob returns a non-blocking snapshot of one background job.
func (c *Controller) PeekBackgroundJob(jobID string) (jobs.JobStatus, error) {
	if c.jobs == nil {
		return jobs.JobStatus{}, fmt.Errorf("background jobs are not enabled")
	}
	return c.jobs.Peek(jobID)
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

// --- memory ---
//
// c.mem is treated as an immutable snapshot guarded by c.mu: reads take the lock
// and return the pointer; writes mutate disk then swap in a freshly discovered
// snapshot. A turn-tail note is queued for each write so the change applies this
// session without disturbing the cache-stable system prefix (it folds into the
// prefix on the next session). All of these are no-ops returning "" when memory
// is disabled.

// QuickAdd appends a one-line note to the doc-memory file for scope (project
// REASONIX.md by default) — the write side of "#<note>". Returns the file written.
func (c *Controller) QuickAdd(scope memory.Scope, note string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	path := c.mem.DocPath(scope)
	if path == "" {
		return "", fmt.Errorf("no target file for memory scope %q", scope)
	}
	if err := memory.AppendDoc(path, note); err != nil {
		return "", err
	}
	c.pendingMemory = append(c.pendingMemory, note)
	c.refreshMemoryLocked()
	return path, nil
}

// SaveDoc overwrites a recognized memory doc with body — the save side of the
// desktop panel's in-place editor. Returns the file written.
func (c *Controller) SaveDoc(path, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	written, err := c.mem.WriteDoc(path, body)
	if err != nil {
		return "", err
	}
	// Inject the new content once on the next turn: the cached prefix still holds
	// the pre-edit version this session, so handing the model the current text
	// avoids a stale-guidance gap until the next session re-folds it into the
	// prefix. Trimmed to a single tail note (drained by Compose), not per-turn.
	c.pendingMemory = append(c.pendingMemory,
		"Memory file "+written+" was just edited. Its current contents:\n"+strings.TrimSpace(body))
	c.refreshMemoryLocked()
	return written, nil
}

// ForgetMemory deletes a saved auto-memory by name — the panel/TUI delete action,
// the manual counterpart to the model's `forget` tool. It queues a turn-tail note
// so the deletion applies this session (the cached prefix still lists the fact
// until the next session re-folds the index).
func (c *Controller) ForgetMemory(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return nil
	}
	if err := c.mem.Store.Delete(name); err != nil {
		return err
	}
	c.pendingMemory = append(c.pendingMemory,
		"Deleted memory \""+name+"\" — disregard its line still shown in the saved-memories index until next session.")
	c.refreshMemoryLocked()
	return nil
}

// QueueMemory implements memory.Queue: when the model runs the remember/forget
// tool, the tool calls this with a note that rides the next turn so the change
// applies this session without touching the cache-stable prefix. It also
// refreshes the snapshot a memory panel reads.
func (c *Controller) QueueMemory(note string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingMemory = append(c.pendingMemory, note)
	c.refreshMemoryLocked()
}

// Memory returns the loaded memory snapshot (nil when memory is disabled), for
// frontends that surface a memory panel or the /memory command. The returned
// *Set is immutable — mutations go through QuickAdd / SaveDoc.
func (c *Controller) Memory() *memory.Set {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mem
}

// refreshMemoryLocked re-discovers memory from disk so a later Memory() reflects
// a just-applied write. Caller holds c.mu.
func (c *Controller) refreshMemoryLocked() {
	if c.mem == nil {
		return
	}
	c.mem = memory.Load(memory.Options{CWD: c.mem.CWD, UserDir: c.mem.UserDir})
}

// --- approval bridge (agent gate → events) ---

// gateApprover adapts the Controller to permission.Approver. It is distinct
// from the public Approve command (different signature, different direction).
type gateApprover struct{ c *Controller }

func (g gateApprover) Approve(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	// Auto-allow without prompting while YOLO/bypass mode is on. Deny rules
	// already bit before this point, so they still block.
	g.c.mu.Lock()
	auto := g.c.bypass
	g.c.mu.Unlock()
	if auto {
		return true, false, nil
	}
	scope := "gate"
	if tool == "task" {
		scope = "task"
	}
	preview := permission.Preview(tool, args)
	return g.c.requestApproval(ctx, tool, subject, preview, scope)
}

// requestApproval emits an ApprovalRequest and blocks until Approve(ID, …)
// answers or ctx is cancelled. A prior tool-wide session grant short-circuits.
// promptMu serialises outstanding prompts.
// parseRewind parses the arguments after "/rewind". The user may provide:
//
//	/rewind              → latest checkpoint, both
//	/rewind <turn>       → that turn, both
//	/rewind <turn> <scope> → that turn, code|conversation|both
//
// If no turn is given, the latest checkpoint is used. If no scope is given, Both is assumed.
func parseRewind(args string, cps []checkpoint.Meta) (int, RewindScope, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		if len(cps) == 0 {
			return 0, RewindBoth, fmt.Errorf("no checkpoints available")
		}
		return cps[len(cps)-1].Turn, RewindBoth, nil
	}
	turn, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, RewindBoth, fmt.Errorf("invalid turn: %w", err)
	}
	scope := RewindBoth
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "code":
			scope = RewindCode
		case "conversation":
			scope = RewindConversation
		case "both":
			scope = RewindBoth
		default:
			return 0, RewindBoth, fmt.Errorf("unknown scope %q", fields[1])
		}
	}
	return turn, scope, nil
}

func (c *Controller) requestApproval(ctx context.Context, tool, subject, preview, scope string) (bool, bool, error) {
	// Session grants are tool-wide: "allow for this session" / "allow persistently"
	// mean the user trusts this tool (write_file, bash, …), not just this one
	// file/command, so a different subject for the same tool isn't re-prompted.
	// Deny rules still bite upstream of here.
	key := tool

	c.mu.Lock()
	// YOLO/bypass auto-allows every approval without prompting.
	// Deny rules bit upstream.
	if c.bypass || c.granted[key] {
		c.mu.Unlock()
		return true, false, nil
	}
	c.mu.Unlock()

	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	// Re-check the grant: a session grant may have landed while we queued behind
	// another prompt for the same subject.
	c.mu.Lock()
	if c.bypass || c.granted[key] {
		c.mu.Unlock()
		return true, false, nil
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	reply := make(chan approvalReply, 1)
	c.approvals[id] = reply
	c.mu.Unlock()

	c.sink.Emit(event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: id, Tool: tool, Subject: subject, Preview: preview, Scope: scope}})
	// The agent now needs the user's attention; a Notification hook can ping an
	// external channel (desktop notice, phone) while the run blocks on the reply.
	if subject != "" {
		go c.hooks.Notification(ctx, "approval needed: "+tool+" "+subject)
	} else {
		go c.hooks.Notification(ctx, "approval needed: "+tool)
	}

	select {
	case r := <-reply:
		if r.allow && r.session {
			c.mu.Lock()
			c.granted[key] = true
			c.mu.Unlock()
		}
		// When persist is true, remember=true signals Gate.OnRemember to write
		// the rule to the on-disk config.
		remember := r.persist
		return r.allow, remember, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.approvals, id)
		c.mu.Unlock()
		return false, false, ctx.Err()
	}
}
