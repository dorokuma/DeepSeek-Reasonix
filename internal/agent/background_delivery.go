package agent

import (
	"log/slog"
	"strings"
	"time"

	"reasonix/internal/jobs"
)

// CompleteBackgroundJob delivers one finished job into the session by id. The
// controller calls this from the global completion hook so delivery does not
// depend on drain timing or resultCh reads. Returns true when output was committed.
func (a *Agent) CompleteBackgroundJob(jobID string) bool {
	if a.jobs == nil || jobID == "" {
		return false
	}
	for attempt := 0; attempt < 16; attempt++ {
		if id, out, ok := a.tryDeliverJobResult(jobID); ok {
			return a.commitBackgroundJobResult(id, out)
		}
		if attempt < 15 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return false
}

// HasUndeliveredBackgroundResults reports whether any completed job still has a
// non-empty terminal result in the manager that is not yet removed.
func (a *Agent) HasUndeliveredBackgroundResults() bool {
	if a.jobs == nil {
		return false
	}
	for _, id := range a.jobs.ActiveJobs() {
		if n, ok := a.jobs.CompletedResult(id); ok && strings.TrimSpace(n.Output) != "" {
			return true
		}
	}
	return false
}

func (a *Agent) tryDeliverJobResult(jobID string) (id, output string, ok bool) {
	jm := a.jobs
	if jm == nil {
		return "", "", false
	}
	ch := jm.NotifyChannels(jobID)
	if ch == nil {
		if n, have := jm.CompletedResult(jobID); have && strings.TrimSpace(n.Output) != "" {
			return jobID, n.Output, true
		}
		return "", "", false
	}
	var notify jobs.JobNotify
	var have bool
	select {
	case n, okCh := <-ch.Result:
		if okCh && n.Type == "result" && strings.TrimSpace(n.Output) != "" {
			notify, have = n, true
		} else if okCh && n.Type == "result" {
			// Channel had a result envelope but empty output — fall back to stored result.
			if n2, okRes := jm.CompletedResult(jobID); okRes && strings.TrimSpace(n2.Output) != "" {
				notify, have = n2, true
			}
		}
	default:
		if n, okRes := jm.CompletedResult(jobID); okRes && strings.TrimSpace(n.Output) != "" {
			notify, have = n, true
		}
	}
	if !have {
		return "", "", false
	}
	return notify.JobID, notify.Output, true
}

func (a *Agent) commitBackgroundJobResult(jobID, output string) bool {
	output = strings.TrimSpace(output)
	if output == "" || a.jobs == nil {
		return false
	}
	toolCallID := ""
	if a.ctrl != nil {
		toolCallID, _ = a.ctrl.TakeJobMeta(jobID)
	}
	if toolCallID == "" {
		toolCallID = a.session.ToolCallIDForStartedTaskLine(jobID)
	}
	if toolCallID == "" {
		slog.Warn("background job result without tool call correlation", "job", jobID, "output_len", len(output))
		return false
	}
	a.deliverBackgroundToolResult(toolCallID, output)
	a.AppendBackgroundTaskResultDelivery(jobID, output)
	a.jobs.RemoveJob(jobID)
	return true
}

func (a *Agent) clearBackgroundWakeIfCaughtUp() {
	if a.ctrl == nil {
		return
	}
	if !a.HasUndeliveredBackgroundResults() {
		a.ctrl.PendingToolResultCAS(true, false)
	}
}
