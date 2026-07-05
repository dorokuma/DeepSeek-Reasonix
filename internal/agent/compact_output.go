package agent

import (
	"encoding/json"

	"reasonix/internal/ctxmode"
)

// compactToolOutput shrinks oversize tool output before it enters model context.
// RTK pipe compaction now happens earlier via the PostToolRewrite hook.
// ctxmode sandboxes the full original for paging; truncation is the last resort.
func compactToolOutput(store *ctxmode.Store, toolName string, args json.RawMessage, body string) (string, string) {
	// CTX keeps the full original; model may see a summary with ctx_read ref.
	if summary, notice, ok := ctxmode.TransformCooperative(store, toolName, args, body, "", "", false, maxToolOutputBytes); ok {
		if len(summary) <= maxToolOutputBytes {
			return summary, notice
		}
		body = summary
	}

	if len(body) <= maxToolOutputBytes {
		return body, ""
	}

	return truncateToolOutput(body)
}
