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

// Multi-agent control defaults (Codex V1 lifecycle; one spawn layer only).
const (
	DefaultMaxConcurrent = 6
	// DefaultMaxDepth: only root may spawn (no nested agents).
	DefaultMaxDepth = 1
	lastTaskListCap = 240
	// How long interrupt/close waits for the current turn to exit.
	interruptSettle = 30 * time.Second
	// Wait timeouts (ms), Codex-style bounds.
	DefaultWaitTimeoutMs = 600_000 // 10 minutes
	MinWaitTimeoutMs     = 1_000
	MaxWaitTimeoutMs     = 3_600_000 // 1 hour
)

// SessionDropper is implemented by MultiAgentRunner to drop persisted sub-agent sessions.
type SessionDropper interface {
	DropSession(path string)
}

// SessionKeeper persists closed-agent sessions for resume across process restarts.
type SessionKeeper interface {
	SessionDropper
	SaveSession(path string) error
	LoadSession(path string) error
	HasPersistedSession(path string) bool
}

// Steerer soft-queues a message into a running agent turn (send_input without interrupt).
type Steerer interface {
	Steer(path, message string) bool
}

// WaitResult is the wait_agent payload.
type WaitResult struct {
	Message     string         `json:"message"`
	Interrupted bool           `json:"interrupted,omitempty"`
	TimedOut    bool           `json:"timed_out,omitempty"`
	Results     string         `json:"results,omitempty"`
	MailCount   int            `json:"mail_count"`
	LiveAgents  []LiveSnapshot `json:"live_agents,omitempty"`
	Next        string         `json:"next,omitempty"`
}

// LiveSnapshot is a compact live-agent row for wait/list.
type LiveSnapshot struct {
	AgentName      string `json:"agent_name"`
	AgentStatus    string `json:"agent_status"`
	ElapsedMs      int64  `json:"elapsed_ms"`
	CurrentTool    string `json:"current_tool,omitempty"`
	LastActivityMs int64  `json:"last_activity_ms,omitempty"`
}

// Runner runs a spawned agent turn (Codex child thread). Same path reuses session.
type Runner interface {
	Run(ctx context.Context, path, message string) (answer string, err error)
}

// Metadata is one open agent record (Codex AgentMetadata + live status).
type Metadata struct {
	Path            string
	Nickname        string
	Role            string
	LastTaskMessage string
	Status          Status
	LastAnswer      string
	LastError       string
	StartedAt       time.Time
	FinishedAt      time.Time
	CurrentTool     string
	ToolCallCount   int
	LastActivityAt  time.Time
	cancel          context.CancelFunc
	turnDone        chan struct{} // closed when the current turn exits
	mu              sync.Mutex
}

// StartTool records that a tool call is beginning (diagnostic metadata).
func (m *Metadata) StartTool(name string) {
	m.mu.Lock()
	m.CurrentTool = name
	m.ToolCallCount++
	m.LastActivityAt = time.Now()
	m.mu.Unlock()
}

// EndTool records that the current tool call has finished.
func (m *Metadata) EndTool() {
	m.mu.Lock()
	m.CurrentTool = ""
	m.LastActivityAt = time.Now()
	m.mu.Unlock()
}

// Control is session-scoped multi-agent controller (one per root session).
type Control struct {
	mu     sync.Mutex
	agents map[string]*Metadata // open agents (count toward concurrency)
	closed map[string]*Metadata // closed but resumable (Codex resume_agent)
	byLeaf map[string]string
	runner Runner
	mailbox       *Mailbox
	maxConcurrent int
	rootStatus    Status
	OnTriggerTurn func()
	OnCompletion  func()
	// openCount: agents still open (Codex total_count until close_agent).
	openCount atomic.Int32
	// runningCount: turns currently in flight.
	runningCount atomic.Int32
	Sink         event.Sink
}

