package agent

import (
	"encoding/json"
	"strings"

	"reasonix/internal/jobs"
	"reasonix/internal/rtk"
	"reasonix/internal/tool"
)

// compactToolOutput shrinks oversize tool output before it enters model context.
// Order: rtk pipe when a safe filter is known, else head/tail truncation.
func compactToolOutput(toolName string, args json.RawMessage, jm *jobs.Manager, body string) (string, string) {
	if len(body) <= maxToolOutputBytes {
		return body, ""
	}
	if filter, ok := pipeFilterHint(toolName, args, jm); ok {
		if compacted, err := rtk.PipeCompact(filter, body); err == nil {
			notice := "tool output compacted via rtk pipe (" + filter + ")"
			if len(compacted) <= maxToolOutputBytes {
				return compacted, notice
			}
			truncated, truncMsg := truncateToolOutput(compacted)
			if truncMsg != "" {
				return truncated, notice + "; " + truncMsg
			}
			return truncated, notice
		}
		rtk.LogMissPipe(toolName, filter, len(body), "pipe_declined")
	} else if rtk.Active() {
		rtk.LogMissPipe(toolName, "", len(body), "no_pipe_filter")
	}
	return truncateToolOutput(body)
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