// Package jobs is the session-scoped background-job registry behind the agent's
// background tools and peek-job / cancel-job / steer-job.
//
// Two product kinds share the registry but differ on completion:
//
//   - kind "task"  — async sub-agent. Final answer auto-delivers into the parent
//     session (synthetic tail tool turn + auto-reentry). peek-job is diagnostic only.
//   - kind "bash"  — shell run_in_background. Output stays in the job buffer;
//     the model/user reads it with peek-job. No auto session delivery.
//
// A Manager owns a context whose lifetime is the session, NOT a single turn.
package jobs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/nilutil"
)

// Status is a job's lifecycle state.
type Status string

const (
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
	Killed  Status = "killed"
)

// View is a read-only snapshot of a job for the status bar.
type View struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"` // unix milliseconds
}

// JobNotify is a notification sent from a background sub-agent to its parent.
type JobNotify struct {
	JobID    string `json:"job_id"`
	Type     string `json:"type"` // "ack" | "progress" | "result"
	Step     int    `json:"step,omitempty"`
	AckMsg   string `json:"ack_msg,omitempty"`   // for ack
	LastTool string `json:"last_tool,omitempty"` // for progress
	Output   string `json:"output,omitempty"`    // for result (final answer)
}

type jobKey struct{}

// UpdateJobActivity updates the last active timestamp of a job associated with the context.
func UpdateJobActivity(ctx context.Context) {
	if j, ok := ctx.Value(jobKey{}).(*Job); ok && j != nil {
		j.lastActive.Store(time.Now().Unix())
	}
}

// Job is one background job. The mutex guards the streaming buffer and the
// terminal fields; the run goroutine writes them, readers (Output/Wait/snapshots)
// take the same lock.
type Job struct {
	ID    string
	Kind  string // "bash" | "task"
	Label string
	// dispatchDigest is set by the task tool for duplicate-dispatch detection (prompt/label fingerprint).
	dispatchDigest string
	// dispatchSemantic is a normalized label+prompt sketch for fuzzy duplicate detection.
	dispatchSemantic string

	mu         sync.Mutex
	buf        bytes.Buffer
	readOffset int
	status     Status
	result     string
	resultRead bool // result already surfaced by Output (task jobs stream nothing to buf)
	startedAt  int64
	cancel     context.CancelFunc
	done       chan struct{}
	lastActive atomic.Int64

	// Notification channels for sub-agent ↔ parent communication (task jobs).
	steerCh  chan string    // parent → child steer messages
	ackCh    chan JobNotify // child → parent ack, buf 4
	resultCh chan JobNotify // child → parent result, buf 1
	notifyCh chan JobNotify // child → parent progress, buf 16

	step     int
	lastTool string
	lastAck  string

	completed bool // true after completion is recorded; task jobs are removed after parent delivery
	sink      event.Sink
}

// KindTask is the auto-delivering async sub-agent job kind.
const KindTask = "task"

// KindBash is the shell background job kind (peek-based, no auto session delivery).
const KindBash = "bash"

// AutoDelivers reports whether a finished job of this kind should be written into
// the parent session and trigger auto-reentry.
func AutoDelivers(kind string) bool {
	return kind == KindTask
}

// SetDispatchDigest records a stable fingerprint for duplicate task detection.
func (m *Manager) SetDispatchDigest(jobID, digest string) {
	if m == nil || jobID == "" || digest == "" {
		return
	}
	j := m.get(jobID)
	if j == nil {
		return
	}
	j.mu.Lock()
	j.dispatchDigest = digest
	j.mu.Unlock()
}

// SetDispatchSemantic stores the normalized semantic key for fuzzy dedup.
func (m *Manager) SetDispatchSemantic(jobID, semantic string) {
	if m == nil || jobID == "" || semantic == "" {
		return
	}
	j := m.get(jobID)
	if j == nil {
		return
	}
	j.mu.Lock()
	j.dispatchSemantic = semantic
	j.mu.Unlock()
}

// DispatchSemantic returns the semantic key stored for a job.
func (m *Manager) DispatchSemantic(jobID string) string {
	j := m.get(jobID)
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dispatchSemantic
}

// DispatchDigest returns the fingerprint stored for a job.
func (m *Manager) DispatchDigest(jobID string) string {
	j := m.get(jobID)
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dispatchDigest
}

// Manager is the session's background-job table. It is safe for concurrent use.
type Manager struct {
	sink   event.Sink
	root   context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	seq            int
	jobs           map[string]*Job
	order          []string
	sem            chan struct{}
	monitorRunning bool
	jobDone        chan struct{}
	onCompletion   func(id string) // called after a job's completion is recorded

	idleKillDefault int
	idleKillByKind  map[string]int
	semanticDedup   SemanticDedupPolicy
}