// NewControl builds a root-session multi-agent controller.
func NewControl() *Control {
	return &Control{
		agents:        make(map[string]*Metadata),
		closed:        make(map[string]*Metadata),
		byLeaf:        make(map[string]string),
		mailbox:       NewMailbox(),
		maxConcurrent: DefaultMaxConcurrent,
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

// Spawn starts a background agent (new thread + first turn).
// Only the root agent may spawn (DefaultMaxDepth = 1; no nesting).
func (c *Control) Spawn(ctx context.Context, parentPath, taskName, message string) (taskPath, nickname string, err error) {
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
	// Hard one-layer gate (product rule: no nested agents, ever).
	// 1) Only root may be the spawn parent.
	// 2) New path must be exactly depth 1 (/root/<name>), never /root/a/b.
	if !IsRootAgentPath(parentPath) {
		return "", "", fmt.Errorf("Agent depth limit reached. Solve the task yourself.")
	}
	if int(c.openCount.Load()) >= c.maxConcurrent {
		return "", "", fmt.Errorf("too many open agents (max %d); close_agent finished agents to free slots", c.maxConcurrent)
	}

	c.mu.Lock()
	// Always attach under root — ignore any non-root parentPath that slipped through.
	path := JoinPath(RootPath, taskName)
	if PathDepth(path) != 1 {
		c.mu.Unlock()
		return "", "", fmt.Errorf("Agent depth limit reached. Solve the task yourself.")
	}
	if err := c.prepareSpawnPathLocked(path); err != nil {
		c.mu.Unlock()
		return "", "", err
	}
	nick := LeafName(path)
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
		StartedAt:       time.Now(),
	}
	c.agents[path] = rec
	c.byLeaf[nick] = path
	c.mu.Unlock()
	c.openCount.Add(1)

	if err := c.startTurn(ctx, rec, path, message); err != nil {
		// Roll back registry so a failed start does not leak a slot.
		c.mu.Lock()
		delete(c.agents, path)
		if c.byLeaf[nick] == path {
			delete(c.byLeaf, nick)
		}
		c.mu.Unlock()
		if c.openCount.Load() > 0 {
			c.openCount.Add(-1)
		}
		return "", "", err
	}
	return path, nick, nil
}

// prepareSpawnPathLocked: path free, or discard a previously closed agent at path.
func (c *Control) prepareSpawnPathLocked(path string) error {
	if rec, exists := c.agents[path]; exists {
		rec.mu.Lock()
		st := rec.Status
		oldNick := rec.Nickname
		rec.mu.Unlock()
		if IsOpen(st) {
			if st == StatusRunning {
				return fmt.Errorf("agent %q is still running; call wait_agent or send_input(interrupt=true)", path)
			}
			return fmt.Errorf("agent %q is still open (status=%s); use send_input to continue or close_agent first", path, st)
		}
		delete(c.agents, path)
		if c.byLeaf[oldNick] == path {
			delete(c.byLeaf, oldNick)
		}
	}
	// Spawning over a closed path discards resume state (fresh agent).
	if old, ok := c.closed[path]; ok {
		delete(c.closed, old.Path)
		if c.byLeaf[old.Nickname] == path {
			delete(c.byLeaf, old.Nickname)
		}
		if d, ok := c.runner.(SessionDropper); ok {
			d.DropSession(path)
		}
	}
	return nil
}

// startTurn begins a single turn. Fails if a turn is already active on rec.
func (c *Control) startTurn(parentCtx context.Context, rec *Metadata, path, message string) error {
	runBase := parentCtx
	if runBase == nil {
		runBase = context.Background()
	}
	runBase = context.WithoutCancel(runBase)
	runCtx, cancel := context.WithCancel(runBase)

	rec.mu.Lock()
	// Only Running is a true in-flight turn. pending_init is the spawn pre-start
	// state; completed/interrupted/errored are idle-open and may start a new turn.
	if rec.Status == StatusRunning {
		rec.mu.Unlock()
		cancel()
		return fmt.Errorf("agent %q already has an active turn", path)
	}
	if rec.Status == StatusShutdown {
		rec.mu.Unlock()
		cancel()
		return fmt.Errorf("agent %q is closed", path)
	}
	done := make(chan struct{})
	rec.turnDone = done
	rec.cancel = cancel
	rec.Status = StatusRunning
	rec.LastTaskMessage = message
	rec.LastError = ""
	rec.FinishedAt = time.Time{}
	rec.mu.Unlock()

	c.runningCount.Add(1)
	go c.runAgent(runCtx, rec, path, message, done)
	return nil
}

// runAgent executes one agent turn; session persistence is the Runner's job.
func (c *Control) runAgent(runCtx context.Context, rec *Metadata, path, message string, done chan struct{}) {
	defer func() {
		c.runningCount.Add(-1)
		if done != nil {
			close(done)
		}
		rec.mu.Lock()
		if rec.turnDone == done {
			rec.turnDone = nil
		}
		rec.mu.Unlock()
	}()

	answer, runErr := c.runner.Run(runCtx, path, message)

	var status Status
	var lastAns, lastErr string
	rec.mu.Lock()
	// If CloseAgent already shut us down, don't overwrite status (session drop is owner's job).
	if rec.Status == StatusShutdown {
		rec.cancel = nil
		rec.mu.Unlock()
		return
	}
	switch {
	case runCtx.Err() != nil:
		status = StatusInterrupted
		lastErr = "interrupted"
		lastAns = strings.TrimSpace(answer)
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
	rec.cancel = nil
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
		return fmt.Sprintf("[agent_complete path=%s name=%s status=interrupted]\nAgent remains open — use send_input to continue or close_agent to free the slot.", path, leaf)
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

// Meta returns the Metadata pointer for path, or nil.
func (c *Control) Meta(path string) *Metadata {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agents[path]
}

// ListedAgent matches Codex list_agents item schema.
type ListedAgent struct {
	AgentName       string    `json:"agent_name"`
	AgentStatus     any       `json:"agent_status"`
	LastTaskMessage any       `json:"last_task_message"`
	ElapsedMs       int64     `json:"elapsed_ms,omitempty"`
	CurrentTool     string    `json:"current_tool,omitempty"`
	ToolCallCount   int       `json:"tool_call_count,omitempty"`
	LastActivityMs  int64     `json:"last_activity_ms,omitempty"`
	StartedAt       time.Time `json:"-"`
}

// List returns open agents (root + tree).
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
		lastAns := r.rec.LastAnswer
		lastErr := r.rec.LastError
		currentTool := r.rec.CurrentTool
		toolCallCount := r.rec.ToolCallCount
		lastActivityAt := r.rec.LastActivityAt
		r.rec.mu.Unlock()
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
		var lastActivityMs int64
		ref := lastActivityAt
		if ref.IsZero() {
			ref = startedAt
		}
		if !ref.IsZero() {
			lastActivityMs = time.Since(ref).Milliseconds()
			if lastActivityMs < 0 {
				lastActivityMs = 0
			}
		}
		out = append(out, ListedAgent{
			AgentName:       r.path,
			AgentStatus:     StatusJSON(st, lastAns, lastErr),
			LastTaskMessage: lastMsg,
			ElapsedMs:       elapsed,
			CurrentTool:     currentTool,
			ToolCallCount:   toolCallCount,
			LastActivityMs:  lastActivityMs,
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

// Interrupt soft-cancels the current turn. Agent stays open (Codex).
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
	rec.mu.Unlock()
	return prev, nil
}

// waitTurnExit waits until the current turn's done channel is closed (or timeout).
func (c *Control) waitTurnExit(rec *Metadata, timeout time.Duration) bool {
	if rec == nil {
		return true
	}
	rec.mu.Lock()
	done := rec.turnDone
	rec.mu.Unlock()
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// SendInput delivers a message on an open agent thread (Codex send_input).
// interrupt=true cancels the current turn first, then starts a new turn with message.
// interrupt=false while running soft-queues into the live turn when possible.
func (c *Control) SendInput(target, message string, interrupt bool) (path string, err error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	path, err = c.ResolveTarget(target)
	if err != nil {
		return "", err
	}
	if path == RootPath || path == "" {
		return "", fmt.Errorf("root is not a spawned agent")
	}
	if c.runner == nil {
		return "", fmt.Errorf("multi-agent runner not configured")
	}

	c.mu.Lock()
	rec := c.agents[path]
	c.mu.Unlock()
	if rec == nil {
		return "", fmt.Errorf("agent %q not found", target)
	}

	rec.mu.Lock()
	st := rec.Status
	rec.mu.Unlock()
	if st == StatusShutdown {
		return "", fmt.Errorf("agent %q is closed; spawn_agent a new one", path)
	}

	if interrupt && IsTurnActive(st) {
		_, _ = c.Interrupt(path)
		if !c.waitTurnExit(rec, interruptSettle) {
			return "", fmt.Errorf("agent %q did not stop in time after interrupt", path)
		}
		rec.mu.Lock()
		st = rec.Status
		rec.mu.Unlock()
	}

	if IsTurnActive(st) && !interrupt {
		if s, ok := c.runner.(Steerer); ok && s.Steer(path, message) {
			rec.mu.Lock()
			rec.LastTaskMessage = message
			rec.mu.Unlock()
			return path, nil
		}
		return "", fmt.Errorf("agent %q is still running and soft-queue failed; set interrupt=true or wait_agent then send_input", path)
	}

	if err := c.startTurn(context.Background(), rec, path, message); err != nil {
		return "", err
	}
	return path, nil
}

// CloseAgent shuts down an agent and frees its concurrency slot (Codex close_agent).
// Session is kept for resume_agent; only a later re-spawn at the same path drops it.
func (c *Control) CloseAgent(target string) (previous any, path string, err error) {
	path, err = c.ResolveTarget(target)
	if err != nil {
		return nil, "", err
	}
	if path == RootPath || path == "" {
		return nil, "", fmt.Errorf("root is not a spawned agent")
	}
	c.mu.Lock()
	rec := c.agents[path]
	if rec == nil {
		c.mu.Unlock()
		return StatusJSON(StatusNotFound, "", ""), path, nil
	}
	rec.mu.Lock()
	prev := StatusJSON(rec.Status, rec.LastAnswer, rec.LastError)
	if rec.cancel != nil {
		rec.cancel()
	}
	rec.Status = StatusShutdown
	rec.FinishedAt = time.Now()
	rec.LastError = "shutdown"
	nick := rec.Nickname
	rec.mu.Unlock()
	c.mu.Unlock()

	_ = c.waitTurnExit(rec, interruptSettle)

	c.mu.Lock()
	if cur, ok := c.agents[path]; ok && cur == rec {
		delete(c.agents, path)
		// Keep byLeaf so resume/resolve by nickname still works for closed agents.
		if c.closed == nil {
			c.closed = make(map[string]*Metadata)
		}
		c.closed[path] = rec
		_ = nick
	}
	c.mu.Unlock()

	if c.openCount.Load() > 0 {
		c.openCount.Add(-1)
	}
	// Persist session for resume (process restart safe when SessionKeeper is set).
	if k, ok := c.runner.(SessionKeeper); ok {
		_ = k.SaveSession(path)
	}
	if c.mailbox != nil {
		c.mailbox.NotifySteer()
	}
	return prev, path, nil
}

// ResumeAgent reopens a closed agent so it can receive send_input / wait_agent (Codex resume_agent).
func (c *Control) ResumeAgent(target string) (status any, path string, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, "", fmt.Errorf("id is required")
	}
	if int(c.openCount.Load()) >= c.maxConcurrent {
		return nil, "", fmt.Errorf("too many open agents (max %d); close_agent others first", c.maxConcurrent)
	}

	c.mu.Lock()
	path, rec := c.lookupAnyLocked(target)
	if rec == nil {
		// Disk-only resume after process restart: target as path or leaf.
		path = c.resolvePersistedPathLocked(target)
		c.mu.Unlock()
		if path == "" {
			return StatusJSON(StatusNotFound, "", ""), "", fmt.Errorf("agent %q not found", target)
		}
		if k, ok := c.runner.(SessionKeeper); ok && k.HasPersistedSession(path) {
			_ = k.LoadSession(path)
			rec = &Metadata{
				Path:     path,
				Nickname: LeafName(path),
				Role:     "task",
				Status:   StatusCompleted,
			}
			c.mu.Lock()
			if c.agents == nil {
				c.agents = make(map[string]*Metadata)
			}
			if c.byLeaf == nil {
				c.byLeaf = make(map[string]string)
			}
			c.agents[path] = rec
			c.byLeaf[rec.Nickname] = path
			c.mu.Unlock()
			c.openCount.Add(1)
			return StatusJSON(StatusCompleted, "", ""), path, nil
		}
		return StatusJSON(StatusNotFound, "", ""), path, fmt.Errorf("agent %q not found", target)
	}
	// Already open: report status (Codex returns current status).
	if _, open := c.agents[path]; open {
		rec.mu.Lock()
		st := StatusJSON(rec.Status, rec.LastAnswer, rec.LastError)
		rec.mu.Unlock()
		c.mu.Unlock()
		return st, path, nil
	}
	// Must be in closed map.
	if _, ok := c.closed[path]; !ok {
		c.mu.Unlock()
		return StatusJSON(StatusNotFound, "", ""), path, fmt.Errorf("agent %q is not resumable", path)
	}
	delete(c.closed, path)
	rec.mu.Lock()
	// Idle-open after resume: not running until send_input.
	rec.Status = StatusCompleted
	if rec.LastAnswer == "" && rec.LastError == "shutdown" {
		rec.LastError = ""
	}
	rec.cancel = nil
	rec.turnDone = nil
	st := StatusJSON(rec.Status, rec.LastAnswer, rec.LastError)
	rec.mu.Unlock()
	c.agents[path] = rec
	c.byLeaf[rec.Nickname] = path
	c.mu.Unlock()

	if k, ok := c.runner.(SessionKeeper); ok {
		_ = k.LoadSession(path)
	}

	c.openCount.Add(1)
	return st, path, nil
}

// resolvePersistedPathLocked maps target to a canonical path for disk resume.
func (c *Control) resolvePersistedPathLocked(target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimSuffix(target, "/")
	}
	seg := NormalizePathSegment(target)
	if full, ok := c.byLeaf[seg]; ok {
		return full
	}
	// Common form: leaf name only → /root/<leaf>
	return JoinPath(RootPath, seg)
}

// lookupAnyLocked finds open or closed agent by path/leaf. Caller holds c.mu.
func (c *Control) lookupAnyLocked(target string) (path string, rec *Metadata) {
	if rec, ok := c.agents[target]; ok {
		return rec.Path, rec
	}
	if rec, ok := c.closed[target]; ok {
		return rec.Path, rec
	}
	if strings.HasPrefix(target, "/") {
		t := strings.TrimSuffix(target, "/")
		if rec, ok := c.agents[t]; ok {
			return rec.Path, rec
		}
		if rec, ok := c.closed[t]; ok {
			return rec.Path, rec
		}
		return "", nil
	}
	seg := NormalizePathSegment(target)
	if full, ok := c.byLeaf[seg]; ok {
		if rec, ok := c.agents[full]; ok {
			return rec.Path, rec
		}
		if rec, ok := c.closed[full]; ok {
			return rec.Path, rec
		}
	}
	for p, r := range c.agents {
		if LeafName(p) == seg {
			return r.Path, r
		}
	}
	for p, r := range c.closed {
		if LeafName(p) == seg {
			return r.Path, r
		}
	}
	return "", nil
}

// Wait blocks at RootPath until the batch is done, the user steers, or the
// default wait timeout elapses.
func (c *Control) Wait(ctx context.Context) WaitResult {
	return c.WaitTimeout(ctx, DefaultWaitTimeoutMs)
}

// WaitTimeout is Wait with an explicit timeout in milliseconds (Codex wait_agent).
// timeoutMs <= 0 uses DefaultWaitTimeoutMs; values are clamped to [Min, Max].
func (c *Control) WaitTimeout(ctx context.Context, timeoutMs int64) WaitResult {
	if timeoutMs <= 0 {
		timeoutMs = DefaultWaitTimeoutMs
	}
	if timeoutMs < MinWaitTimeoutMs {
		timeoutMs = MinWaitTimeoutMs
	}
	if timeoutMs > MaxWaitTimeoutMs {
		timeoutMs = MaxWaitTimeoutMs
	}
	dctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	return c.WaitFor(dctx, RootPath)
}

// WaitFor blocks until every non-final descendant under forPath has reached a
// final status (completed/errored/shutdown) — Codex is_final: interrupted is NOT
// final, so wait keeps blocking after interrupt until send_input finishes or close.
// Context cancel / deadline ends the wait (deadline → timed_out).
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

	finish := func(msg string, interrupted, timedOut bool) WaitResult {
		take()
		live := c.pendingUnderSnapshot(forPath)
		res := WaitResult{
			Message:     msg,
			Interrupted: interrupted,
			TimedOut:    timedOut,
			Results:     FormatMailsForSession(taken),
			MailCount:   len(taken),
			LiveAgents:  live,
		}
		switch {
		case timedOut:
			res.Next = "Wait timed out before all agents finished. Call wait_agent again, send_input, or close_agent."
		case interrupted:
			res.Next = "Interrupted by user or cancel. Process results so far; remaining agents stay open until close_agent."
		case len(taken) == 0:
			res.Next = "Nothing to collect. Spawn work, send_input, or continue locally. Close finished agents with close_agent."
		default:
			res.Next = "Batch results are in results. Close finished agents with close_agent to free slots. Do not re-spawn the same work."
		}
		return res
	}

	ch, _, unsub := c.mailbox.SubscribeFor(forPath)
	defer unsub()

	sawPending := false
	for {
		take()
		pendingAgents := c.pendingUnderCount(forPath)
		if pendingAgents > 0 {
			sawPending = true
		}
		mailPending := c.mailbox.HasPendingFor(forPath)

		// Batch done: no non-final agents and no mail left.
		if pendingAgents == 0 && !mailPending {
			if len(taken) > 0 || !sawPending {
				return finish("Wait completed.", false, false)
			}
			select {
			case <-ctx.Done():
				if ctx.Err() == context.DeadlineExceeded {
					return finish("Wait timed out.", false, true)
				}
				return finish("Wait interrupted by cancel.", true, false)
			case a := <-ch:
				if a == ActivitySteer {
					return finish("Wait interrupted by new input.", true, false)
				}
			}
			continue
		}

		if pendingAgents == 0 && mailPending {
			continue
		}

		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return finish("Wait timed out.", false, true)
			}
			return finish("Wait interrupted by cancel.", true, false)
		case a := <-ch:
			if a == ActivitySteer {
				return finish("Wait interrupted by new input.", true, false)
			}
		}
	}
}

