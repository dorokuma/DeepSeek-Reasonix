package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

// sessionIndex maps chatID → session file path
type sessionIndex map[int64]string

// SessionManager manages per-chat Controller instances.
type SessionManager struct {
	mu     sync.Mutex
	index  sessionIndex
	active map[int64]*control.Controller
	config *Config
	// sinks is per-chat; boot.Build captures the sink for that chat only.
	// A single global sink would race when multiple chats build controllers.
	sinks map[int64]event.Sink
	store *stateStore // index persistence (#4, #6, #11, #16)
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager(cfg *Config) *SessionManager {
	return &SessionManager{
		index:  make(sessionIndex),
		active: make(map[int64]*control.Controller),
		sinks:  make(map[int64]event.Sink),
		config: cfg,
	}
}

// SetChatSink sets the event sink used when building a controller for chatID.
func (sm *SessionManager) SetChatSink(chatID int64, s event.Sink) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.sinks == nil {
		sm.sinks = make(map[int64]event.Sink)
	}
	sm.sinks[chatID] = s
}

// SetSink is kept for callers that only have a single chat; prefers chat 0.
// Prefer SetChatSink.
func (sm *SessionManager) SetSink(s event.Sink) {
	sm.SetChatSink(0, s)
}

// SetStore sets the state store for index persistence.
func (sm *SessionManager) SetStore(s *stateStore) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.store = s
}

