package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/event"
)

// Multi-agent control defaults (no wait timeout — wait blocks until done or steer).
const (
	DefaultMaxConcurrent = 6
	DefaultMaxDepth      = 3
	// spawnInterruptCooldown blocks interrupt→immediate re-spawn of the same path
	// (the expensive kill-and-retry anti-pattern). Completed/errored agents can be
	// re-spawned by reusing the path after registry cleanup.
	spawnInterruptCooldown = 5 * time.Minute
	// spawnGoalCooldown blocks re-delegating the same goal (message fingerprint)
	// even under a new task_name — kills burn-money re-spawn loops.
	spawnGoalCooldown = 15 * time.Minute
	// lastTaskListCap matches practical list readability; full text stays on the record
	// for debugging via LastTaskMessageRaw if needed — list returns capped copy like
	// a UI summary. Codex stores last_task_message for the instruction text; long
	// prompts are rare there. Cap prevents reasonix 32KiB tool truncation from
	// wiping agent_status entries (host constraint, not a new list schema).
	lastTaskListCap = 240
)

// WaitResult is the wait_agent payload: block until the whole batch is done (or steer).
type WaitResult struct {
	Message     string         `json:"message"`
	Interrupted bool           `json:"interrupted,omitempty"`
	Results     string         `json:"results,omitempty"`
	MailCount   int            `json:"mail_count"`
	LiveAgents  []LiveSnapshot `json:"live_agents,omitempty"`
	Next        string         `json:"next,omitempty"`
}

// LiveSnapshot is a compact live-agent row for wait/list.
type LiveSnapshot struct {
	AgentName   string `json:"agent_name"`
	AgentStatus string `json:"agent_status"`
	ElapsedMs   int64  `json:"elapsed_ms"`
}

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
	FinishedAt      time.Time // set when entering a terminal status
	cancel          context.CancelFunc
	mu              sync.Mutex
}

// Control is session-scoped AgentControl (one per root thread tree).
// Shared by root and all ThreadSpawn children via context.
type Control struct {
	mu            sync.Mutex
	agents        map[string]*Metadata // path -> metadata (live tree)
	byLeaf        map[string]string
	// recentGoals: goal fingerprint → last spawn time (live or completed).
	// Prevents burn-money re-spawn of the same work under a new task_name.
	recentGoals   map[string]time.Time
	runner        Runner
	mailbox       *Mailbox
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
		recentGoals:   make(map[string]time.Time),
		mailbox:       NewMailbox(),
		maxConcurrent: DefaultMaxConcurrent,
		maxDepth:      DefaultMaxDepth,
		rootStatus:    StatusRunning,
	}
}

// goalURLRe extracts fetch targets so two prompts with the same curl URL
// fingerprint as one goal even when task_name/wording differ.
var goalURLRe = regexp.MustCompile(`(?i)(?:https?://[^\s'"\\]+|wttr\.in/[^\s'"\\]+)`)

// goalFingerprint derives a stable key for "same delegated work".
// Prefer URLs / host paths in the message; else hash normalized text.
func goalFingerprint(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	urls := goalURLRe.FindAllString(message, -1)
	var core string
	if len(urls) > 0 {
		// strip query noise that doesn't change intent? keep full URL — format=3 vs j1 differ
		sort.Strings(urls)
		core = strings.Join(urls, "|")
	} else {
		core = strings.ToLower(message)
		core = strings.Join(strings.Fields(core), " ")
		if len(core) > 400 {
			core = core[:400]
		}
	}
	sum := sha256.Sum256([]byte(core))
	return hex.EncodeToString(sum[:16])
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

// Spawn starts a background agent.
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

	fp := goalFingerprint(message)
	c.mu.Lock()
	// Same goal under cooldown (including different task_name).
	if fp != "" {
		if t, ok := c.recentGoals[fp]; ok && time.Since(t) < spawnGoalCooldown {
			// Still allow if no live agent and user substantially changed URLs
			// (fingerprint differs). Here same fp within window → refuse.
			c.mu.Unlock()
			return "", "", fmt.Errorf("same goal was already delegated recently; call wait_agent for results, use followup_task to redirect, or change the work substantially (not just the task name)")
		}
		// Live agent already running this goal text.
		for _, rec := range c.agents {
			rec.mu.Lock()
			live := IsListLive(rec.Status)
			msg := rec.LastTaskMessage
			rec.mu.Unlock()
			if live && goalFingerprint(msg) == fp {
				c.mu.Unlock()
				return "", "", fmt.Errorf("same goal is already running as %q; call wait_agent instead of spawn_agent again", rec.Path)
			}
		}
	}
	path := JoinPath(parentPath, taskName)
	if err := c.prepareSpawnPathLocked(path); err != nil {
		c.mu.Unlock()
		return "", "", err
	}
	if fp != "" {
		if c.recentGoals == nil {
			c.recentGoals = make(map[string]time.Time)
		}
		c.recentGoals[fp] = time.Now()
	}
	nick := LeafName(path)
	// nickname uniqueness
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

	// Preserve spawn-call values without inheriting cancel; child has its own cancel.
	runBase := context.WithoutCancel(ctx)
	runCtx, cancel := context.WithCancel(runBase)
	rec.mu.Lock()
	rec.cancel = cancel
	rec.Status = StatusRunning
	rec.mu.Unlock()
	c.runningCount.Add(1)

	go c.runAgent(runCtx, rec, path, message, childDepth)

	return path, nick, nil
}

