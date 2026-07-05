package agent

import (
	"regexp"
	"strings"

	"reasonix/internal/provider"
)

var backgroundTaskResultRE = regexp.MustCompile(`(?s)<background-task-result job="[^"]*">\n(.*)\n</background-task-result>`)

func (a *Agent) surfaceBackgroundHandoffIfNeeded(wakeForBackground bool, assistantText string) string {
	if !wakeForBackground || hasVisibleFinalAnswer(assistantText) {
		return assistantText
	}
	if body := a.latestBackgroundDeliveryBody(); body != "" {
		const max = 8000
		if len(body) > max {
			body = body[:max] + "\n…[truncated]"
		}
		return "后台子代理已完成，结果如下：\n\n" + body
	}
	return assistantText
}

func (a *Agent) latestBackgroundDeliveryBody() string {
	if a.session == nil {
		return ""
	}
	a.session.mu.RLock()
	msgs := a.session.Messages
	a.session.mu.RUnlock()
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == provider.RoleUser {
			if sub := backgroundTaskResultRE.FindStringSubmatch(m.Content); len(sub) > 1 {
				return strings.TrimSpace(sub[1])
			}
		}
		if m.Role == provider.RoleTool && m.Name == "task" && !strings.HasPrefix(strings.TrimSpace(m.Content), "Started task ") {
			return strings.TrimSpace(m.Content)
		}
	}
	return ""
}
