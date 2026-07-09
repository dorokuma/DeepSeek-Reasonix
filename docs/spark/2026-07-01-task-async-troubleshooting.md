# task 异步委派排障（主代理 / TUI）

## 架构（单路径，禁止再拧巴）

```
主代理 task 工具调用
  → 同步 tool 结果：{status:started, job_id}   // 该 tool_call 的终态，永不改写
  → 子代理后台 Run
  → jobs 完成
  → 会话尾部追加合成一轮：
        assistant tool_calls: bg-delivery-<job_id>  name=task
        tool result: 子代理全文
  → auto-reentry 让主代理基于尾部 tool 结果续写
```

**禁止**：
- 原地改写中间的 Started 行（看不见 + 历史被反复改坏）
- 在尾部单独塞一条孤儿 `role=tool`（会被 `SanitizeToolPairing` 丢掉）
- 用 `role=user` 信封冒充工具结果（双轨、难测）

## TUI / 界面看什么

终答由 **auto-reentry 后的主代理回复** 呈现。tool 区里原始 Started 卡可能一直显示“已启动”——正常。看：

| 现象 | 含义 |
|------|------|
| Notice：`background task finished: task-N` | 子代理已结束 |
| 会话尾部出现 `bg-delivery-task-N` 的 tool 结果 | **投递成功** |
| 主代理续轮长回复 | 正常终答入口 |
| `Reject: too many background jobs (max 3)` | 已有 3 个后台 job |
| 长期无续轮 | peek-job / failed notice；勿连点 task |

## 日志关键词

- `resultCh full, dropping result` — 结果通道满
- `auto-reentry depth cap reached` — 续轮限流，等用户再发一条
- `background job finished but result not committed` — 投递失败

## 与系统提示

`task` 的 PostCallGuidance：Started 只是收据；终答是尾部新一轮 tool 结果。