// prepareSpawnPathLocked enforces no live duplicate and no interrupt→re-spawn churn.
// Caller holds c.mu. On success, path is free for a new Metadata entry.
func (c *Control) prepareSpawnPathLocked(path string) error {
	rec, exists := c.agents[path]
	if !exists {
		return nil
	}
	rec.mu.Lock()
	st := rec.Status
	finished := rec.FinishedAt
	oldNick := rec.Nickname
	rec.mu.Unlock()
	if IsListLive(st) {
		return fmt.Errorf("agent %q is still running; call wait_agent (interrupt only if the task is wrong, not because it is slow)", path)
	}
	if st == StatusInterrupted && !finished.IsZero() && time.Since(finished) < spawnInterruptCooldown {
		return fmt.Errorf("agent %q was just interrupted; use followup_task instead of spawn_agent for the same work", path)
	}
	// Completed / errored / old interrupt: drop registry so the canonical path can be reused.
	delete(c.agents, path)
	if c.byLeaf[oldNick] == path {
		delete(c.byLeaf, oldNick)
	}
	return nil
}

// runAgent executes one agent turn and publishes terminal status + parent mail.
func (c *Control) runAgent(runCtx context.Context, rec *Metadata, path, message string, depth int) {
	defer c.runningCount.Add(-1)
	answer, runErr := c.runner.Run(runCtx, path, message, depth)

	var status Status
	var lastAns, lastErr string
	rec.mu.Lock()
	switch {
	case runCtx.Err() != nil:
		status = StatusInterrupted
		lastErr = "interrupted"
	case runErr != nil:
		status = StatusErrored
		lastErr = runErr.Error()
		lastAns = strings.TrimSpace(answer)
	default:
		status = StatusCompleted
		lastAns = strings.TrimSpace(answer)
		lastErr = ""
	}
	rec.Status = status
	rec.LastAnswer = lastAns
	rec.LastError = lastErr
	rec.FinishedAt = time.Now()
	rec.mu.Unlock()

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

	// Status is terminal first, then mail — WaitFor treats "saw live, now zero, no mail yet"
	// as enqueue race and keeps blocking on the mailbox signal (no wall-clock timeout).
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
	AgentName       string    `json:"agent_name"`
	AgentStatus     any       `json:"agent_status"`
	LastTaskMessage any       `json:"last_task_message"` // string or null
	ElapsedMs       int64     `json:"elapsed_ms,omitempty"`
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
		var elapsed int64
		if !startedAt.IsZero() {
			elapsed = time.Since(startedAt).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
		}
		out = append(out, ListedAgent{
			AgentName:       r.path,
			AgentStatus:     StatusJSON(st, "", ""),
			LastTaskMessage: lastMsg,
			ElapsedMs:       elapsed,
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
		rec.FinishedAt = time.Now()
		rec.LastError = "interrupted"
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

// Wait blocks at RootPath until the batch is done or the user steers.
func (c *Control) Wait(ctx context.Context) WaitResult {
	return c.WaitFor(ctx, RootPath)
}

// WaitFor blocks with no deadline until every live agent under forPath has
// finished (and all mail to forPath has been taken), or until user steer / ctx cancel.
// Parallel children: one wait collects every completion for forPath in Results.
// There is no timeout — a stuck wait costs no model tokens; steer wakes it.
func (c *Control) WaitFor(ctx context.Context, forPath string) WaitResult {
	if forPath == "" {
		forPath = RootPath
	}
	if c == nil || c.mailbox == nil {
		return WaitResult{
			Message: "Wait unavailable.",
			Next:    "multi-agent control unavailable",
		}
	}

	var taken []Mail
	take := func() {
		if batch := c.mailbox.DrainFor(forPath); len(batch) > 0 {
			taken = append(taken, batch...)
		}
	}

	finish := func(msg string, interrupted bool) WaitResult {
		take()
		live := c.liveUnderSnapshot(forPath)
		res := WaitResult{
			Message:     msg,
			Interrupted: interrupted,
			Results:     FormatMailsForSession(taken),
			MailCount:   len(taken),
			LiveAgents:  live,
		}
		if interrupted {
			res.Next = "Interrupted by user or cancel. Process results so far; remaining live agents keep running unless you interrupt them."
		} else if len(taken) == 0 {
			res.Next = "Nothing to collect. Spawn work or continue locally."
		} else {
			res.Next = "All batch results are in results. Do not list_agents or re-spawn for the same work."
		}
		return res
	}

	ch, _, unsub := c.mailbox.SubscribeFor(forPath)
	defer unsub()

	sawLive := false
	for {
		take()
		live := c.liveUnderCount(forPath)
		if live > 0 {
			sawLive = true
		}
		pending := c.mailbox.HasPendingFor(forPath)

		// Batch complete: no live descendants and no mail left to take.
		if live == 0 && !pending {
			if len(taken) > 0 || !sawLive {
				return finish("Wait completed.", false)
			}
			// Saw live agents go to zero but mail not visible yet (status→enqueue race):
			// block on the next mailbox signal only — still no wall-clock timeout.
			select {
			case <-ctx.Done():
				return finish("Wait interrupted by cancel.", true)
			case a := <-ch:
				if a == ActivitySteer {
					return finish("Wait interrupted by new input.", true)
				}
			}
			continue
		}

		if live == 0 && pending {
			// More mail to drain on next iteration.
			continue
		}

		// Still have live agents — wait for mail, steer, or cancel.
		select {
		case <-ctx.Done():
			return finish("Wait interrupted by cancel.", true)
		case a := <-ch:
			if a == ActivitySteer {
				return finish("Wait interrupted by new input.", true)
			}
		}
	}
}

// liveUnderCount counts live agents in the subtree of forPath (not including forPath itself).
func (c *Control) liveUnderCount(forPath string) int {
	return len(c.liveUnderSnapshot(forPath))
}

// liveUnderSnapshot returns live agents under forPath (descendants only).
func (c *Control) liveUnderSnapshot(forPath string) []LiveSnapshot {
	if c == nil {
		return nil
	}
	forPath = strings.TrimSuffix(strings.TrimSpace(forPath), "/")
	if forPath == "" {
		forPath = RootPath
	}
	prefix := forPath + "/"

	c.mu.Lock()
	defer c.mu.Unlock()
	type row struct {
		path string
		rec  *Metadata
	}
	var rows []row
	for path, rec := range c.agents {
		if path == forPath || strings.HasPrefix(path, prefix) {
			// Waiter's own path is not a child; only descendants.
			if path == forPath {
				continue
			}
			rows = append(rows, row{path: path, rec: rec})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	var out []LiveSnapshot
	now := time.Now()
	for _, r := range rows {
		r.rec.mu.Lock()
		st := r.rec.Status
		started := r.rec.StartedAt
		r.rec.mu.Unlock()
		if !IsListLive(st) {
			continue
		}
		var elapsed int64
		if !started.IsZero() {
			elapsed = now.Sub(started).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
		}
		out = append(out, LiveSnapshot{
			AgentName:   r.path,
			AgentStatus: string(st),
			ElapsedMs:   elapsed,
		})
	}
	return out
}

// liveChildrenSnapshot returns all live non-root agents (for list-style diagnostics).
func (c *Control) liveChildrenSnapshot() []LiveSnapshot {
	return c.liveUnderSnapshot(RootPath)
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
