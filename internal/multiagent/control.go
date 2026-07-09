package multiagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default wait timeouts (Codex MultiAgentV2Config defaults-ish).
const (
	DefaultWaitTimeoutMs = 600_000 // 10m
	MinWaitTimeoutMs     = 1_000
	MaxWaitTimeoutMs     = 3_600_000
	DefaultMaxConcurrent = 6
	DefaultMaxDepth      = 3
	RootPath             = "/root"
)

// Runner runs a spawned agent to completion (implemented outside this package).
type Runner interface {
	// Run executes the child agent. path is canonical task path; message is the prompt.
	// depth is NestingDepth for the child (1 = first spawn from root).
	Run(ctx context.Context, path, message string, depth int) (answer string, err error)
}

// AgentRec is one live or retained agent thread record.
type AgentRec struct {
	Path         string
	Nickname     string
	Status       Status
	LastAnswer   string
	LastError    string
	LastTaskMsg  string
	Depth        int
	cancel       context.CancelFunc
	mu           sync.Mutex
}

// Control is the MultiAgent V2 control plane for one root session.
type Control struct {
	mu            sync.Mutex
	agents        map[string]*AgentRec // key = full path
	byLeaf        map[string]string    // leaf -> full path (last wins)
	mailbox       *Mailbox
	runner        Runner
	maxConcurrent int
	maxDepth      int
	// OnTriggerTurn is invoked when mail with TriggerTurn arrives and something
	// should wake the parent (followup_task). Completion uses TriggerTurn=false.
	OnTriggerTurn func()
	// OnCompletion is optional host hook after a child finishes (for auto-reentry
	// of parent when idle). Codex completion is mailbox-only; reasonix may set this.
	OnCompletion func()
	runningCount atomic.Int32
}

// NewControl builds a session multi-agent controller.
func NewControl() *Control {
	return &Control{
		agents:        make(map[string]*AgentRec),
		byLeaf:        make(map[string]string),
		mailbox:       NewMailbox(),
		maxConcurrent: DefaultMaxConcurrent,
		maxDepth:      DefaultMaxDepth,
	}
}

func (c *Control) SetRunner(r Runner) { c.runner = r }

func (c *Control) Mailbox() *Mailbox { return c.mailbox }

// NotifySteer signals wait_agent that the user interrupted (Codex Steer).
func (c *Control) NotifySteer() {
	if c == nil {
		return
	}
	c.mailbox.NotifySteer()
}