// NewManager returns a Manager whose jobs run under a fresh session-scoped
// context (cancelled by Close). sink receives job-lifecycle notices; pass the
// session's synchronized sink (event.Sync) since jobs emit from goroutines.
func NewManager(sink event.Sink) *Manager {
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	root, cancel := context.WithCancel(context.Background())
	m := &Manager{
		sink:    sink,
		root:    root,
		cancel:  cancel,
		jobs:    map[string]*Job{},
		sem:     make(chan struct{}, 3),
		jobDone: make(chan struct{}, 10),
	}
	m.Configure(DefaultManagerPolicies())
	return m
}

// SetOnCompletion registers a callback that fires when a job's completion
// is recorded. Call once during initialisation, before any jobs are started.
func (m *Manager) SetOnCompletion(fn func(id string)) {
	m.onCompletion = fn
}

func (m *Manager) startMonitorIfNeeded() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.monitorRunning {
		return
	}
	m.monitorRunning = true
	go m.staleMonitorLoop()
}

func (m *Manager) checkAndClean() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().Unix()
	runningCount := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.status == Running {
			runningCount++
			lastActive := j.lastActive.Load()
			// Already hold m.mu; must not call idleKillSeconds (re-locks).
			limit := int64(m.idleKillSecondsLocked(j.Kind))
			if lastActive > 0 && now-lastActive > limit {
				j.status = Killed
				j.cancel()
			}
		}
		j.mu.Unlock()
	}
	if runningCount == 0 {
		m.monitorRunning = false
		return true
	}
	return false
}

func (m *Manager) staleMonitorLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.root.Done():
			m.mu.Lock()
			m.monitorRunning = false
			m.mu.Unlock()
			return
		case <-m.jobDone:
			if m.checkAndClean() {
				return
			}
		case <-ticker.C:
			if m.checkAndClean() {
				return
			}
		}
	}
}

// jobWriter appends a job's streamed output under its lock so a concurrent
// Output read never races the producing goroutine.
type jobWriter struct{ j *Job }

func (w jobWriter) Write(p []byte) (int, error) {
	w.j.mu.Lock()
	defer w.j.mu.Unlock()
	w.j.lastActive.Store(time.Now().Unix())
	return w.j.buf.Write(p)
}

// BeforeRunFunc runs synchronously after the job is registered but before the
// run goroutine starts. Callers use it to RegisterJobMeta so drainNotify can
// correlate results even when the job completes instantly.
type BeforeRunFunc func(jobID string)

// Start launches run on a goroutine under the manager's session context and
// returns the job immediately. run streams output to the writer and returns the
// terminal result text (a task's final answer; a bash job streams everything to
// the buffer and returns ""). The job is marked killed when its context was
// cancelled, failed on any other error, else done.
func (m *Manager) Start(ctx context.Context, kind, label string, run func(ctx context.Context, out io.Writer) (string, error), onComplete func(jobID string), beforeRun ...BeforeRunFunc) (*Job, error) {
	m.startMonitorIfNeeded()
	select {
	case m.sem <- struct{}{}:
	default:
		return nil, fmt.Errorf("Reject: too many background jobs running (max 3)")
	}

	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("%s-%d", kind, m.seq)
	jobCtx, cancel := context.WithCancel(m.root)
	j := &Job{
		ID:        id,
		Kind:      kind,
		Label:     label,
		status:    Running,
		startedAt: nowMs(),
		cancel:    cancel,
		done:      make(chan struct{}),
		steerCh:   make(chan string, 8),
		ackCh:     make(chan JobNotify, 4),
		resultCh:  make(chan JobNotify, 1),
		notifyCh:  make(chan JobNotify, 16),
		sink:      m.sink,
	}
	j.lastActive.Store(time.Now().Unix())
	jobCtx = context.WithValue(jobCtx, jobKey{}, j)

	m.jobs[id] = j
	m.order = append(m.order, id)
	m.mu.Unlock()

	m.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: startedText(kind, id, label)})

	if len(beforeRun) > 0 && beforeRun[0] != nil {
		beforeRun[0](id)
	}

	go func() {
		defer func() {
			<-m.sem
			select {
			case m.jobDone <- struct{}{}:
			default:
			}
		}()
		result, err := run(jobCtx, jobWriter{j})

		j.mu.Lock()
		killMarked := j.status == Killed
		j.mu.Unlock()

		var st Status
		switch {
		case strings.TrimSpace(result) != "" && err == nil:
			// run() returned a terminal answer — treat as Done even if Kill raced after return.
			st = Done
		case jobCtx.Err() != nil || killMarked:
			st = Killed
		case err != nil:
			st = Failed
			if result == "" {
				result = err.Error()
			}
		default:
			st = Done
		}
		if st == Killed && strings.TrimSpace(result) == "" {
			result = fmt.Sprintf("background %s %q was cancelled or killed before producing a result", kind, id)
		}
		// Task jobs need a non-empty payload so parent session delivery can commit.
		// Bash jobs keep empty result — output lives in the stream buffer for peek-job.
		if AutoDelivers(kind) && st == Done && strings.TrimSpace(result) == "" {
			result = fmt.Sprintf("background %s %q finished with an empty answer", kind, id)
		}

		// Send result to resultCh before onComplete (per spec §5.3).
		if st == Done || st == Failed {
			if !safeChanSend(j.resultCh, JobNotify{JobID: id, Type: "result", Output: result}) {
				slog.Warn("resultCh full or closed, dropping result for job", "id", id)
			}
		}

		// Emit the closing notice (wording differs by product kind).
		level, text := finishedNotice(kind, id, st, err)
		m.sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text})

		j.mu.Lock()
		j.result = result
		j.status = st
		j.completed = true
		j.mu.Unlock()
		// Single completion path: optional per-call hook, then global onCompletion.
		// Controllers should wire only SetOnCompletion; per-call onComplete is nil for task/bash.
		if onComplete != nil {
			onComplete(id)
		}
		if m.onCompletion != nil {
			m.onCompletion(id)
		}

		close(j.done)
	}()
	return j, nil
}

