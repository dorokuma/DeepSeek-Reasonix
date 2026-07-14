package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

func requestRestart(reason string) {
	restartOnce.Do(func() {
		log.Printf("restart: %s", reason)
		close(restartCh)
	})
}

func restartRequested() <-chan struct{} {
	return restartCh
}

func notifyRestart() {
	requestRestart("notifyRestart")
}

func markRestartNotify(chatID int64, msgID int) error {
	log.Printf("markRestartNotify: chat %d, msg %d", chatID, msgID)
	data := struct {
		ChatID int64 `json:"chat_id"`
		MsgID  int   `json:"msg_id"`
	}{ChatID: chatID, MsgID: msgID}
	// Write to a well-known path so the restarted process can pick it up.
	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/reasonix-bridge"
	}
	_ = os.MkdirAll(stateDir, 0o700)
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, "restart_notify.json"), b, 0o600)
}

// pendingRestartNotify reads and deletes the restart-notify marker file.
// Returns the stored chatID if present, otherwise 0.
func pendingRestartNotify() int64 {
	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/reasonix-bridge"
	}
	path := filepath.Join(stateDir, "restart_notify.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	_ = os.Remove(path) // consume the marker
	var data struct {
		ChatID int64 `json:"chat_id"`
		MsgID  int   `json:"msg_id"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		log.Printf("pendingRestartNotify: unmarshal: %v", err)
		return 0
	}
	return data.ChatID
}
