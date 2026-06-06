package agent

import (
	"strings"

	"reasonix/internal/provider"
)

const (
	maxVisibilityRetries = 2
)

const visibilityNudge = "Your last reply said content is 'above', 'loaded in the system prompt', or summarized a file/rules without pasting the actual text. " +
	"The user cannot see tool results, the system prompt, or memory — only your assistant message counts as visible. " +
	"Answer again and include the full text they asked for in your message (quote or fenced block). " +
	"Do not say 'above', 'as shown', '内容如上', or '都在上面'."

// phantomVisibilityPhrases match replies that point at hidden context instead of quoting.
var phantomVisibilityPhrases = []string{
	"都在上面", "内容如上", "已在上面", "内容都在上面", "都在上面了",
	"系统提示中已加载", "已加载的规则", "当前系统提示中",
	"as above", "as shown", "already displayed", "already loaded",
	"loaded above", "shown above", "in the system prompt",
}

type visibilityRecoveryState struct {
	retries int
}

type visibilityRecoveryAction int

const (
	visibilityRecoveryNone visibilityRecoveryAction = iota
	visibilityRecoveryContinueNudge
)

func lastUserMessage(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// responseQuotesSubstantiveContent reports whether the assistant message actually
// carries file/rule body the user can read, not just a meta summary.
func responseQuotesSubstantiveContent(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.Contains(text, "```") {
		return true
	}
	// Inline code / markdown samples (e.g. Telegram format tests) count as visible body.
	if strings.Contains(text, "`") && len([]rune(trimmed)) >= 24 {
		return true
	}
	headerLines := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			headerLines++
		}
	}
	if headerLines >= 2 {
		return true
	}
	// Common markers when REASONIX.md body is pasted verbatim.
	for _, marker := range []string{"**零自主**", "# 铁律", "# 身份", "**先叫爸爸**"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func claimsContentIsElsewhere(text string) bool {
	lower := strings.ToLower(text)
	for _, p := range phantomVisibilityPhrases {
		if strings.Contains(text, p) || strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func userRequestedVisibleContent(user string) bool {
	u := strings.ToLower(strings.TrimSpace(user))
	// Explicit "show me the file/rules" intent only — not "测试内容", "发内容", or casual "看看".
	for _, k := range []string{
		"内容是什么", "什么内容", "全文", "完整内容", "原文", "原样贴", "贴出来",
		"给我看", "展示给", "展示一下",
		"reasonix.md", "全局规则", "规则文件", "规则是什么", "什么规则",
		"show me the", "paste the", "contents of", "full text of",
		"文件内容",
	} {
		if strings.Contains(u, k) {
			return true
		}
	}
	if strings.Contains(u, "读取") || strings.Contains(u, "读一下") {
		return strings.Contains(u, "文件") || strings.Contains(u, "规则") || strings.Contains(u, ".md")
	}
	return false
}

func decideVisibilityRecovery(st *visibilityRecoveryState, text, lastUser string) (visibilityRecoveryAction, string) {
	if st.retries >= maxVisibilityRetries || !hasVisibleAssistantText(text) {
		return visibilityRecoveryNone, ""
	}
	quoted := responseQuotesSubstantiveContent(text)
	needRetry := (claimsContentIsElsewhere(text) && !quoted) ||
		(userRequestedVisibleContent(lastUser) && !quoted)
	if !needRetry {
		return visibilityRecoveryNone, ""
	}
	st.retries++
	return visibilityRecoveryContinueNudge,
		"回复未向用户展示正文，正在要求粘贴实际内容 (" + itoa(st.retries) + "/" + itoa(maxVisibilityRetries) + ")…"
}