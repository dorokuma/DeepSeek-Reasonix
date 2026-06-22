package rtk

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// LogLevel controls RTK diagnostic logging (to the global slog handler when
// enabled via REASONIX_RTK_LOG, for consistency with CTX and app logging).
type LogLevel int

const (
	LogOff LogLevel = iota
	LogMiss
	LogAll
)

// LogLevelFromEnv reads REASONIX_RTK_LOG: all (default for review), miss, or off.
// Legacy values 1, true, yes, and on map to all. Default changed to all so hits/misses
// are logged by default for recent review of compaction effectiveness.
func LogLevelFromEnv() LogLevel {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REASONIX_RTK_LOG"))) {
	case "off", "0", "false", "no":
		return LogOff
	case "miss", "decline", "declines":
		return LogMiss
	default:
		// default to all (was off) to capture all hits and misses for review
		return LogAll
	}
}

func logMiss(surface, detail string) {
	if LogLevelFromEnv() < LogMiss {
		return
	}
	slog.Info("rtk status=miss", "surface", surface, "detail", detail)
}

func logHit(cmd, rewritten string) {
	if LogLevelFromEnv() < LogAll {
		return
	}
	slog.Info("rtk status=hit", "cmd", cmd, "rewritten", rewritten)
}

// LogFail records a failed RTK invocation (binary error, timeout, etc).
func LogFail(surface, cmd string, err error) {
	if LogLevelFromEnv() < LogMiss {
		return
	}
	slog.Warn("rtk status=fail", "surface", surface, "cmd", cmd, "error", err)
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