func (m *Manager) get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// Label returns a job's command preview label. ok is false when id is unknown.
func (m *Manager) Label(id string) (string, bool) {
	j := m.get(id)
	if j == nil {
		return "", false
	}
	return j.Label, true
}

// Kind returns a job's kind ("task", "bash", …). ok is false when id is unknown.
func (m *Manager) Kind(id string) (string, bool) {
	j := m.get(id)
	if j == nil {
		return "", false
	}
	return j.Kind, true
}

// finishedNotice is the user-visible completion line. Task jobs promise auto-delivery;
// bash jobs point operators at peek-job.
func finishedNotice(kind, id string, st Status, err error) (event.Level, string) {
	switch st {
	case Failed:
		return event.LevelWarn, fmt.Sprintf("background %s failed: %s — %v", kind, id, err)
	case Killed:
		return event.LevelInfo, fmt.Sprintf("background %s killed: %s", kind, id)
	default:
		if AutoDelivers(kind) {
			return event.LevelInfo, fmt.Sprintf("background %s finished: %s — result at conversation tail (Started card stays; ignore mid-history)", kind, id)
		}
		return event.LevelInfo, fmt.Sprintf("background %s finished: %s — use peek-job for output (not auto-delivered to chat)", kind, id)
	}
}

// Output returns the job's output produced since the last Output call plus its
// current status. ok is false when the id is unknown.
func (m *Manager) Output(id string) (text string, status Status, ok bool) {
	j := m.get(id)
	if j == nil {
		return "", "", false
	}
	j.mu.Lock()
	full := j.buf.String()
	text = full[j.readOffset:]
	j.readOffset = len(full)
	// A task job streams nothing to the buffer — its answer lands in result. Once
	// it is terminal with no buffered output, surface that result once so a task's
	// answer is visible here too (peek-job uses Output for incremental reads).
	if text == "" && j.status != Running && j.result != "" && !j.resultRead {
		text = j.result
		j.resultRead = true
	}
	status = j.status
	j.mu.Unlock()

	return text, status, true
}

// Kill cancels a running job. Returns false when the id is unknown or the job has
// already finished.
func (m *Manager) Kill(id string) bool {
	j := m.get(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	running := j.status == Running
	if running {
		// Flip to Killed synchronously so Output/Wait reflect the kill the instant
		// it's requested, not whenever the run goroutine's cmd.Run returns (which
		// trails by WaitDelay while a cancelled process tree tears down). The
		// goroutine still sets Killed + records completion on return; this only
		// fires when the job is actually Running, so a job that just finished
		// keeps its real terminal status.
		j.status = Killed
	}
	j.mu.Unlock()
	if !running {
		return false
	}
	j.cancel()
	return true
}

func (m *Manager) resolve(ids []string) []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	if len(ids) == 0 {
		for _, id := range m.order {
			j := m.jobs[id]
			if j == nil {
				continue
			}
			j.mu.Lock()
			running := j.status == Running
			j.mu.Unlock()
			if running {
				out = append(out, j)
			}
		}
		return out
	}
	for _, id := range ids {
		if j := m.jobs[id]; j != nil {
			out = append(out, j)
		}
	}
	return out
}

// Running returns a snapshot of the still-running jobs (for the status bar).
func (m *Manager) Running() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []View
	for _, id := range m.order {
		j := m.jobs[id]
		if j == nil {
			continue
		}
		j.mu.Lock()
		if j.status == Running {
			out = append(out, View{ID: j.ID, Kind: j.Kind, Label: j.Label, Status: string(j.status), StartedAt: j.startedAt})
		}
		j.mu.Unlock()
	}
	return out
}