// Spawn starts a background agent (Codex spawn_agent). Returns task_name JSON fields.
func (c *Control) Spawn(ctx context.Context, parentPath, taskName, message string, parentDepth int) (taskPath, nickname string, err error) {
	if c.runner == nil {
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

	path := JoinPath(parentPath, taskName)
	// Unique path if collision
	c.mu.Lock()
	if _, exists := c.agents[path]; exists {
		path = JoinPath(parentPath, fmt.Sprintf("%s_%d", taskName, time.Now().Unix()%10000))
	}
	nick := LeafName(path)
	rec := &AgentRec{
		Path:        path,
		Nickname:    nick,
		Status:      StatusPendingInit,
		LastTaskMsg: message,
		Depth:       childDepth,
	}
	c.agents[path] = rec
	c.byLeaf[nick] = path
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	rec.mu.Lock()
	rec.cancel = cancel
	rec.Status = StatusRunning
	rec.mu.Unlock()
	c.runningCount.Add(1)

	go func() {
		defer c.runningCount.Add(-1)
		answer, runErr := c.runner.Run(runCtx, path, message, childDepth)
		rec.mu.Lock()
		if runCtx.Err() != nil {
			rec.Status = StatusInterrupted
			rec.LastError = "interrupted"
		} else if runErr != nil {
			rec.Status = StatusErrored
			rec.LastError = runErr.Error()
			rec.LastAnswer = strings.TrimSpace(answer)
		} else {
			rec.Status = StatusCompleted
			rec.LastAnswer = strings.TrimSpace(answer)
		}
		status := rec.Status
		lastAns := rec.LastAnswer
		lastErr := rec.LastError
		rec.mu.Unlock()

		// Forward completion to parent mailbox (Codex forward_child_completion_to_parent).
		// trigger_turn=false — wait_agent or next turn drains mail (1:1 V2).
		msg := formatCompletionMessage(path, status, lastAns, lastErr)
		parent := ParentPath(path)
		if parent == "" {
			parent = RootPath
		}
		c.mailbox.Enqueue(Mail{
			From:        path,
			To:          parent,
			Message:     msg,
			TriggerTurn: false,
		})
		if c.OnCompletion != nil {
			c.OnCompletion()
		}
	}()

	return path, nick, nil
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

// ResolveTarget maps target string to full path (relative leaf or canonical).
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
	// try as /root/... path prefix
	if strings.HasPrefix(target, "/") {
		if rec, ok := c.agents[target]; ok {
			return rec.Path, nil
		}
		return "", fmt.Errorf("agent %q not found", target)
	}
	if full, ok := c.byLeaf[NormalizePathSegment(target)]; ok {
		return full, nil
	}
	// suffix match
	for path := range c.agents {
		if LeafName(path) == NormalizePathSegment(target) || strings.HasSuffix(path, "/"+target) {
			return path, nil
		}
	}
	return "", fmt.Errorf("agent %q not found", target)
}

// GetStatus returns status for path.
func (c *Control) GetStatus(path string) (Status, string, string) {
	c.mu.Lock()
	rec := c.agents[path]
	c.mu.Unlock()
	if rec == nil {
		return StatusNotFound, "", ""
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.Status, rec.LastAnswer, rec.LastError
}

// ListedAgent is list_agents item.
type ListedAgent struct {
	AgentName        string `json:"agent_name"`
	AgentStatus      any    `json:"agent_status"`
	LastTaskMessage  string `json:"last_task_message"`
}

// List returns live agents, optional path_prefix filter.
func (c *Control) List(pathPrefix string) []ListedAgent {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := strings.TrimSpace(pathPrefix)
	var out []ListedAgent
	for path, rec := range c.agents {
		if prefix != "" && !strings.HasPrefix(path, prefix) && path != prefix {
			continue
		}
		rec.mu.Lock()
		st := rec.Status
		ans := rec.LastAnswer
		errMsg := rec.LastError
		last := rec.LastTaskMsg
		rec.mu.Unlock()
		name := path
		if rec.Nickname != "" {
			name = rec.Nickname
		}
		out = append(out, ListedAgent{
			AgentName:       name,
			AgentStatus:     StatusJSON(st, ans, errMsg),
			LastTaskMessage: last,
		})
	}
	return out
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

// SendMessage queues a message to a child (Codex send_message / followup_task).
// triggerTurn=true starts follow-up work on idle children.
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
	rec.LastTaskMsg = message
	st := rec.Status
	rec.mu.Unlock()

	// Always record as mail for observability.
	c.mailbox.Enqueue(Mail{
		From:        fromPath,
		To:          path,
		Message:     message,
		TriggerTurn: triggerTurn,
	})
	if triggerTurn && c.OnTriggerTurn != nil {
		c.OnTriggerTurn()
	}

	// If completed/errored and followup, re-spawn follow-up run on same path.
	if triggerTurn && (st == StatusCompleted || st == StatusErrored || st == StatusInterrupted) {
		if c.runner == nil {
			return nil
		}
		childDepth := rec.Depth
		if childDepth < 1 {
			childDepth = 1
		}
		runCtx, cancel := context.WithCancel(context.Background())
		rec.mu.Lock()
		rec.cancel = cancel
		rec.Status = StatusRunning
		rec.mu.Unlock()
		c.runningCount.Add(1)
		go func() {
			defer c.runningCount.Add(-1)
			answer, runErr := c.runner.Run(runCtx, path, message, childDepth)
			rec.mu.Lock()
			if runCtx.Err() != nil {
				rec.Status = StatusInterrupted
				rec.LastError = "interrupted"
			} else if runErr != nil {
				rec.Status = StatusErrored
				rec.LastError = runErr.Error()
				rec.LastAnswer = strings.TrimSpace(answer)
			} else {
				rec.Status = StatusCompleted
				rec.LastAnswer = strings.TrimSpace(answer)
			}
			status := rec.Status
			lastAns := rec.LastAnswer
			lastErr := rec.LastError
			rec.mu.Unlock()
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
		}()
	}
	_ = st
	return nil
}

// Wait blocks until mailbox activity, steer, or timeout (Codex wait_agent v2).
func (c *Control) Wait(ctx context.Context, timeoutMs int64) (message string, timedOut bool) {
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
		switch *pending {
		case ActivitySteer:
			return "Wait interrupted by new input.", false
		default:
			return "Wait completed.", false
		}
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
		// re-check pending mail that raced
		if c.mailbox.HasPending() {
			return "Wait completed.", false
		}
		return "Wait timed out.", true
	}
}

// FormatMailsForSession renders drained mails as a single user-visible block.
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
