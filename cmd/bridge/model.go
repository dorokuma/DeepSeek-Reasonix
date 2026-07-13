package main

import (
	"context"
	"fmt"
	"log"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
)

// handleModel implements the /model command logic.
//
// Without arguments it shows the current model name.
// With arguments it switches the model for the current chat.
//
// Per design §5.1, the switch sequence is:
//   Snapshot → boot.Build(newModel) → Load+Resume same path → atomic replace → old Close
//
// If the switch succeeds the user is notified; on failure the old controller is preserved.
func (b *Bridge) handleModel(chatID int64, args string) {
	if args == "" {
		// No args: show current model.
		ctrl := b.sm.ControllerFor(chatID)
		model := b.cfg.Model
		if model == "" && ctrl != nil {
			model = ctrl.Label()
		}
		if model == "" {
			model = "未设置"
		}
		b.sendMessage(chatID, fmt.Sprintf("🤖 %s", model))
		return
	}

	// Switch to new model.
	b.sendMessage(chatID, fmt.Sprintf("🔄 → %s", args))

	if err := b.sm.SwitchModel(b.ctx, chatID, args); err != nil {
		log.Printf("/model error (chat %d): %v", chatID, err)
		b.sendMessage(chatID, fmt.Sprintf("❌ 切换失败: %v", err))
		return
	}

	b.sendMessage(chatID, fmt.Sprintf("✅ %s", args))
}

// SwitchModel switches the active controller for the given chat to the new model.
// It performs the full switch sequence: Snapshot → boot.Build(newModel) →
// Resume same session path → atomic replace → old Close.
// If any step fails, the old controller remains active and the error is returned.
func (sm *SessionManager) SwitchModel(ctx context.Context, chatID int64, newModel string) error {
	sm.mu.Lock()
	oldCtrl, ok := sm.active[chatID]
	sm.mu.Unlock()

	if !ok {
		// No active controller — ensure one exists first.
		ctrl, err := sm.ensureController(ctx, chatID)
		if err != nil {
			return fmt.Errorf("ensureController: %w", err)
		}
		// Built with the default model; still switch if a model was requested.
		oldCtrl = ctrl
	}

	// 1. Cancel any running turn and Snapshot the old controller.
	if oldCtrl.Running() {
		oldCtrl.Cancel()
		oldCtrl.Wait()
	}
	if err := oldCtrl.Snapshot(); err != nil {
		log.Printf("SwitchModel: snapshot before switch (chat %d): %v", chatID, err)
		// Non-fatal — continue with switch.
	}

	// Capture the current session path before switching.
	oldPath := oldCtrl.SessionPath()

	// 2. Build a new controller with the requested model.
	sm.mu.Lock()
	sink := sm.sinks[chatID]
	sm.mu.Unlock()
	newCtrl, err := boot.Build(ctx, boot.Options{
		Model:            newModel,
		WorkspaceRoot:    sm.config.WorkDir,
		Sink:             sink,
		SkipModelRefresh: true, // skip live probe on hot path
		RequireKey:       false,
	})
	if err != nil {
		return fmt.Errorf("boot.Build(%q): %w", newModel, err)
	}

	// 3. If there's an existing session, load and resume it on the new controller.
	if oldPath != "" {
		sess, err := agent.LoadSession(oldPath)
		if err == nil {
			newCtrl.Resume(sess, oldPath)
		} else {
			// Session file missing or corrupted — start fresh on the same path.
			log.Printf("SwitchModel: resume on %s failed (%v); starting fresh", oldPath, err)
			newCtrl.SetSessionPath(oldPath)
		}
	}
	newCtrl.EnableInteractiveApproval()

	// 4. Atomic replace in the active map.
	sm.mu.Lock()
	sm.active[chatID] = newCtrl
	sm.mu.Unlock()

	// 5. Close the old controller in background (it has already been snapshotted).
	go func() {
		oldCtrl.Close()
	}()

	return nil
}
