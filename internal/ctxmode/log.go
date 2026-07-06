package ctxmode

import (
	"log/slog"
)

// LogMissStore records a sandbox store failure.
func LogMissStore(tool string, bytes int, err error) {
	slog.Debug("ctx miss", "surface", "store", "tool", tool, "bytes", bytes, "err", err)
}

// LogHitSandbox records a successful outbound sandbox.
func LogHitSandbox(tool, ref string, bytes int) {
	slog.Debug("ctx hit", "surface", "sandbox", "tool", tool, "ref", ref, "bytes", bytes)
}

// LogJournalErr records a journal persistence failure.
func LogJournalErr(op string, err error) {
	if err == nil {
		return
	}
	slog.Warn("ctx journal", "op", op, "err", err)
}
