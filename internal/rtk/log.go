package rtk

import (
	"fmt"
	"log/slog"
)

func logMiss(surface, detail string) {
	slog.Info("rtk status=miss", "surface", surface, "detail", detail)
}

func LogHit(cmd, rewritten string) {
	slog.Info("rtk status=hit", "cmd", cmd, "rewritten", rewritten)
}

// LogFail records a failed RTK invocation (binary error, timeout, etc).
func LogFail(surface, cmd string, err error) {
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
