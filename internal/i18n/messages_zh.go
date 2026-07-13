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

	InitHint:       "项目记忆（AGENTS.md）在会话内由模型生成：运行 `reasonix chat`，然后 `/init` —— 模型会分析代码库并写入。配置请用 `reasonix setup`。",
	StepSetKeyHint: "运行 `reasonix setup`，或 export DEEPSEEK_API_KEY=…",
	StepChatDesc:   "交互式会话",
	StepRunDesc:    "执行单次任务",
	HelpFooter:     "reasonix help · 查看全部命令",

	ChatTip:           "对话上下文将跨轮保留。输入 'exit' 或按 Ctrl-D 退出。",
	TurnCancelled:     "已取消 — 回到提示符",
	NoSessionToResume: "没有可恢复的会话 — 用 `reasonix chat` 开一个新的",
	ResumeRequiresTTY: "--resume 需要交互式终端；用 --continue 直接恢复最近一次",
	PickSessionLabel:  "恢复哪个会话？",

	ResumeListHeader:    "会话（/resume <n> 切换）",
	ResumeBusy:          "请先完成或取消当前这一轮再恢复会话",
	ResumeBadIndexFmt:   "请选择 1–%d 的会话（用 /resume 查看列表）",
	ResumeAlreadyActive: "已在该会话中",
	ResumedTitle:        "已恢复会话",
	ResumePickTitle:     "选择要恢复的会话",
	ResumePickHint:      "↑/↓ 移动 · Enter 恢复 · Esc 取消",

	ChatThinking:           "思考中…",
	ChatThoughtForFmt:      "思考了 %d 秒",
	ChatStatusThinkingFmt:  "%s 思考中… (%d 秒 · Esc 取消)",
	ChatToolWorkingFmt:     "%s 运行中 · %d 秒",
	ChatStatusRetryingFmt:  "%s 正在重试 (%d/%d)… (Esc 取消)",
	ChatStatusIdle:         "就绪",
	ChatStatusYoloIdle:     "已跳过审批",
	ChatStatusCycleHint:    "shift+tab 循环切换",
	ChatStatusCacheNowFmt:  "本次 %s",
	ChatStatusCacheAvgFmt:  "平均 %s",
	ChatStatusToolApproval: "1 本次允许 · 2 本会话允许 · 3 总是允许（保存） · 4 拒绝 · y/a/p/n 兼容 · Ctrl-C 取消本轮",
	AskTypeSomething:       "自己输入",
	AskTypingHint:          "输入后按 Enter 确认",
	AskChatInstead:         "先不选择，直接回复",
	ChatStatusQuestion:     "↑/↓ 选 · 数字快选 · 空格多选 · Enter 确认 · ←/→ 切换问题 · Esc 取消",
	StatusResumePicker:     "↑/↓ 移动 · Enter 恢复 · Esc 取消",
	AskSubmitTitle:         "提交答案",
	AskUnanswered:          "(未答)",
	AskSubmitHint:          "Enter 提交 · ← 返回修改",
	ToolApprovalPromptFmt:  "需要你的许可\n\n将调用工具 %s%s。\n%s\n1. 本次允许\n2. 本会话允许同类调用\n3. 总是允许（保存到配置）\n4. 拒绝\n选择 [1/2/3/4]（兼容 y/a/p/n）",
	ToolApprovalSourceFmt:  "来源: %s",
	ToolApprovalBuiltIn:    "内置工具",
	ToolApprovalImageUse:   "将读取提供的图片用于图像理解。",
	DiffFoldedFmt:          "… 还有 %d 行",

	OutputStyleNone:   "无可用输出样式",
	OutputStyleHeader: "输出样式：",
	OutputStyleHint:   "在 reasonix.toml 中设置 agent.output_style",
	ThemeHeader:       "主题：",
	ThemeHint:         "使用 /theme <auto|light|dark|style> 切换",

	ThemeChangedFmt:    "主题已切换为 %s / %s",
	ThemeUnknownFmt:    "未知主题 %q",
	LanguageHeader:     "语言：",
	LanguageHint:       "使用 /language <auto|en|zh> 切换",
	LanguageChangedFmt: "语言已设为 %s（解析为 %s）",

	CompactionWorking: "正在压缩…",
	CompactionTitle:   "压缩",
	CompactionUnit:    "轮",
	CompactionAuto:    "自动",
	CompactionManual:  "手动",

	SlashCompactDone:   "已压缩",
	SlashCompactFailed: "压缩失败",
	SlashNewDone:       "已开始新对话",
	SlashNewFailed:     "新建对话失败",
	SlashTodoCleared:   "任务列表已清空",

	SlashUnavailable: "当前不可用",
	SlashUnknown:     "未知命令",
	SlashHelp:        "命令：/compact · /new · /resume · /rewind · /tree · /branch · /switch · /todo · /verbose · /model · /effort · /theme · /language · /mcp · /skills · /hooks · /paste-image · /memory · /remember · /quit · /help · 以及技能（/init, /test, …）",
	SlashPromptEmpty: "输入命令",
	SlashMCPNone:     "无 MCP 服务器",

	CtrlCQuitHint:      "Ctrl-C 退出",
	CompHintSlash:      "/ 查看命令",
	CompHintFile:       "@ 引用文件",
	ShellExecEmpty:     "请输入命令",
	ShellExecFailedFmt: "shell 命令失败：%v",

	ShellExecTimeoutFmt: "shell 命令超时（> %s）",
	ShellModeHint:       "输入 exit 或 Ctrl-D 退出 shell",
	CmdNew:              "/new — 新建对话",
	CmdCompact:          "/compact — 压缩上下文",
	CmdRewind:           "/rewind — 回退到之前的状态",

	CmdTree:         "/tree — 查看分支树",
	CmdBranch:       "/branch — 创建分支",
	CmdSwitchBranch: "/switch — 切换分支",
	CmdResume:       "/resume — 恢复会话",
	CmdModel:        "/model — 切换模型",

	CmdMemory:   "/memory — 查看记忆",
	CmdRemember: "/remember — 记住信息",
	CmdForget:   "/forget — 删除记忆",
	CmdMcp:      "/mcp — 管理 MCP",
	CmdHooks:    "/hooks — 管理 hook",

	CmdPasteImage:  "/paste-image — 粘贴图片",
	CmdOutputStyle: "/output-style — 输出风格",
	CmdTheme:       "/theme — 切换主题",
	CmdLanguage:    "/language — 切换语言",
	CmdSkill:       "/skills — 管理技能",

	CmdVerbose: "/verbose — 切换详细输出",
	CmdEffort:  "/effort — 设置推理力度",
	CmdHelp:    "/help — 帮助",

	CmdTodo:      "/todo — 任务列表",
	CmdQuit:      "/quit — 退出",
	ArgSkillList: "list",
	ArgSkillShow: "show",
	ArgSkillNew:  "new",

	ArgSkillPaths:   "paths",
	ArgMcpAdd:       "add",
	ArgMcpRemove:    "remove",
	ArgMcpList:      "list",
	ArgMcpConnected: "connected",

	ArgHooksList:    "list",
	ArgHooksTrust:   "trust",
	ArgModelCurrent: "current",
	ArgEffortAuto:   "auto",
	ArgEffortLow:    "low",

	ArgEffortMedium: "medium",
	ArgEffortHigh:   "high",
	ArgEffortXHigh:  "x-high",
	ArgEffortMax:    "max",
	ArgThemeCurrent: "current",

	ArgLanguageAuto:     "auto",
	ArgLanguageEn:       "en",
	ArgLanguageZh:       "zh",
	ListModelsHeaderFmt: "模型（当前：%s）",
	ListModelsHint:      "使用 /model <name> 切换模型",

	ListMemoryHeader:    "记忆",
	ListMemoryNone:      "无记忆",
	ListSkillsHeaderFmt: "技能（%d）",
	ListSkillsNone:      "无技能",
	ListHooksHeaderFmt:  "hook（%d 个活跃）",

	ListHooksNone: "无 hook",
	ListMcpHeader: "MCP 服务器",
	ListMcpNone:   "无 MCP",
	MemoryNone:    "无记忆",
	MemoryLoaded:  "已加载记忆",

	MemorySavedHeader:    "已保存",
	MemoryStoredUnderFmt: "  存储在 %s",
	MemoryEditHint:       "编辑记忆",
	ForgetUsage:          "/forget <key>",
	ForgetDoneFmt:        "已遗忘记忆：%s",

	QuickRememberEmpty:     "请输入要记住的内容",
	QuickRememberDoneFmt:   "已记住 → %s",
	ModelSwitchUnavailable: "当前不可用",
	ModelSwitchBusy:        "正在切换…",
	ModelAlreadyOnFmt:      "已在 %s",

	ModelSwitchingFmt:      "正在切换至 %s…",
	ModelSwitchedFmt:       "已切换至 %s（对话保留，提示缓存重置）",
	ModelListHeader:        "模型列表",
	RewindNone:             "暂无回退选项",
	RewindCodeConversation: "代码 + 对话",

	RewindConversationOnly: "仅对话",
	RewindCodeOnly:         "仅代码",
	RewindFork:             "分支（新分支，保留代码）",
	RewindSummarizeFrom:    "从此处开始总结",
	RewindSummarizeUpto:    "总结到此为止",

	RewindPickTitle:       "⟲ 回退——选择轮次",
	RewindPickHint:        "↑/↓ 移动 · Enter 选择 · Esc 关闭",
	RewindRestoreTitleFmt: "⟲ 恢复到第 %d 轮",
	RewindApplyHint:       "↑/↓ · Enter 应用 · Esc 返回",
	RewindEmpty:           "(空)",

	SkillPickerTitle:        "技能",
	SkillPickerAvailableFmt: "%d 个可用",
	SkillPickerMatchingFmt:  "%d 匹配 · 共 %d",
	SkillPickerHint:         "↑↓ 导航 · 空格切换 · Enter 保存 · / 搜索 · s 来源 · r 刷新 · Esc 取消",
	SkillPickerDetailHint:   "↑↓ 导航 · Enter 选择 · 空格切换 · Esc 返回",

	SkillPickerSearchEmpty:       "没有匹配的技能",
	SkillPickerSearchPrompt:      "搜索：",
	SkillPickerSearchPlaceholder: "搜索技能...",
	SkillPickerSourceTitle:       "来源",
	SkillPickerSourceActiveFmt:   "%d 个已启用",

	SkillPickerSourceHint:    "↑↓ 导航 · Enter 查看 · d 诊断 · s 技能 · r 刷新 · Esc 关闭",
	SkillPickerDiagHidden:    "d 显示诊断",
	SkillPickerDiagShown:     "d 隐藏诊断",
	SkillPickerBuiltinSource: "内置",
	SkillPickerRescanned:     "已重新扫描技能",

	SkillPickerNoDescription: "(无描述)",
	SkillPickerScopeProject:  "项目",
	SkillPickerScopeCustom:   "自定义",
	SkillPickerScopeGlobal:   "全局",
	SkillPickerScopeBuiltin:  "内置",

	SkillPickerAvailableLabel:   "开",
	SkillPickerDisabledLabel:    "关",
	SkillPickerNoChanges:        "技能对话框已关闭：无变更",
	SkillPickerSourceSkillsHint: "↑↓ 导航 · 空格切换 · Enter 详情 · Esc 来源",

	SkillPickerSourceSkillsEmpty: "此来源中无技能",
	SkillPickerActionToggle:      "切换启用",
	SkillPickerActionDelete:      "删除技能",
	SkillPickerDeleteTitleFmt:    "删除技能 /%s？",
	SkillPickerDeleteConfirm:     "删除",

	SkillPickerDeleteCancel: "取消",
	SkillPickerDeleteHint:   "Enter 确认 · y 删除 · n/Esc 取消",
	SkillPickerDeletedFmt:   "已删除技能 %s",
	SkillPickerMoreAboveFmt: "↑ 还有 %d 个",
	SkillPickerMoreBelowFmt: "↓ 还有 %d 个",

	SkillPickerTokenFmt:      "~%d tok",
	SkillPickerDetailMetaFmt: "范围：%s",
	SkillPickerSkillsUnit:    "个",
	SkillPickerLinesUnit:     "行",
	SkillPickerStatusLabel:   "技能选择器",

	SkillPickerStatusOK:         "正常",
	SkillPickerStatusMissing:    "缺失",
	SkillPickerStatusNotDir:     "非目录",
	SkillPickerStatusUnreadable: "不可读",
	SelectProvidersLabel:        "选择要启用的提供商",

	EnterAPIKeysHeader: "输入 API key（Enter 跳过稍后设置）：",
	MissingKeyIntro:    "reasonix.toml 已就绪——就差 API key 了。",
	WroteFileFmt:       "已写入 %s",
	SetupComplete:      "设置完成。",
	SetupCancelled:     "设置已取消。",

	TryHintFmt:            "试试 %s？",
	NextHint:              "下一步：设置 API key（运行 `reasonix setup` 或 export DEEPSEEK_API_KEY=...），然后运行 `reasonix run \"你的任务\"`。",
	ConfirmReconfigureFmt: "%s 已存在。重新配置并覆盖？",
	KeepingExisting:       "保留现有配置。",
	NotOverwritingFmt:     "%s 已存在，不覆盖",

	FetchingModelsFmt:          "正在获取 %s 的模型列表…",
	FetchModelsSuccessFmt:      "找到 %d 个模型（%s）",
	FetchModelsFailedFmt:       "获取模型列表失败（%s）：%v",
	FetchModelsUsingPresetsFmt: "%s 不支持在线获取，使用内置预设",
	FamilyKeyPromptFmt:         "输入 %s 的 API key 以列出可用模型（Enter 跳过）：",

	SelectModelsLabel:       "为 %s 选择要启用的模型",
	NoModelsAvailableFmt:    "%s：无可用模型，跳过",
	CustomFetchEmpty:        "模型列表为空——转为手动添加",
	AnthropicFetchEmpty:     "模型列表为空——Anthropic 兼容提供商通常不暴露列表，转为手动添加",
	SkipStaleCustomEntryFmt: "跳过过时的 %q 条目（指向 %s）",

	APIKeyAlreadySetFmt:  "使用已有值 %s",
	APIKeyResetPromptFmt: "重新输入 %s？",
	CustomProviderLabel:  "自定义模型",
	CustomProviderDesc:   "添加第三方 OpenAI 兼容模型",
	CustomAddMethodLabel: "添加第三方 OpenAI 兼容模型 - 选择添加方式",

	CustomMethodManual:  "手动输入模型名",
	CustomMethodURL:     "从 URL 获取模型",
	CustomPromptModel:   "输入模型名",
	CustomPromptBaseURL: "输入 Base URL",
	CustomPromptKeyEnv:  "输入 API Key 环境变量名",

	CustomPromptAPIKey:      "输入 API Key",
	CustomAddedFmt:          "已添加自定义模型：%s",
	AnthropicProviderLabel:  "Anthropic 兼容",
	AnthropicProviderDesc:   "添加 Anthropic API 兼容模型",
	AnthropicAddMethodLabel: "添加 Anthropic 兼容模型 - 选择添加方式",

	AnthropicMethodManual:  "手动输入模型名",
	AnthropicMethodURL:     "从 URL 获取模型",
	AnthropicPromptModel:   "输入模型名",
	AnthropicPromptBaseURL: "输入 Base URL",
	AnthropicPromptKeyEnv:  "输入 API Key 环境变量名",

	AnthropicPromptAPIKey:          "输入 API Key",
	AnthropicAddedFmt:              "已添加 Anthropic 兼容模型：%s",
	AnthropicFetchingModelsFmt:     "正在获取 %s 的模型列表…",
	AnthropicFetchModelsSuccessFmt: "找到 %d 个模型（%s）",
	AnthropicFetchModelsFailedFmt:  "获取模型列表失败（%s）：%v",

	AnthropicSelectModelsLabel: "为 %s 选择要启用的模型",
	UnknownCommandFmt:          "未知命令：%s",
	UsageRunHint:               "用法：reasonix run [--model NAME] <task>",
	ErrorPrefix:                "错误：",
	ReconfigureOnUnknownModel:  "配置的模型已不可用——重新运行 setup。",

	WriteConfigErr:                 "写入配置：",
	WriteEnvErr:                    "写入 .env：",
	ProviderErrBadRequest:          "请求格式错误（HTTP 400）：请求体被拒绝。这可能是 bug——如果持续出现请报告。",
	ProviderErrAuth:                "认证失败（HTTP 401）：API key 缺失、错误或已过期。检查 .env 中的 key 或运行 `reasonix setup`。",
	ProviderErrInsufficientBalance: "余额不足（HTTP 402）：账户额度不足。请充值后重试。",

	ProviderErrUnprocessable: "无效参数（HTTP 422）：请求参数被拒绝。这可能是 bug——如果持续出现请报告。",
	ProviderErrRateLimited:   "达到速率限制（HTTP 429）：请求过多（TPM/RPM）。已重试后回退——请减慢速度或稍后重试。",
	ProviderErrServer:        "服务器错误（HTTP 500）：提供商内部故障。已重试后回退；如果持续失败请稍后重试。",
	ProviderErrServerBusy:    "服务忙，请稍后重试",
	SelectOneHint:            "(↑/↓ · 回车)",

	SelectManyHint: "(↑/↓ · 空格 · 回车 · q)",
	UsageBody: `reasonix —— 配置与插件驱动的编码代理（多模型）

用法：
  reasonix chat [--model NAME] [-c|--continue] [--resume]   交互会话
  reasonix run  [--model NAME] [--max-steps N] [-c|--continue] [--resume PATH] <task>   单次任务
  reasonix serve [--model NAME] [--addr HOST:PORT]     HTTP/SSE 服务
  reasonix setup [path]    交互式配置向导
  reasonix mcp <add|remove|list>    管理 MCP 服务器
  reasonix doctor [--json]    诊断信息
  reasonix version
  reasonix help

配置优先级：flag > ./reasonix.toml > ~/.config/reasonix/config.toml > 内置默认
密钥通过 api_key_env 环境变量注入`,
}
