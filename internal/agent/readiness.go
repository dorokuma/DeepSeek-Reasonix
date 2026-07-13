package agent

import (
	"fmt"
	"strings"

	"reasonix/internal/evidence"
	"reasonix/internal/instruction"
)

type finalReadinessCheck struct {
	applies              bool
	reason               string
	missingProjectChecks int
	incompleteTodos      int
}

func (c finalReadinessCheck) audit(result evidence.ReadinessAuditResult, recovered bool) evidence.ReadinessAudit {
	return evidence.ReadinessAudit{
		Result:                 result,
		Recovered:              recovered,
		MissingProjectChecks:   c.missingProjectChecks,
		IncompleteTodos:        c.incompleteTodos,
		CommandMismatchMissing: c.missingProjectChecks,
	}
}

func (a *Agent) finalReadinessCheck() finalReadinessCheck {
	var missing []string
	out := finalReadinessCheck{}
	hasProjectChecks := len(a.projectChecks) > 0
	if !hasProjectChecks {
		if len(missing) > 0 {
			out.reason = strings.Join(missing, "; ")
		}
		return out
	}
	out.applies = true
	for _, check := range a.projectChecks {
		command := strings.TrimSpace(check.Command)
		if command == "" {
			continue
		}
		out.missingProjectChecks++
		missing = append(missing, fmt.Sprintf("run %q from %s after the latest write", command, finalReadinessCheckSource(check)))
	}

	if len(missing) == 0 {
		return out
	}
	out.reason = strings.Join(missing, "; ")
	return out
}

func finalReadinessCheckSource(check instruction.VerifyCheck) string {
	source := strings.TrimSpace(check.SourcePath)
	if source == "" {
		source = "project memory"
	}
	if check.Line > 0 {
		return fmt.Sprintf("%s:%d", source, check.Line)
	}
	return source
}

func finalReadinessRetryMessage(reason string) string {
	return "Readiness failed: " + reason + ". Use tools, then answer."
}

func executorHandoffRetryMessage() string {
	return `Executor phase: use tools now. If blocked, state the blocker and ask approval.`
}

// orchestratorIdleRetryMessage is injected when the root agent (main_agent_allowed
// whitelist) returns a final answer without any tool call and without a clear
// confirm/clarify question. Mechanism enforces "orchestrator must act", not prompt ban-lists.
func orchestratorIdleRetryMessage() string {
	return `Root: ask/对吗 if unclear; else spawn_agent then wait_agent. run_skill alone ≠ done. Do not refuse.`
}

// looksLikeCapabilityRefuse: model claims it cannot act (not a real confirm).
// Mechanism: such finals never count as legitimate no-tool exits for the root.
func looksLikeCapabilityRefuse(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	// Chinese capability / tool excuses
	for _, p := range []string{"无法", "不能", "没有可用", "没有工具", "不具备", "做不到", "没法"} {
		if strings.Contains(t, p) {
			return true
		}
	}
	for _, p := range []string{"cannot", "can't", "unable to", "no access", "don't have", "do not have", "i have no", "no tool"} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// isOrchestratorActionTool: tools that count as "actually did orchestrator work".
// run_skill / recall / note alone do not — they only load text or notes.
func isOrchestratorActionTool(name string) bool {
	switch name {
	case "spawn_agent", "wait_agent", "ask", "followup_task", "send_message", "interrupt_agent":
		return true
	default:
		return false
	}
}

// looksLikeConfirmOrClarify reports user-facing confirm/clarify replies that
// legitimately use no tools (orchestrator step 3.1).
func looksLikeConfirmOrClarify(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if looksLikeCapabilityRefuse(t) {
		return false
	}
	if strings.Contains(t, "对吗") || strings.Contains(t, "对不对") || strings.Contains(t, "是否") {
		return true
	}
	runes := []rune(t)
	if len(runes) > 240 {
		return false
	}
	return strings.HasSuffix(t, "？") || strings.HasSuffix(t, "?")
}

func hasVisibleFinalAnswer(text, reasoning string) bool {
	return strings.TrimSpace(text) != "" || strings.TrimSpace(reasoning) != ""
}

func emptyFinalRetryMessage() string {
	return "No visible answer. Continue and give a short user-facing reply."
}

func streamRecoveryMessage(hasPartialText, hadPartialTool bool) string {
	switch {
	case hadPartialTool:
		return "Stream cut mid-tool. Retry a full tool call if still needed."
	case hasPartialText:
		return "Stream cut. Continue after partial text; do not repeat it."
	default:
		return "Stream cut before answer. Continue the task."
	}
}

// stream runs one completion, emitting reasoning and text deltas as typed
// events and collecting complete tool calls. A Message event closes the text
// stream so a sink can re-render the streamed raw text as styled markdown. The
// accumulated text and reasoning are also returned so the caller can round-trip
// reasoning on the next turn.
