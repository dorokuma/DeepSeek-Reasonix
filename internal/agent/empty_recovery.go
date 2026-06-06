package agent

import (
	"regexp"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

const (
	maxThinkingPrefillRetries = 2
	maxEmptyContentRetries    = 3
)

const emptyResponseUserMessage = "⚠️ 模型处理完成，但未生成可见回复。请再发一次或换种问法。"

const postToolEmptyNudge = "You just executed tool calls but returned an empty response. Please process the tool results above and continue with the task."

var inlineThinkPattern = regexp.MustCompile(`(?i)<(?:think|thinking|reasoning|REASONING_SCRATCHPAD)[^>]*>`)

type emptyRecoveryState struct {
	thinkingPrefillRetries int
	emptyContentRetries    int
	postToolEmptyRetried   bool
}

type emptyRecoveryAction int

const (
	emptyRecoveryNone emptyRecoveryAction = iota
	emptyRecoveryContinuePrefill
	emptyRecoveryContinueNudge
	emptyRecoveryContinueRetry
	emptyRecoveryExhausted
)

func hasVisibleAssistantText(text string) bool {
	return strings.TrimSpace(stripInlineThinkBlocks(text)) != ""
}

func hasStructuredReasoning(text, reasoning string) bool {
	return strings.TrimSpace(reasoning) != "" || inlineThinkPattern.MatchString(text)
}

func stripInlineThinkBlocks(s string) string {
	for _, pair := range [][2]string{
		{"<REASONING_SCRATCHPAD>", "</REASONING_SCRATCHPAD>"},
		{"<think>", "</think>"},
		{"<reasoning>", "</reasoning>"},
		{"<thinking>", "</thinking>"},
	} {
		open, close := pair[0], pair[1]
		for {
			start := strings.Index(s, open)
			if start < 0 {
				break
			}
			rest := s[start+len(open):]
			end := strings.Index(rest, close)
			if end < 0 {
				s = s[:start]
				break
			}
			s = s[:start] + rest[end+len(close):]
		}
	}
	return s
}

func priorWasTool(msgs []provider.Message) bool {
	n := len(msgs)
	start := n - 5
	if start < 0 {
		start = 0
	}
	for i := n - 1; i >= start; i-- {
		if msgs[i].Role == provider.RoleTool {
			return true
		}
	}
	return false
}

func decideEmptyRecovery(st *emptyRecoveryState, text, reasoning string, msgs []provider.Message) (emptyRecoveryAction, string) {
	if hasVisibleAssistantText(text) {
		return emptyRecoveryNone, ""
	}

	hasInlineThinking := inlineThinkPattern.MatchString(text)
	hasStructured := hasStructuredReasoning(text, reasoning)

	if priorWasTool(msgs) && !st.postToolEmptyRetried && !hasInlineThinking {
		st.postToolEmptyRetried = true
		return emptyRecoveryContinueNudge, "模型在工具执行后返回空回复，正在请求继续处理…"
	}

	if hasStructured && st.thinkingPrefillRetries < maxThinkingPrefillRetries {
		st.thinkingPrefillRetries++
		return emptyRecoveryContinuePrefill, "模型只返回了思考过程，正在续写可见回复 (" + itoa(st.thinkingPrefillRetries) + "/" + itoa(maxThinkingPrefillRetries) + ")…"
	}

	prefillExhausted := hasStructured && st.thinkingPrefillRetries >= maxThinkingPrefillRetries
	if (hasStructured && prefillExhausted) || !hasStructured {
		if st.emptyContentRetries < maxEmptyContentRetries {
			st.emptyContentRetries++
			return emptyRecoveryContinueRetry, "模型返回空回复，正在重试 (" + itoa(st.emptyContentRetries) + "/" + itoa(maxEmptyContentRetries) + ")…"
		}
	}

	return emptyRecoveryExhausted, ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// trimEmptyResponseScaffolding removes trailing assistant turns that only exist
// for empty-response recovery (prefill / retry) so the durable transcript ends
// with the real final answer.
func (a *Agent) trimEmptyResponseScaffolding() {
	msgs := a.session.Snapshot()
	changed := false
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role != provider.RoleAssistant {
			break
		}
		if len(last.ToolCalls) > 0 {
			break
		}
		if strings.TrimSpace(last.Content) != "" {
			break
		}
		msgs = msgs[:len(msgs)-1]
		changed = true
	}
	if changed {
		a.session.Replace(msgs)
	}
}

func (a *Agent) emitEmptyResponseFallback() {
	a.sink.Emit(eventNotice(emptyResponseUserMessage))
	a.sink.Emit(eventText(emptyResponseUserMessage))
	a.sink.Emit(eventMessage(emptyResponseUserMessage))
}

// event* helpers avoid importing event in every callsite pattern — kept local.
func eventNotice(text string) event.Event {
	return event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: text}
}

func eventText(text string) event.Event {
	return event.Event{Kind: event.Text, Text: text}
}

func eventMessage(text string) event.Event {
	return event.Event{Kind: event.Message, Text: text}
}