// LoadIndex loads the chat→path index from the state store on startup.
func (sm *SessionManager) LoadIndex() error {
	sm.mu.Lock()
	store := sm.store
	sm.mu.Unlock()
	if store == nil {
		return nil
	}
	records, err := store.load()
	if err != nil {
		return err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, rec := range records {
		if rec.SessionPath != "" {
			sm.index[rec.ChatID] = rec.SessionPath
		}
	}
	log.Printf("loaded %d session index entries from state store", len(records))
	return nil
}

// SaveAll persists the full index to the state store.
func (sm *SessionManager) SaveAll() error {
	sm.mu.Lock()
	store := sm.store
	sm.mu.Unlock()
	if store == nil {
		return nil
	}
	sm.mu.Lock()
	records := make([]chatRecord, 0, len(sm.index))
	for chatID, path := range sm.index {
		records = append(records, chatRecord{ChatID: chatID, SessionPath: path})
	}
	sm.mu.Unlock()
	return store.saveAll(records)
}

// sessionDir returns the effective session directory from config or default.
func (sm *SessionManager) sessionDir() string {
	if sm.config.SessionDir != "" {
		return sm.config.SessionDir
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "reasonix", "sessions")
}

// resolvePath returns the session file path for a chat.
// Per §3.2.2: if index has path, use it; otherwise mint new path.
func (sm *SessionManager) resolvePath(chatID int64, label string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if p, ok := sm.index[chatID]; ok && p != "" {
		return p
	}
	// Mint new path: sessionDir/<label>_<chatID>.jsonl
	path := agent.NewSessionPath(sm.sessionDir(), label)
	sm.index[chatID] = path

	// Persist the new mapping immediately (#4, #6, #11, #16)
	if sm.store != nil {
		if err := sm.store.upsert(chatRecord{ChatID: chatID, SessionPath: path}); err != nil {
			log.Printf("persist index for chat %d: %v", chatID, err)
		}
	}

	return path
}

// ensureController returns a ready Controller for the chat.
// Per §3.2.2: reuse active, or Build+Load+Resume/SetSessionPath.
// Concurrent first-message races: only one controller is kept; the loser is Closed.
func (sm *SessionManager) ensureController(ctx context.Context, chatID int64) (*control.Controller, error) {
	sm.mu.Lock()
	if ctrl, ok := sm.active[chatID]; ok {
		sm.mu.Unlock()
		return ctrl, nil
	}
	sink := sm.sinks[chatID]
	sm.mu.Unlock()

	if sink == nil {
		return nil, fmt.Errorf("ensureController: chat %d has no sink — call ensureChatSink before Send (ApprovalRequest would be discarded)", chatID)
	}

	// Build new controller (may race with another goroutine for the same chat).
	ctrl, err := boot.Build(ctx, boot.Options{
		Model:            sm.config.Model,
		WorkspaceRoot:    sm.config.WorkDir,
		Sink:             sink,
		SkipModelRefresh: true,  // per P2-5: skip refresh on hot path
		RequireKey:       false, // key provided via env
	})
	if err != nil {
		return nil, fmt.Errorf("boot.Build: %w", err)
	}

	// Resolve or create session path
	path := sm.resolvePath(chatID, ctrl.Label())

	// Load existing session or start fresh
	sess, err := agent.LoadSession(path)
	if err != nil {
		if os.IsNotExist(err) {
			ctrl.SetSessionPath(path)
		} else {
			// Session file corrupted — mint new path per §3.2.2
			log.Printf("session load failed for chat %d (path %s): %v — minting new path", chatID, path, err)
			newPath := agent.NewSessionPath(filepath.Dir(path), ctrl.Label())
			sm.mu.Lock()
			sm.index[chatID] = newPath
			sm.mu.Unlock()
			ctrl.SetSessionPath(newPath)
			// Persist the corrected mapping immediately
			if sm.store != nil {
				if perr := sm.store.upsert(chatRecord{ChatID: chatID, SessionPath: newPath}); perr != nil {
					log.Printf("persist re-minted path for chat %d: %v", chatID, perr)
				}
			}
		}
	} else {
		ctrl.Resume(sess, path)
	}

	// Wire interactive approval so ApprovalRequest events reach Telegram
	// inline keyboards (handleApprove). Do NOT SetBypass — silent auto-allow
	// hides a broken popup path; popups must actually show.
	ctrl.EnableInteractiveApproval()

	// Double-check: another goroutine may have won the race.
	sm.mu.Lock()
	if existing, ok := sm.active[chatID]; ok {
		sm.mu.Unlock()
		log.Printf("ensureController: race for chat %d — keeping existing controller", chatID)
		ctrl.Close()
		return existing, nil
	}
	sm.active[chatID] = ctrl
	sm.mu.Unlock()

	return ctrl, nil
}

// Submit routes user input through Controller.Submit (same entry as TUI/serve):
// !shell, built-in slash, skills, or a model turn. Bridge must not use Send for
// raw user text that may contain slash commands.
func (sm *SessionManager) Submit(ctx context.Context, chatID int64, text string) error {
	ctrl, err := sm.ensureController(ctx, chatID)
	if err != nil {
		return err
	}
	ctrl.Submit(text)
	return nil
}

// SyncSessionPath updates the chat→path index after Submit("/new") or similar.
func (sm *SessionManager) SyncSessionPath(chatID int64) {
	sm.mu.Lock()
	ctrl, ok := sm.active[chatID]
	store := sm.store
	sm.mu.Unlock()
	if !ok || ctrl == nil {
		return
	}
	path := ctrl.SessionPath()
	if path == "" {
		return
	}
	sm.mu.Lock()
	sm.index[chatID] = path
	sm.mu.Unlock()
	if store != nil {
		if err := store.upsert(chatRecord{ChatID: chatID, SessionPath: path}); err != nil {
			log.Printf("SyncSessionPath chat %d: %v", chatID, err)
		}
	}
}

// Stop cancels the current turn for the given chat.
func (sm *SessionManager) Stop(chatID int64) {
	sm.mu.Lock()
	ctrl, ok := sm.active[chatID]
	sm.mu.Unlock()
	if ok {
		ctrl.Cancel()
	}
}

// NewSession starts a brand-new conversation for the given chat.
// Per §3.2.2/§5.1: Cancels running turn → Snapshot → agent.NewSession →
// mint new path → update index → persist.  The caller should reply to the user.
func (sm *SessionManager) NewSession(ctx context.Context, chatID int64) error {
	sm.mu.Lock()
	ctrl, ok := sm.active[chatID]
	sm.mu.Unlock()

	if !ok {
		// No active controller — ensure one exists first, then reset.
		var err error
		ctrl, err = sm.ensureController(ctx, chatID)
		if err != nil {
			return fmt.Errorf("ensureController: %w", err)
		}
	}

	// Cancel any running turn first.
	if ctrl.Running() {
		ctrl.Cancel()
		ctrl.Wait()
	}

	// NewSession snapsots, resets the executor, mints a new path, fires hooks.
	if err := ctrl.NewSession(); err != nil {
		return fmt.Errorf("NewSession: %w", err)
	}

	// Update the index with the newly minted path.
	newPath := ctrl.SessionPath()

	sm.mu.Lock()
	sm.index[chatID] = newPath
	store := sm.store
	sm.mu.Unlock()

	if store != nil {
		if err := store.upsert(chatRecord{ChatID: chatID, SessionPath: newPath}); err != nil {
			log.Printf("persist index after /new for chat %d: %v", chatID, err)
		}
	}

	return nil
}

// ResetSession removes the session for a chat, forcing a fresh start on next Send.
// It cancels any running turn, removes the controller from active, and deletes
// the chat from the index and state store.  The next call to ensureController
// will mint a brand-new session path.
func (sm *SessionManager) ResetSession(chatID int64) {
	sm.mu.Lock()
	// Cancel and remove from active
	if ctrl, ok := sm.active[chatID]; ok {
		ctrl.Cancel()
		delete(sm.active, chatID)
	}
	// Remove from index
	delete(sm.index, chatID)
	store := sm.store
	sm.mu.Unlock()

	// Remove from state store
	if store != nil {
		if err := store.remove(chatID); err != nil {
			log.Printf("ResetSession: remove chat %d from state store: %v", chatID, err)
		}
	}
}

// ControllerFor returns the active controller for a chat, or nil.
func (sm *SessionManager) ControllerFor(chatID int64) *control.Controller {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.active[chatID]
}

// Shutdown gracefully closes all controllers per §9.2.
// 1. For each active controller: Cancel if Running → bounded wait → Snapshot → Close.
// 2. Bounded wait uses a select with time.After so shutdown never hangs.
func (sm *SessionManager) Shutdown() {
	sm.mu.Lock()
	chats := make([]int64, 0, len(sm.active))
	for chatID := range sm.active {
		chats = append(chats, chatID)
	}
	sm.mu.Unlock()

	for _, chatID := range chats {
		sm.mu.Lock()
		ctrl, ok := sm.active[chatID]
		sm.mu.Unlock()
		if !ok {
			continue
		}

		// 1. Cancel if running
		if ctrl.Running() {
			ctrl.Cancel()
		}

		// 2. Bounded wait: give the turn a moment to wind down before snapshotting
		//    (§9.2). We use a select with a done channel driven by ctrl.Wait() so
		//    shutdown never stalls.
		waitDone := make(chan struct{})
		go func() {
			ctrl.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
			// turn finished promptly
		case <-time.After(5 * time.Second):
			log.Printf("controller for chat %d: wait timed out, proceeding to snapshot", chatID)
		}

		// 3. Snapshot
		if err := ctrl.Snapshot(); err != nil {
			log.Printf("controller snapshot error (chat %d): %v", chatID, err)
		}

		// 4. Close
		ctrl.Close()

		// Remove from active map
		sm.mu.Lock()
		delete(sm.active, chatID)
		sm.mu.Unlock()
	}

	// Persist the final index before exiting (#4, #6, #11, #16)
	if err := sm.SaveAll(); err != nil {
		log.Printf("save index on shutdown: %v", err)
	}
}
