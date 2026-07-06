package agent

import (
	"strings"

	"reasonix/internal/provider"
)

const backgroundWakeUserNudge = "后台任务结果已写入会话末尾的 task 工具消息。请用简要中文向用户汇报完成情况，勿复读子代理全文。"

func (a *Agent) maybeNudgeBackgroundWake(wakeForBackground bool, step int) {
	if !wakeForBackground || step != 0 || a.session == nil {
		return
	}
	if a.latestBackgroundDeliveryBody() == "" {
		return
	}
	a.session.Add(provider.Message{Role: provider.RoleUser, Content: backgroundWakeUserNudge})
}

func (a *Agent) latestBackgroundDeliveryBody() string {
	if a.session == nil {
		return ""
	}
	for i := len(a.session.Snapshot()) - 1; i >= 0; i-- {
		m := a.session.Snapshot()[i]
		if m.Role != provider.RoleTool || m.Name != "task" {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" || IsStartedTaskPlaceholder(c) {
			continue
		}
		return c
	}
	return ""
}
