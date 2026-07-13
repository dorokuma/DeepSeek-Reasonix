package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"reasonix/internal/event"
)

// CronTask describes a single scheduled message.
type CronTask struct {
	ID          int    `json:"id"`
	Schedule    string `json:"schedule"`     // cron expression, e.g. "0 9 * * *"
	ChatID      int64  `json:"chat_id"`      // target Telegram chat
	Message     string `json:"message"`      // message body to send
	SessionPath string `json:"session_path"` // independent session path (empty = auto-mint)
	Enabled     bool   `json:"enabled"`
	Label       string `json:"label,omitempty"` // human-readable label for logging
}

// CronTaskStore persists and loads cron tasks.
type CronTaskStore struct {
	mu   sync.Mutex
	path string
}

func newCronTaskStore(stateDir string) *CronTaskStore {
	return &CronTaskStore{
		path: filepath.Join(stateDir, "cron_tasks.json"),
	}
}

func (s *CronTaskStore) load() ([]CronTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tasks []CronTask
	if err := json.Unmarshal(b, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *CronTaskStore) save(tasks []CronTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0600)
}

// CronManager manages scheduled cron tasks for the bridge.
type CronManager struct {
	mu       sync.Mutex
	cron     *cron.Cron
	store    *CronTaskStore
	tasks    []CronTask
	entryIDs map[int]cron.EntryID // task ID → cron entry ID
	sm       *SessionManager
	client   *TelegramClient
	cfg      *Config
	ctx      contextShutdown
	nextID   int
}

// contextShutdown matches the cancelable context signature the bridge uses.
type contextShutdown interface {
	Done() <-chan struct{}
	Err() error
}

// NewCronManager creates a CronManager. It starts with no tasks; call Load()
// to restore persisted tasks and Start() to begin scheduling.
func NewCronManager(sm *SessionManager, client *TelegramClient, cfg *Config, ctx contextShutdown) *CronManager {
	return &CronManager{
		cron:     cron.New(cron.WithSeconds()),
		store:    newCronTaskStore(cfg.StateDir),
		entryIDs: make(map[int]cron.EntryID),
		sm:       sm,
		client:   client,
		cfg:      cfg,
		ctx:      ctx,
		nextID:   1,
	}
}

// Load reads persisted tasks from the state store and registers them.
func (m *CronManager) Load() error {
	tasks, err := m.store.load()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.tasks = tasks
	m.mu.Unlock()

	for _, t := range tasks {
		if t.Enabled {
			m.addTask(t)
		}
	}
	log.Printf("cron: loaded %d tasks (%d enabled)", len(tasks), countEnabled(tasks))
	return nil
}

// Start begins the cron scheduler.
func (m *CronManager) Start() {
	m.cron.Start()
	log.Println("cron: scheduler started")
}

// Stop gracefully stops the cron scheduler.
func (m *CronManager) Stop() {
	ctx := m.cron.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		log.Println("cron: stop timed out")
	}
	log.Println("cron: scheduler stopped")
}

// AddTask adds a new task and persists the task list.
func (m *CronManager) AddTask(t CronTask) (int, error) {
	m.mu.Lock()
	t.ID = m.nextID
	m.nextID++
	m.tasks = append(m.tasks, t)
	m.mu.Unlock()

	if t.Enabled {
		m.addTask(t)
	}

	if err := m.store.save(m.tasks); err != nil {
		log.Printf("cron: persist tasks: %v", err)
	}

	return t.ID, nil
}

// RemoveTask removes a task by ID.
func (m *CronManager) RemoveTask(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Remove from cron scheduler.
	if eid, ok := m.entryIDs[id]; ok {
		m.cron.Remove(eid)
		delete(m.entryIDs, id)
	}

	// Remove from task list.
	updated := make([]CronTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t.ID != id {
			updated = append(updated, t)
		}
	}
	m.tasks = updated

	return m.store.save(m.tasks)
}

// ListTasks returns a copy of all registered tasks.
func (m *CronManager) ListTasks() []CronTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CronTask, len(m.tasks))
	copy(out, m.tasks)
	return out
}

