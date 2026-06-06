package rtk

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// LogLevel controls RTK diagnostic logging to stderr.
type LogLevel int

const (
	LogOff LogLevel = iota
	LogMiss
	LogAll
)

// LogLevelFromEnv reads REASONIX_RTK_LOG: off (default), miss, or all.
// Legacy values 1, true, yes, and on map to all (preserve prior hit logging).
func LogLevelFromEnv() LogLevel {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REASONIX_RTK_LOG"))) {
	case "miss", "decline", "declines":
		return LogMiss
	case "all", "1", "true", "yes", "on":
		return LogAll
	default:
		return LogOff
	}
}

func logMiss(surface, detail string) {
	if LogLevelFromEnv() < LogMiss {
		return
	}
	log.Printf("rtk miss: %s %s", surface, detail)
}

func logHit(cmd, rewritten string) {
	if LogLevelFromEnv() < LogAll {
		return
	}
	log.Printf("rtk hit: cmd=%q rewritten=%q", cmd, rewritten)
}

// LogMissBuiltin records a builtin gate fallback (grep, ls, …).
func LogMissBuiltin(surface, cmd, reason string) {
	logMiss("surface="+surface, fmt.Sprintf("cmd=%q reason=%s", cmd, reason))
}

// LogMissBash records a bash command that rewrite did not change.
func LogMissBash(cmd, reason string) {
	logMiss("surface=bash", fmt.Sprintf("cmd=%q reason=%s", cmd, reason))
}

// LogMissPipe records large-output compaction that did not use rtk pipe.
func LogMissPipe(tool, filter string, bytes int, reason string) {
	detail := fmt.Sprintf("tool=%s bytes=%d reason=%s", tool, bytes, reason)
	if filter != "" {
		detail += " filter=" + filter
	}
	logMiss("surface=pipe", detail)
}