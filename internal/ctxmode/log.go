package ctxmode

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// LogLevel controls ctxmode diagnostic logging to stderr.
type LogLevel int

const (
	LogOff LogLevel = iota
	LogMiss
	LogAll
)

// LogLevelFromEnv reads REASONIX_CTX_LOG: off (default), miss, or all.
func LogLevelFromEnv() LogLevel {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REASONIX_CTX_LOG"))) {
	case "miss", "decline", "declines":
		return LogMiss
	case "all", "1", "true", "yes", "on":
		return LogAll
	default:
		return LogOff
	}
}

func logMiss(detail string) {
	if LogLevelFromEnv() < LogMiss {
		return
	}
	log.Printf("ctx miss: %s", detail)
}

func logHit(detail string) {
	if LogLevelFromEnv() < LogAll {
		return
	}
	log.Printf("ctx hit: %s", detail)
}

// LogMissStore records a sandbox store failure.
func LogMissStore(tool string, bytes int, err error) {
	logMiss(fmt.Sprintf("surface=store tool=%s bytes=%d err=%v", tool, bytes, err))
}

// LogHitSandbox records a successful outbound sandbox.
func LogHitSandbox(tool, ref string, bytes int) {
	logHit(fmt.Sprintf("surface=sandbox tool=%s ref=%s bytes=%d", tool, ref, bytes))
}

// LogJournalErr records a journal persistence failure (REASONIX_CTX_LOG=all).
func LogJournalErr(op string, err error) {
	if LogLevelFromEnv() < LogAll || err == nil {
		return
	}
	log.Printf("ctx journal %s: %v", op, err)
}