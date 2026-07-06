package rtk

import (
	"strconv"
)

// EnvDoc describes one REASONIX_RTK* environment variable.
type EnvDoc struct {
	Name        string
	Default     string
	Description string
}

// EnvDocs returns the RTK-related environment variables Reasonix understands.
func EnvDocs() []EnvDoc {
	return []EnvDoc{
		{
			Name:        "REASONIX_RTK",
			Default:     "rewrite",
			Description: "Integration mode: rewrite (transparent compaction), suggest (log only), or off.",
		},
		{
			Name:        "REASONIX_RTK_TIMEOUT",
			Default:     "3s",
			Description: "Timeout for rtk rewrite probes and gated shell runs. Accepts a Go duration (e.g. 5s, 500ms) or plain seconds (e.g. 10).",
		},
		{
			Name:        "REASONIX_RTK_READ_LIMIT",
			Default:     "800 (rewrite mode) / 2000 (off)",
			Description: "Default read_file line cap when limit is unset. Only applies in rewrite mode unless explicitly set while off.",
		},
	}
}

// EnvSnapshot reports the effective RTK env values for doctor and debugging.
func EnvSnapshot() map[string]string {
	mode := ModeFromEnv().String()
	timeout := rewriteTimeout().String()
	readLimit := strconv.Itoa(ReadFileDefaultLimit())
	snap := map[string]string{
		"REASONIX_RTK":            mode,
		"REASONIX_RTK_TIMEOUT":    timeout,
		"REASONIX_RTK_READ_LIMIT": readLimit,
	}
	return snap
}
