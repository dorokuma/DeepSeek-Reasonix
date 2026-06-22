package hook

import (
	"context"
	"encoding/json"
	"strings"

	"reasonix/internal/jobs"
	"reasonix/internal/rtk"
)

const rtkPipeThreshold = 32 * 1024

// NewRTKRewriter creates a PostToolRewriter for RTK pipe compaction.
func NewRTKRewriter(jm *jobs.Manager) *rtkRewriter {
	return &rtkRewriter{jm: jm}
}

type rtkRewriter struct {
	jm *jobs.Manager
}

func (r *rtkRewriter) PostToolRewrite(_ context.Context, name string, args json.RawMessage, result string) string {
	filter := rtkPipeFilter(name, args, r.jm)
	if filter == "" {
		rtk.LogMissPipe(name, "", len(result), "no_pipe_filter")
		return result
	}
	if len(result) < rtkPipeThreshold {
		return result
	}
	out, err := rtk.PipeCompact(filter, result)
	if err != nil {
		rtk.LogFail("pipe", name, err)
		rtk.LogMissPipe(name, filter, len(result), "pipe_declined")
		return result
	}
	if len(out) >= len(result) {
		rtk.LogMissPipe(name, filter, len(result), "pipe_no_shrink")
		return result
	}
	return out
}

// rtkPipeFilter determines the rtk pipe filter for a tool call.
func rtkPipeFilter(name string, args json.RawMessage, jm *jobs.Manager) string {
	switch name {
	case "bash", "bash_output", "wait":
		cmd := extractShellCommand(name, args, jm)
		if filter, ok := rtk.PipeFilterForShell(cmd); ok {
			return filter
		}
		return ""
	case "grep":
		return "grep"
	default:
		return ""
	}
}

// extractShellCommand pulls the shell command string from tool arguments.
func extractShellCommand(name string, args json.RawMessage, jm *jobs.Manager) string {
	switch name {
	case "bash":
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &p) == nil {
			return p.Command
		}
	case "bash_output":
		var p struct {
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(args, &p) == nil && p.JobID != "" && jm != nil {
			if label, ok := jm.Label(p.JobID); ok && strings.TrimSpace(label) != "" {
				return label
			}
		}
	case "wait":
		var p struct {
			JobIDs []string `json:"job_ids"`
		}
		if len(args) > 0 && json.Unmarshal(args, &p) == nil && len(p.JobIDs) == 1 && jm != nil {
			if label, ok := jm.Label(p.JobIDs[0]); ok && strings.TrimSpace(label) != "" {
				return label
			}
		}
	}
	return ""
}
