package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"reasonix/internal/ctxmode"
	"reasonix/internal/jobs"
	"reasonix/internal/rtk"
	"reasonix/internal/tool"
)

// compactToolOutput shrinks oversize tool output before it enters model context.
// RTK pipe compacts semantically first; ctxmode sandboxes the full original for
// paging; truncation is the last resort.
func compactToolOutput(store *ctxmode.Store, toolName string, args json.RawMessage, jm *jobs.Manager, body string) (string, string) {
	original := body
	compactBody, pipeNotice, piped := tryRTKPipe(toolName, args, jm, body)

	// CTX keeps the full original; model may see the RTK-compacted inline view.
	if summary, notice, ok := ctxmode.TransformCooperative(store, toolName, args, original, compactBody, pipeNotice, piped, maxToolOutputBytes); ok {
		if len(summary) <= maxToolOutputBytes {
			return summary, notice
		}
		body = summary
	} else if piped {
		body = compactBody
		if len(body) <= maxToolOutputBytes {
			return body, ""  // suppress rtk pipe notice from chat (only log); model gets compact in body
		}
	}

	if len(body) <= maxToolOutputBytes {
		return body, ""
	}

	// Non-sandbox tools (bash): pipe may not have run yet below ctx threshold.
	if !piped {
		if compacted, notice, ok := tryRTKPipe(toolName, args, jm, body); ok {
			if len(compacted) <= maxToolOutputBytes {
				return compacted, ""  // suppress rtk pipe notice from chat (only log via slog + REASONIX_RTK_LOG)
			}
			truncated, truncMsg := truncateToolOutput(compacted)
			if truncMsg != "" {
				return truncated, notice + "; " + truncMsg  // note: truncMsg now "" after our trunc suppress
			}
			return truncated, ""
		}
	}

	return truncateToolOutput(body)
}

func tryRTKPipe(toolName string, args json.RawMessage, jm *jobs.Manager, body string) (compacted, notice string, ok bool) {
	filter, hasFilter := pipeFilterHint(toolName, args, jm)
	if !hasFilter {
		if rtk.Active() && len(body) > ctxmode.ThresholdBytes() {
			rtk.LogMissPipe(toolName, "", len(body), "no_pipe_filter")
		}
		return "", "", false
	}
	// Pipe when output is large enough to benefit (ctx threshold) or over hard cap.
	if len(body) < ctxmode.ThresholdBytes() && len(body) <= maxToolOutputBytes {
		return "", "", false
	}
	out, err := rtk.PipeCompact(filter, body)
	if err != nil {
		rtk.LogMissPipe(toolName, filter, len(body), "pipe_declined")
		return "", "", false
	}
	if len(out) >= len(body) {
		rtk.LogMissPipe(toolName, filter, len(body), "pipe_no_shrink")
		return "", "", false
	}
	slog.Info("rtk pipe compact", "tool", toolName, "filter", filter, "bytes_in", len(body), "bytes_out", len(out))
	notice = fmt.Sprintf("rtk pipe (%s): %d→%d bytes", filter, len(body), len(out))
	return out, notice, true
}

func pipeFilterHint(toolName string, args json.RawMessage, jm *jobs.Manager) (string, bool) {
	if strings.HasPrefix(toolName, tool.MCPNamePrefix) {
		return "", false
	}
	switch toolName {
	case "grep":
		return "grep", true
	case "bash":
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &p) != nil || strings.TrimSpace(p.Command) == "" {
			return "", false
		}
		return rtk.PipeFilterForShell(p.Command)
	case "bash_output":
		var p struct {
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(args, &p) != nil || p.JobID == "" || jm == nil {
			return "", false
		}
		label, ok := jm.Label(p.JobID)
		if !ok || strings.TrimSpace(label) == "" {
			return "", false
		}
		return rtk.PipeFilterForShell(label)
	case "wait":
		var p struct {
			JobIDs []string `json:"job_ids"`
		}
		if len(args) > 0 && json.Unmarshal(args, &p) == nil && len(p.JobIDs) == 1 && jm != nil {
			if label, ok := jm.Label(p.JobIDs[0]); ok && strings.TrimSpace(label) != "" {
				return rtk.PipeFilterForShell(label)
			}
		}
		return "", false
	default:
		return "", false
	}
}