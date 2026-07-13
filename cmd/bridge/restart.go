package main

import (
	"log"
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
	log.Printf("markRestartNotify stub: chat %d, msg %d", chatID, msgID)
	return nil
}
