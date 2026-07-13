package main

import (
	"log"
	"sync"
)

// Light self-restart: close restartCh once → main exits after graceful Shutdown.
// systemd Restart=always brings the process back; sessions resume via index+jsonl.

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