func (c *Control) pendingUnderCount(forPath string) int {
	return len(c.pendingUnderSnapshot(forPath))
}

// activeUnderCount is turn-active only (running/pending_init) — used by tests/spawn guards.
func (c *Control) activeUnderCount(forPath string) int {
	n := 0
	for _, snap := range c.pendingUnderSnapshot(forPath) {
		if snap.AgentStatus == string(StatusRunning) || snap.AgentStatus == string(StatusPendingInit) {
			n++
		}
	}
	return n
}

// pendingUnderSnapshot returns non-final agents under forPath (Codex wait set).
func (c *Control) pendingUnderSnapshot(forPath string) []LiveSnapshot {
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
		currentTool := r.rec.CurrentTool
		lastActivityAt := r.rec.LastActivityAt
		r.rec.mu.Unlock()
		if IsFinal(st) {
			continue
		}
		var elapsed int64
		if !started.IsZero() {
			elapsed = now.Sub(started).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
		}
		var lastActivityMs int64
		ref := lastActivityAt
		if ref.IsZero() {
			ref = started
		}
		if !ref.IsZero() {
			lastActivityMs = now.Sub(ref).Milliseconds()
			if lastActivityMs < 0 {
				lastActivityMs = 0
			}
		}
		out = append(out, LiveSnapshot{
			AgentName:      r.path,
			AgentStatus:    string(st),
			ElapsedMs:      elapsed,
			CurrentTool:    currentTool,
			LastActivityMs: lastActivityMs,
		})
	}
	return out
}

func (c *Control) liveUnderSnapshot(forPath string) []LiveSnapshot {
	return c.pendingUnderSnapshot(forPath)
}

func (c *Control) liveChildrenSnapshot() []LiveSnapshot {
	return c.pendingUnderSnapshot(RootPath)
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

// LiveCount returns open non-root agents.
func (c *Control) LiveCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.agents)
}

// OpenCount returns the concurrency occupancy (open agents).
func (c *Control) OpenCount() int {
	if c == nil {
		return 0
	}
	return int(c.openCount.Load())
}
