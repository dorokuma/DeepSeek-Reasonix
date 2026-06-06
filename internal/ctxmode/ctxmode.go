// Package ctxmode keeps large tool results out of the model context window.
// Oversized read_file, grep, and MCP output is stored in a session-local
// sidecar; the model sees a short summary plus a ref for ctx_read/ctx_search.
package ctxmode

import (
	"os"
	"strconv"
	"strings"
)

const defaultThresholdBytes = 8 * 1024

// Active reports whether outbound sandboxing is enabled.
func Active() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REASONIX_CTX"))) {
	case "off", "0", "false", "no":
		return false
	default:
		return true
	}
}

// ThresholdBytes is the minimum tool result size before sandboxing applies.
func ThresholdBytes() int {
	v := strings.TrimSpace(os.Getenv("REASONIX_CTX_THRESHOLD"))
	if v == "" {
		return defaultThresholdBytes
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultThresholdBytes
	}
	return n
}