// WaitRunning blocks until no jobs report Running or ctx is cancelled.
func (m *Manager) WaitRunning(ctx context.Context) error {
	if m == nil {
		return nil
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(m.Running()) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close cancels the session context, terminating every running job. Safe to call
// once at controller shutdown.
func (m *Manager) Close() {
	m.mu.Lock()
	// Cancel every job's context individually, since they may not derive from m.root.
	for _, j := range m.jobs {
		j.cancel()
	}
	m.mu.Unlock()
	m.cancel()
}

// safeChanSend performs a non-blocking channel send. It recovers from send-on-
// closed-channel panics so async job completion cannot crash the process when
// channels race teardown.
func safeChanSend[T any](ch chan T, v T) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	select {
	case ch <- v:
		return true
	default:
		return false
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

func startedText(kind, id, label string) string {
	if label != "" {
		return fmt.Sprintf("background %s started: %s (%s)", kind, id, label)
	}
	return fmt.Sprintf("background %s started: %s", kind, id)
}

// --- new types and methods per spec §5 ---

var (
	ErrJobNotFound     = fmt.Errorf("job not found")
	ErrSteerBufferFull = fmt.Errorf("steer buffer full")
)

// JobStatus is a read-only snapshot returned by Peek.
type JobStatus struct {
	JobID       string
	Status      string // "running" | "done" | "cancelled" | "error"
	StartedAtMs int64  // Unix ms when the job was created
	Step        int
	LastTool    string
	LastAck     string
}

// JobChannels exposes the notification channel read-ends for a job.
type JobChannels struct {
	Ack      <-chan JobNotify
	Result   <-chan JobNotify
	Progress <-chan JobNotify
}

// Steer sends a message to a job's steer channel (non-blocking).
func (m *Manager) Steer(jobID string, message string) error {
	j := m.get(jobID)
	if j == nil {
		return ErrJobNotFound
	}
	select {
	case j.steerCh <- message:
		return nil
	default:
		return ErrSteerBufferFull
	}
}

// ActiveJobs returns all job IDs currently in the map (including completed ones).
func (m *Manager) ActiveJobs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	return ids
}

// Peek returns a non-blocking snapshot of a job's status.
func (m *Manager) Peek(jobID string) (JobStatus, error) {
	j := m.get(jobID)
	if j == nil {
		return JobStatus{}, ErrJobNotFound
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	s := JobStatus{
		JobID:       j.ID,
		Status:      string(j.status),
		StartedAtMs: j.startedAt,
		Step:        j.step,
		LastTool:    j.lastTool,
		LastAck:     j.lastAck,
	}
	return s, nil
}

// NotifyChannels returns the three notification channel read-ends for a job.
// Returns nil if the job no longer exists.
func (m *Manager) NotifyChannels(jobID string) *JobChannels {
	j := m.get(jobID)
	if j == nil {
		return nil
	}
	return &JobChannels{
		Ack:      j.ackCh,
		Result:   j.resultCh,
		Progress: j.notifyCh,
	}
}

// CompletedResult returns a terminal result for a finished job when resultCh
// was not drained (e.g. buffer full drop) or the channel read raced completion.
func (m *Manager) CompletedResult(jobID string) (JobNotify, bool) {
	j := m.get(jobID)
	if j == nil {
		return JobNotify{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.completed {
		return JobNotify{}, false
	}
	// Done / Failed / Killed all carry a terminal result string for parent delivery.
	if j.status != Done && j.status != Failed && j.status != Killed {
		return JobNotify{}, false
	}
	return JobNotify{JobID: jobID, Type: "result", Output: j.result}, true
}

// RemoveJob deletes a job from the map. Called by drainNotify after consuming resultCh.
func (m *Manager) RemoveJob(jobID string) {
	m.mu.Lock()
	delete(m.jobs, jobID)
	for i, id := range m.order {
		if id == jobID {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

// --- call-context injection (mirrors agent.CallContext) ---

type ctxKey struct{}

// WithManager stamps ctx with the job manager so tools can reach it via
// FromContext. The agent sets this on every tool call's context.
func WithManager(ctx context.Context, m *Manager) context.Context {
	return context.WithValue(ctx, ctxKey{}, m)
}

// FromContext returns the job manager set by the agent, if any. ok is false for a
// plain context (headless tests, calls outside the run loop).
func FromContext(ctx context.Context) (*Manager, bool) {
	m, ok := ctx.Value(ctxKey{}).(*Manager)
	return m, ok && m != nil
}

// PostMessage is a retired mid-flight report API. Always returns false; kept so
// older tests compile. Task results deliver only at job completion.
func PostMessage(ctx context.Context, msg string) bool {
	_ = ctx
	_ = msg
	return false
}
