package multiagent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/event"
)

// Codex MultiAgentV2Config-ish defaults.
const (
	DefaultWaitTimeoutMs = 600_000
	MinWaitTimeoutMs     = 1_000
	MaxWaitTimeoutMs     = 3_600_000
	DefaultMaxConcurrent = 6
	DefaultMaxDepth      = 3
	// lastTaskListCap matches practical list readability; full text stays on the record
	// for debugging via LastTaskMessageRaw if needed — list returns capped copy like
	// a UI summary. Codex stores last_task_message for the instruction text; long
	// prompts are rare there. Cap prevents reasonix 32KiB tool truncation from
	// wiping agent_status entries (host constraint, not a new list schema).
	lastTaskListCap = 240
)

// Runner runs a spawned agent to completion (Codex child thread).
type Runner interface {
	Run(ctx context.Context, path, message string, depth int) (answer string, err error)
}

// Metadata is one live agent record (Codex AgentMetadata + live status).
type Metadata struct {
	Path            string
	Nickname        string
	Role            string
	LastTaskMessage string
	Status          Status
	LastAnswer      string
	LastError       string
	Depth           int
	StartedAt       time.Time
	cancel          context.CancelFunc
	mu              sync.Mutex
}

// Control is session-scoped AgentControl (one per root thread tree).
// Shared by root and all ThreadSpawn children via context.
type Control struct {
	mu            sync.Mutex
	agents        map[string]*Metadata // path -> metadata (live tree)
	byLeaf        map[string]string
	mailbox       *Mailbox
	runner        Runner
	maxConcurrent int
	maxDepth      int
	rootStatus    Status
	OnTriggerTurn func()
	OnCompletion  func()
	runningCount  atomic.Int32
	Sink          event.Sink // agent_status: event sink for sub-agent lifecycle events
}

// NewControl builds a root-session multi-agent controller (at most one per session).
func NewControl() *Control {
	return &Control{
		agents:        make(map[string]*Metadata),
		byLeaf:        make(map[string]string),
		mailbox:       NewMailbox(),
		maxConcurrent: DefaultMaxConcurrent,
		maxDepth:      DefaultMaxDepth,
		rootStatus:    StatusRunning,
	}
}

func (c *Control) SetRunner(r Runner) { c.runner = r }

func (c *Control) Mailbox() *Mailbox {
	if c == nil {
		return nil
	}
	return c.mailbox
}

// NotifySteer signals wait_agent (Codex InputQueueActivity::Steer).
func (c *Control) NotifySteer() {
	if c == nil || c.mailbox == nil {
		return
	}
	c.mailbox.NotifySteer()
}

// SetRootStatus updates the synthetic root list entry status.
func (c *Control) SetRootStatus(s Status) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.rootStatus = s
	c.mu.Unlock()
}

// Spawn starts a background agent (Codex spawn_agent).
func (c *Control) Spawn(ctx context.Context, parentPath, taskName, message string, parentDepth int) (taskPath, nickname string, err error) {
	if c == nil || c.runner == nil {
		return "", "", fmt.Errorf("multi-agent runner not configured")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", "", fmt.Errorf("message is required")
	}
	taskName = NormalizePathSegment(taskName)
	if parentPath == "" {
		parentPath = RootPath
	}
	childDepth := parentDepth + 1
	if childDepth > c.maxDepth {
		return "", "", fmt.Errorf("Agent depth limit reached. Solve the task yourself.")
	}
	if int(c.runningCount.Load()) >= c.maxConcurrent {
		return "", "", fmt.Errorf("too many concurrent agents (max %d)", c.maxConcurrent)
	}

	c.mu.Lock()
	path := JoinPath(parentPath, taskName)
	if _, exists := c.agents[path]; exists {
		path = JoinPath(parentPath, fmt.Sprintf("%s_%d", taskName, time.Now().UnixNano()%100000))
	}
	nick := LeafName(path)
	// nickname uniqueness (Codex used_agent_nicknames)
	base := nick
	for n := 2; c.leafTaken(nick); n++ {
		nick = fmt.Sprintf("%s_%d", base, n)
	}
	rec := &Metadata{
		Path:            path,
		Nickname:        nick,
		Role:            "task",
		LastTaskMessage: message,
		Status:          StatusPendingInit,
		Depth:           childDepth,
		StartedAt:       time.Now(),
	}
	c.agents[path] = rec
	c.byLeaf[nick] = path
	c.mu.Unlock()

	// Preserve spawn-call values (agent, options, store) without inheriting cancel;
	// child lifetime is its own cancel (Codex independent child thread).
	runBase := context.WithoutCancel(ctx)
	runCtx, cancel := context.WithCancel(runBase)
	rec.mu.Lock()
	rec.cancel = cancel
	rec.Status = StatusRunning // Codex TurnStarted → Running
	rec.mu.Unlock()
	c.runningCount.Add(1)

	go c.runAgent(runCtx, rec, path, message, childDepth)

	return path, nick, nil
}

