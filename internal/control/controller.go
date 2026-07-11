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
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/checkpoint"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/ctxmode"
	"reasonix/internal/diff"
	"reasonix/internal/event"
	"reasonix/internal/multiagent"
	"reasonix/internal/hook"
	"reasonix/internal/memory"
	"reasonix/internal/nilutil"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
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
	turnActive        bool
	sessionPath       string
	approvals         map[string]chan approvalReply
	asks              map[string]chan []event.AskAnswer
	granted           map[string]bool
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
		reg:           opts.Registry,
		pluginCtx:     pluginCtx,
		closeCtx:      context.Background(),
		closeCancel:   func() {}, // replaced by Close if ever needed; safe no-op default
		cpRoot:        opts.WorkspaceRoot,
		cfg:           opts.Config,
		approvals:     map[string]chan approvalReply{},
		asks:          map[string]chan []event.AskAnswer{},
		granted:       map[string]bool{},
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
		// MultiAgent completion: arm pending + idle wake (busy → queue empty reentry).
		// Same-turn flushes already drain mailbox; empty wake no-ops if nothing left.
		if ma := c.executor.MultiAgentControl(); ma != nil {
			ma.Sink = c.sink // agent_status: wire event sink for sub-agent lifecycle events
			ma.OnCompletion = func() {
				c.SetPendingToolResult(true)
				c.autoReenter()
			}
		}
	}
	// Sub-agents use multiagent mailbox for completion wakeups.
	return c
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

// MakeOnComplete is a no-op. Multiagent completion uses OnCompletion.
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
	c.mu.Lock()
	c.turnActive = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.turnActive = false
		c.mu.Unlock()
	}()

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

// MultiAgentControl returns the session-scoped multi-agent controller, if any.
func (c *Controller) MultiAgentControl() *multiagent.Control {
	if c.executor == nil {
		return nil
	}
	return c.executor.MultiAgentControl()
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
	c.mu.Lock()
	turnActive := c.turnActive
	c.mu.Unlock()
	if turnActive {
		return fmt.Errorf("cannot compact while a turn is running")
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
	turnActive := c.turnActive
	c.mu.Unlock()
	if turnActive {
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
