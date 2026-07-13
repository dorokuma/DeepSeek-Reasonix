package hook

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/rtk"
)

const rtkPipeThreshold = 32 * 1024

// NewRTKRewriter creates a PostToolRewriter for RTK pipe compaction.
func NewRTKRewriter() *rtkRewriter {
	return &rtkRewriter{}
}

type rtkRewriter struct{}

func (r *rtkRewriter) PostToolRewrite(_ context.Context, name string, args json.RawMessage, result string) string {
	filter := rtkPipeFilter(name, args)
	if filter == "" {
		rtk.LogMissPipe(name, "", len(result), "no_pipe_filter")
		return result
	}
	rtk.LogHit(filter, fmt.Sprintf("tool=%s bytes=%d below_threshold", name, len(result)))
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
	rtk.LogHit(filter, fmt.Sprintf("tool=%s compacted %d->%d", name, len(result), len(out)))
	return out
}

// rtkPipeFilter determines the rtk pipe filter for a tool call.
func rtkPipeFilter(name string, args json.RawMessage) string {
	switch name {
	case "bash":
		cmd := extractShellCommand(args)
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

// extractShellCommand pulls the shell command string from bash tool arguments.
func extractShellCommand(args json.RawMessage) string {
	var p struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &p) == nil {
		return p.Command
	}
	return ""
}
