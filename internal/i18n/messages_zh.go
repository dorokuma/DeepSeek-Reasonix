package i18n

// Chinese is the zh-Hans catalogue. Keep the %s placeholders in the same order
// as English unless a phrase genuinely demands re-ordering — call sites pass
// arguments positionally and won't reshuffle.
var Chinese = Messages{
	Subtitle:        "配置与插件驱动的 coding agent",
	WelcomeTitleFmt: "欢迎使用 %s",
	NoConfigYet:     "还没有配置 — 现在来设置一下吧。",
	StartingChatFmt: "正在启动 %s…",
	SetKeyHint:      "设置好 API key 后运行 `reasonix chat`。",
	ConfigLabel:     "配置",
	ModelsLabel:     "模型",
	ConfigNotFound:  "未找到 — 使用内置默认值",
	ConfigErrorFmt:  "%s — 错误：%v",
	NoKey:           "未设置 key",
	Ready:           "已就绪",
	GetStarted:      "开始使用",
	StepScaffold:    "生成 reasonix.toml",
	StepSetKey:      "设置 API key",
	StepSetKeyHint:  "执行 export DEEPSEEK_API_KEY=… 或写入 .env",
	StepChatDesc:    "交互式会话",
	StepRunDesc:     "执行单次任务",
	HelpFooter:      "reasonix help · 查看全部命令",

	ChatTip:           "对话上下文将跨轮保留。输入 'exit' 或按 Ctrl-D 退出。",
	TurnCancelled:     "已取消 — 回到提示符",
	NoSessionToResume: "没有可恢复的会话 — 用 `reasonix chat` 开一个新的",
	ResumeRequiresTTY: "--resume 需要交互式终端；用 --continue 直接恢复最近一次",
	PickSessionLabel:  "恢复哪个会话？",

	ChatStatusThinkingFmt:  "%s 思考中… (%d 秒 · Esc 取消)",
	ChatStatusIdle:         "Tab 切换 plan · Enter 发送 · Esc 退出当前状态 · PgUp/PgDn 滚动 · Ctrl-D 退出",
	ChatStatusPlanApproval: "Enter 同意 · 输入文本反馈以让 AI 修改 · Esc 撤掉横幅 · PgUp/PgDn 滚动",
	PlanApprovalPrompt:     "计划已生成（看上方）— 回车通过，或输入修改建议",
	ChatStatusToolApproval: "y 同意一次 · a 本会话允许 · n 拒绝 · Ctrl-C 取消本轮",
	ToolApprovalPromptFmt:  "允许 %s%s？— [y] 本次 · [a] 本会话 · [n] 拒绝",

	SlashCompactDone:   "已压缩 — 旧的中段换成一段摘要，最近几轮保留原样",
	SlashCompactFailed: "压缩失败",
	SlashNewDone:       "已新建会话 — 之前的对话已存档",
	SlashNewFailed:     "新建会话失败",
	SlashUnavailable:   "当前构建不支持该命令",
	SlashUnknown:       "未知命令",
	SlashHelp:          "命令：/compact（手动压缩上下文）· /new（开新会话）· /mcp（MCP 服务器）· /help",
	SlashPromptEmpty:   "该 MCP prompt 没有返回可发送的内容",
	SlashMCPNone:       "没有配置 MCP 服务器 — 在 reasonix.toml 加一个 [[plugins]] 条目",
	CompHintSlash:      "↑/↓ 移动 · Tab/Enter 选中 · Esc 关闭",
	CompHintFile:       "↑/↓ 移动 · Tab/Enter 进入文件夹或选中文件 · Esc 关闭",

	SelectProvidersLabel:  "选择要启用的 provider",
	EnterAPIKeysHeader:    "输入 API key（回车跳过、稍后写入 .env）：",
	MissingKeyIntro:       "reasonix.toml 已配置好 — 只差一个 API key 就可以开始。",
	WroteFileFmt:          "已写入 %s",
	SetupComplete:         "设置完成。",
	SetupCancelled:        "设置已取消。",
	TryHintFmt:            "试试: %s",
	NextHint:              "下一步：设置 API key（export DEEPSEEK_API_KEY=... 或写入 .env），然后运行 `reasonix run \"你的任务\"`。",
	ConfirmReconfigureFmt: "%s 已存在。重新配置并覆盖？",
	KeepingExisting:       "保留原配置不变。",
	NotOverwritingFmt:     "%s 已存在，不覆盖",

	UnknownCommandFmt: "未知命令 %q",
	UsageRunHint:      "用法：reasonix run [--model NAME] <task>",
	ErrorPrefix:       "错误：",
	WriteConfigErr:    "写入配置失败：",
	WriteEnvErr:       "写入 .env 失败：",

	SelectOneHint:  "(↑/↓ · Enter · q 取消)",
	SelectManyHint: "(↑/↓ · Space · Enter · q)",

	UsageBody: `reasonix — 由配置和插件驱动的 coding agent（多模型）

用法：
  reasonix chat [--model NAME]                          交互式会话（多轮）
  reasonix run  [--model NAME] [--max-steps N] <task>   执行单次任务后退出
  reasonix serve [--model NAME] [--addr HOST:PORT]      通过 HTTP+SSE 提供会话（浏览器客户端在 /）
  reasonix init [path]                                  交互式设置；生成 reasonix.toml（及 .env）
  reasonix version
  reasonix help

示例：
  reasonix chat
  reasonix run "把 main.go 里的 TODO 实现掉"
  reasonix run --model mimo-pro "给这个函数补单元测试"
  echo "解释这段代码" | reasonix run

配置：
  优先级：flag > ./reasonix.toml > ~/.config/reasonix/config.toml > 内置默认值
  密钥通过 api_key_env 从环境变量注入（如 DEEPSEEK_API_KEY）。
  运行 'reasonix init' 生成配置；详见 docs/SPEC.md。
`,
}