// runAgent executes one agent turn and publishes terminal status + parent mail.
func (c *Control) runAgent(runCtx context.Context, rec *Metadata, path, message string, depth int) {
	defer c.runningCount.Add(-1)
	answer, runErr := c.runner.Run(runCtx, path, message, depth)
	rec.mu.Lock()
	switch {
	case runCtx.Err() != nil:
		rec.Status = StatusInterrupted
		rec.LastError = "interrupted"
	case runErr != nil:
		rec.Status = StatusErrored
		rec.LastError = runErr.Error()
		rec.LastAnswer = strings.TrimSpace(answer)
	default:
		rec.Status = StatusCompleted
		rec.LastAnswer = strings.TrimSpace(answer)
		rec.LastError = ""
	}
	status := rec.Status
	lastAns := rec.LastAnswer
	lastErr := rec.LastError
	rec.mu.Unlock()

	// agent_status: emit lifecycle event for terminal states
	if c.Sink != nil {
		c.Sink.Emit(event.Event{
			Kind: event.AgentStatus,
			AgentStatus: &event.AgentStatusData{
				AgentPath: path,
				Status:    string(status),
				Error:     lastErr,
				Timestamp: time.Now().UnixMilli(),
			},
		})
	}

	// Codex: forward completion to parent mailbox (trigger_turn=false).
	parent := ParentPath(path)
	if parent == "" {
		parent = RootPath
	}
	c.mailbox.Enqueue(Mail{
		From:        path,
		To:          parent,
		Message:     formatCompletionMessage(path, status, lastAns, lastErr),
		TriggerTurn: false,
	})
	if c.OnCompletion != nil {
		c.OnCompletion()
	}
}

func (c *Control) leafTaken(nick string) bool {
	_, ok := c.byLeaf[nick]
	return ok
}

func formatCompletionMessage(path string, status Status, answer, errMsg string) string {
	leaf := LeafName(path)
	switch status {
	case StatusCompleted:
		if answer == "" {
			answer = "(empty answer)"
		}
		return fmt.Sprintf("[agent_complete path=%s name=%s status=completed]\n%s", path, leaf, answer)
	case StatusErrored:
		return fmt.Sprintf("[agent_complete path=%s name=%s status=errored]\n%v\n%s", path, leaf, errMsg, answer)
	case StatusInterrupted:
		return fmt.Sprintf("[agent_complete path=%s name=%s status=interrupted]", path, leaf)
	default:
		return fmt.Sprintf("[agent_complete path=%s name=%s status=%s]", path, leaf, status)
	}
}

// ResolveTarget maps target string to full path (leaf or canonical).
func (c *Control) ResolveTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("target is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if rec, ok := c.agents[target]; ok {
		return rec.Path, nil
	}
	if strings.HasPrefix(target, "/") {
		if rec, ok := c.agents[strings.TrimSuffix(target, "/")]; ok {
			return rec.Path, nil
		}
		return "", fmt.Errorf("agent %q not found", target)
	}
	seg := NormalizePathSegment(target)
	if full, ok := c.byLeaf[seg]; ok {
		return full, nil
	}
	for path := range c.agents {
		if LeafName(path) == seg || strings.HasSuffix(path, "/"+seg) {
			return path, nil
		}
	}
	return "", fmt.Errorf("agent %q not found", target)
}

// GetStatus returns live status for path.
func (c *Control) GetStatus(path string) (Status, string, string) {
	c.mu.Lock()
	rec := c.agents[path]
	root := c.rootStatus
	c.mu.Unlock()
	if path == RootPath || path == "" {
		return root, "", ""
	}
	if rec == nil {
		return StatusNotFound, "", ""
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.Status, rec.LastAnswer, rec.LastError
}

// ListedAgent matches Codex list_agents item schema.
type ListedAgent struct {
	AgentName       string `json:"agent_name"`
	AgentStatus     any    `json:"agent_status"`
	LastTaskMessage any    `json:"last_task_message"` // string or null
	StartedAt       time.Time `json:"-"`
}

// List returns live agents like Codex list_agents (root + tree, sorted by path).
func (c *Control) List(currentPath, pathPrefix string) []ListedAgent {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	resolved := ""
	if strings.TrimSpace(pathPrefix) != "" {
		resolved = ResolveRelative(currentPath, pathPrefix)
	}

	type row struct {
		path string
		rec  *Metadata
	}
	var rows []row
	for path, rec := range c.agents {
		if resolved != "" && path != resolved && !strings.HasPrefix(path, resolved+"/") {
			continue
		}
		rows = append(rows, row{path: path, rec: rec})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })

	out := make([]ListedAgent, 0, len(rows)+1)
	// Root when it matches prefix (session itself is always "live").
	if resolved == "" || resolved == RootPath {
		out = append(out, ListedAgent{
			AgentName:       RootPath,
			AgentStatus:     StatusJSON(c.rootStatus, "", ""),
			LastTaskMessage: "Current user session",
		})
	}
	for _, r := range rows {
		r.rec.mu.Lock()
		startedAt := r.rec.StartedAt
		st := r.rec.Status
		last := r.rec.LastTaskMessage
		r.rec.mu.Unlock()
		// List is live-only: pending_init + running. Interrupted/terminal omit.
		// Registry still keeps records for followup/interrupt; results via mailbox.
		if !IsListLive(st) {
			continue
		}
		var lastMsg any
		if last == "" {
			lastMsg = nil
		} else {
			lastMsg = capRunes(last, lastTaskListCap)
		}
		out = append(out, ListedAgent{
			AgentName:       r.path,
			AgentStatus:     StatusJSON(st, "", ""),
			LastTaskMessage: lastMsg,
			StartedAt:       startedAt,
		})
	}
	return out
}

func capRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Interrupt cancels a running agent (Codex interrupt_agent).
func (c *Control) Interrupt(target string) (previous any, err error) {
	path, err := c.ResolveTarget(target)
	if err != nil {
		return nil, err
	}
	if path == RootPath || path == "" {
		return nil, fmt.Errorf("root is not a spawned agent")
	}
	c.mu.Lock()
	rec := c.agents[path]
	c.mu.Unlock()
	if rec == nil {
		return StatusJSON(StatusNotFound, "", ""), nil
	}
	rec.mu.Lock()
	prev := StatusJSON(rec.Status, rec.LastAnswer, rec.LastError)
	if rec.cancel != nil {
		rec.cancel()
	}
	if !IsFinal(rec.Status) {
		rec.Status = StatusInterrupted
	}
	rec.mu.Unlock()
	return prev, nil
}

// SendMessage queues instruction (Codex send_message / followup_task).
func (c *Control) SendMessage(fromPath, target, message string, triggerTurn bool) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("Empty message can't be sent to an agent")
	}
	path, err := c.ResolveTarget(target)
	if err != nil {
		return err
	}
	if fromPath == "" {
		fromPath = RootPath
	}
	c.mu.Lock()
	rec := c.agents[path]
	c.mu.Unlock()
	if rec == nil {
		return fmt.Errorf("agent %q not found", target)
	}
	rec.mu.Lock()
	rec.LastTaskMessage = message
	st := rec.Status
	depth := rec.Depth
	rec.mu.Unlock()

	c.mailbox.Enqueue(Mail{
		From:        fromPath,
		To:          path,
		Message:     message,
		TriggerTurn: triggerTurn,
	})
	if triggerTurn && c.OnTriggerTurn != nil {
		c.OnTriggerTurn()
	}

	// followup on idle/terminal: start a new Run (Codex followup triggers turn).
	if triggerTurn && (st == StatusCompleted || st == StatusErrored || st == StatusInterrupted || st == StatusShutdown) {
		if c.runner == nil {
			return nil
		}
		if depth < 1 {
			depth = 1
		}
		runCtx, cancel := context.WithCancel(context.Background())
		rec.mu.Lock()
		rec.cancel = cancel
		rec.Status = StatusRunning
		rec.mu.Unlock()
		c.runningCount.Add(1)
		go c.runAgent(runCtx, rec, path, message, depth)
	}
	return nil
}

// Wait blocks until mailbox activity, steer, or timeout (Codex wait_agent v2).
func (c *Control) Wait(ctx context.Context, timeoutMs int64) (message string, timedOut bool) {
	if c == nil || c.mailbox == nil {
		return "Wait timed out.", true
	}
	if timeoutMs <= 0 {
		timeoutMs = DefaultWaitTimeoutMs
	}
	if timeoutMs < MinWaitTimeoutMs {
		timeoutMs = MinWaitTimeoutMs
	}
	if timeoutMs > MaxWaitTimeoutMs {
		timeoutMs = MaxWaitTimeoutMs
	}

	ch, pending, unsub := c.mailbox.Subscribe()
	defer unsub()
	if pending != nil {
		if *pending == ActivitySteer {
			return "Wait interrupted by new input.", false
		}
		return "Wait completed.", false
	}

	timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "Wait interrupted by new input.", false
	case a := <-ch:
		if a == ActivitySteer {
			return "Wait interrupted by new input.", false
		}
		return "Wait completed.", false
	case <-timer.C:
		if c.mailbox.HasPending() {
			return "Wait completed.", false
		}
		return "Wait timed out.", true
	}
}

// FormatMailsForSession renders drained mails for the parent model.
func FormatMailsForSession(mails []Mail) string {
	if len(mails) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[multi_agent_mailbox]\n")
	for i, m := range mails {
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fmt.Fprintf(&b, "from=%s to=%s\n%s", m.From, m.To, m.Message)
	}
	b.WriteString("\n[/multi_agent_mailbox]")
	return b.String()
}

// LiveCount returns non-root agents still tracked.
func (c *Control) LiveCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.agents)
}
