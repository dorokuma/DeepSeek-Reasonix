package openai

import "reasonix/internal/provider"

// needsReasoningRoundTrip reports whether an assistant message must carry
// reasoning_content back to DeepSeek thinking-mode APIs.
//
// DeepSeek rejects multi-turn tool/agent sessions with HTTP 400 when
// reasoning_content is missing on tool-call turns — including when the model
// returned an empty string (community repro ~59%). Pure chat turns between user
// messages may omit reasoning; see api-docs.deepseek.com/guides/thinking_mode.
func needsReasoningRoundTrip(msgs []provider.Message, idx int) bool {
	if idx < 0 || idx >= len(msgs) {
		return false
	}
	m := msgs[idx]
	if m.Role != provider.RoleAssistant {
		return false
	}
	if len(m.ToolCalls) > 0 {
		return true
	}
	// Final answer after tool results in the same user episode still rides in
	// the tool-call chain and must round-trip its reasoning block.
	for j := idx - 1; j >= 0; j-- {
		switch msgs[j].Role {
		case provider.RoleUser:
			return false
		case provider.RoleTool:
			return true
		case provider.RoleAssistant:
			if len(msgs[j].ToolCalls) > 0 {
				return true
			}
		}
	}
	return false
}