// addTask registers a single task with the cron scheduler.
func (m *CronManager) addTask(t CronTask) {
	task := t // capture
	eid, err := m.cron.AddFunc(task.Schedule, func() {
		m.executeTask(task)
	})
	if err != nil {
		log.Printf("cron: schedule task %d (%q): %v", task.ID, task.Schedule, err)
		return
	}
	m.mu.Lock()
	m.entryIDs[task.ID] = eid
	m.mu.Unlock()
	log.Printf("cron: scheduled task %d: %q label=%q", task.ID, task.Schedule, task.Label)
}

// executeTask runs one cron task: Submit → Wait → Snapshot (same entry as chat).
// On failure, a summary is sent to the bound chat.
func (m *CronManager) executeTask(t CronTask) {
	log.Printf("cron: executing task %d (chat %d)", t.ID, t.ChatID)

	// Build a short-lived context with timeout.
	ctx, cancel := contextTimeout(m.cfg.StateDir)
	defer cancel()

	// Ensure a non-nil sink so ApprovalRequest is not discarded; prefer an
	// already-registered chat sink (user has chatted), else a no-op sink.
	m.sm.mu.Lock()
	if m.sm.sinks == nil || m.sm.sinks[t.ChatID] == nil {
		if m.sm.sinks == nil {
			m.sm.sinks = make(map[int64]event.Sink)
		}
		m.sm.sinks[t.ChatID] = &cronSink{chatID: t.ChatID, client: m.client}
	}
	m.sm.mu.Unlock()

	// Same path as Telegram messages: Submit (slash/!/model turn), not bare Send.
	if err := m.sm.Submit(ctx, t.ChatID, t.Message); err != nil {
		log.Printf("cron: task %d: Submit: %v", t.ID, err)
		m.notifyFailure(t, fmt.Sprintf("提交失败：%v", err))
		return
	}

	ctrl := m.sm.ControllerFor(t.ChatID)
	if ctrl == nil {
		m.notifyFailure(t, "💤 无活跃会话")
		return
	}

	// Wait for the turn to complete.
	ctrl.Wait()

	// Snapshot to persist the turn.
	if err := ctrl.Snapshot(); err != nil {
		log.Printf("cron: task %d: snapshot: %v", t.ID, err)
	}

	log.Printf("cron: task %d completed", t.ID)
}

// cronSink is a minimal event.Sink for scheduled tasks that have never opened a chat sink.
// It only surfaces errors/notices to the bound Telegram chat.
type cronSink struct {
	chatID int64
	client *TelegramClient
}

func (s *cronSink) Emit(e event.Event) {
	switch e.Kind {
	case event.TurnDone:
		if e.Err != nil && !isBenignTurnErr(e.Err) {
			msg := fmt.Sprintf("⚠️ 定时任务回合出错：%v", e.Err)
			_, _ = s.client.Send(backgroundContext(), NewMessage(s.chatID, msg))
		}
	case event.Message:
		if text := strings.TrimSpace(e.Text); text != "" {
			_, _ = s.client.Send(backgroundContext(), NewMessage(s.chatID, text))
		}
	case event.Notice:
		if text := strings.TrimSpace(e.Text); text != "" {
			_, _ = s.client.Send(backgroundContext(), NewMessage(s.chatID, text))
		}
	}
}

// notifyFailure sends a failure summary to the bound chat.
func (m *CronManager) notifyFailure(t CronTask, cause string) {
	msg := "⚠️ 定时任务失败\n"
	if t.Label != "" {
		msg += "任务: " + t.Label + "\n"
	}
	msg += "原因: " + cause

	// Try to send; log failure but don't propagate.
	if _, err := m.client.Send(backgroundContext(), NewMessage(t.ChatID, msg)); err != nil {
		log.Printf("cron: notifyFailure (chat %d): %v", t.ChatID, err)
	}
}

func countEnabled(tasks []CronTask) int {
	n := 0
	for _, t := range tasks {
		if t.Enabled {
			n++
		}
	}
	return n
}
