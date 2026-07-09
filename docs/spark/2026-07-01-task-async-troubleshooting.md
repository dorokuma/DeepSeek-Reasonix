# task 异步委派排障（主代理 / TUI）

## 正常链路

1. 主代理 `task` → 同步 tool 结果：Started stub（JSON `status=started` 或旧文案）
2. 子代理在后台 `Run` 至终答
3. `jobs.Manager` 完成 → `Controller` `CompleteBackgroundJob` + `pendingToolResult` + `autoReenter`
4. **双重投递（不可简化）**：
   - **中部**：`ReplaceTaskStartedWithResult` 把 Started 占位改成终答（保住 tool_call 配对）
   - **尾部**：追加 `role=user` 的 `<background-task-result job="task-N">…</background-task-result>`（auto-reentry 可见）

> 历史坑：只 patch 中部 → 主代理“收不到”；只在尾部 append `role=tool` → 被 `SanitizeToolPairing` 当孤儿丢掉。

## TUI / 界面看什么

**交付约定**：终答写入会话后，由 **auto-reentry 触发的主代理下一条 assistant 回复** 呈现给用户。TUI 对成功的 tool 调用默认不展开长输出；中部 patch 不一定刷新 tool 卡片。判断交付看 **Notice + 会话尾部 envelope + 主代理新回复**。

| 现象 | 含义 |
|------|------|
| Notice：`background task finished: task-N` | 子代理已结束；随后应出现主代理续轮回复（含调查结论） |
| tool 区仍显示 `Started task …` / working | 常见；中部可能已 patch，界面未重画 |
| 会话尾部出现 `<background-task-result …>` | **投递成功**的硬证据 |
| 主代理续轮后的 **assistant 长回复** | **正常终答入口**（内容来自尾部 envelope） |
| `Reject: too many background jobs (max 3)` | 已有 3 个后台 job，禁止再 `task` |
| 长期无续轮、无结论 | 用 `peek-job`；查 failed/killed notice；勿连点 `task` |
| Notice：后台任务已完成但结果尚未写入会话 | `drainNotify` 失败；再发一条消息或等重试 |

## 日志关键词（服务端 / slog）

- `resultCh full, dropping result` — 结果通道满，主代理可能收不到终答，需修消费或加大缓冲
- `auto-reentry depth cap reached` — 自动续轮被限流，等用户发一条消息再继续

## 与系统提示的关系

`reasonix-system.md` §3.3–§3.6 与 `task` 的 `PostCallGuidance` 对齐：Started ≠ 失败，禁止同 prompt 重派；**委派后勿立即 peek-job**（排障场景见 §3.6）。