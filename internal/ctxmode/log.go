package ctxmode

import (
	"log/slog"
	"os"
	"strings"
)

// LogLevel controls ctxmode diagnostic logging (to the global slog handler
// when enabled via REASONIX_CTX_LOG).
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

// LogMissStore records a sandbox store failure.
func LogMissStore(tool string, bytes int, err error) {
	if LogLevelFromEnv() < LogMiss {
		return
	}
	slog.Info("ctx miss", "surface", "store", "tool", tool, "bytes", bytes, "err", err)
}

// LogHitSandbox records a successful outbound sandbox.
func LogHitSandbox(tool, ref string, bytes int) {
	if LogLevelFromEnv() < LogAll {
		return
	}
	slog.Info("ctx hit", "surface", "sandbox", "tool", tool, "ref", ref, "bytes", bytes)
}

// LogJournalErr records a journal persistence failure.
// Errors are always logged (via slog.Warn) so that persistence problems
// (disk full, permission, sqlite corruption) are visible even when
// REASONIX_CTX_LOG=off or miss. The env only gates hit/miss diagnostics.
func LogJournalErr(op string, err error) {
	if err == nil {
		return
	}
	slog.Warn("ctx journal", "op", op, "err", err)
}