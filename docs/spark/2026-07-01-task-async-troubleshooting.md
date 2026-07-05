# task 异步委派排障（主代理 / TUI）

## 正常链路

1. 主代理 `task` → 同步 tool 结果：`Started task task-N (label)`
2. 子代理在后台 `Run` 至终答
3. `jobs.Manager` 完成 → `Controller` `pendingToolResult` + `autoReenter`
4. `Agent.drainNotify` → `PatchToolResult` 把同一条 tool 行替换为子代理正文（或追加后 stream）

## TUI / 界面看什么

**交付约定（方案 A）**：终答写入会话后，由 **auto-reentry 触发的主代理下一条 assistant 回复** 呈现给用户。TUI 对成功的 tool 调用默认不展开长输出；`PatchToolResult` 只改会话、不刷新 tool 卡片。因此 **勿以 tool 区是否从 `Started…` 变长来判断是否交付**——看 Notice + 主代理新回复即可。

| 现象 | 含义 |
|------|------|
| Notice：`background task finished: task-N` | 子代理已结束；随后应出现主代理续轮回复（含调查结论） |
| tool 区仍显示 `Started task …` / working | 常见；终答可能在会话里已补丁，界面未重画 tool 行 |
| 主代理续轮后的 **assistant 长回复** | **正常终答入口**（内容来自已补丁的 tool 消息） |
| `Reject: too many background jobs (max 3)` | 已有 3 个后台 job，禁止再 `task` |
| 长期无续轮、无结论 | 用 `peek-job`；查 failed/killed notice；勿连点 `task` |
| Notice：后台任务已完成但结果尚未写入会话 | `drainNotify` 失败；再发一条消息或等重试 |

## 日志关键词（服务端 / slog）

- `resultCh full, dropping result` — 结果通道满，主代理可能收不到终答，需修消费或加大缓冲
- `auto-reentry depth cap reached` — 自动续轮被限流，等用户发一条消息再继续

## 与系统提示的关系

`reasonix-system.md` §3.3–§3.6 与 `task` 的 `PostCallGuidance` 对齐：Started ≠ 失败，禁止同 prompt 重派；**委派后勿立即 peek-job**（排障场景见 §3.6）。