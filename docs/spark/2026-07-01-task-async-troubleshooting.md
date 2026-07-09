# task 异步委派排障（主代理 / TUI）

## 架构（单路径）

```
主代理 task 工具调用
  → 同步 tool 结果：{status:started, job_id}   // 收据，永不改写
  → 子代理后台 Run（kind=task）
  → jobs 完成 → 唯一回调 SetOnCompletion → handleJobCompletion
  → 会话尾部追加合成一轮：
        assistant tool_calls: name=task_result  id=bg-delivery-<job_id>
        tool result: 子代理全文
  → auto-reentry 主代理续写
```

### 与 bash 后台的区别

| | task 子代理 | bash 后台 shell |
|--|------------|-----------------|
| kind | `task` | `bash` |
| 终答进会话 | 是（`task_result`） | 否 |
| 看输出 | 等尾部 `task_result` | `peek-job` |
| 自动续聊 | 是 | 否 |

## TUI / 界面

| 现象 | 含义 |
|------|------|
| Notice：`background task finished: task-N — result at conversation tail…` | 子代理已结束并会/已投递 |
| Notice：`background bash finished: bash-N — use peek-job…` | shell 结束，用 peek |
| 中间 Started 卡片一直「已启动」 | **正常**；别等它变长 |
| 会话尾部 `task_result` / `bg-delivery-task-N` | **投递成功** |
| 主代理续轮长回复 | 正常终答入口 |

## 运维细节（已修）

- 空自动续轮：末尾无未读 `task_result` 且无待投递 task 时直接跳过，不打空模型请求
- 多任务同时结束：空续轮合并为一次；深度上限 32
- bash 完成后保留最多 20 个 / 30 分钟，超时 GC（task 投递后立即移除）
- `task_result` 已注册为系统工具（不可手调），避免历史工具名「幽灵」

## 禁止再引入

- 原地改写中间 Started 行
- 尾部孤儿 `role=tool`（无配对 assistant tool_calls）
- 每 job 再挂一套 onComplete 与 SetOnCompletion 双轨
- 中途 `PostMessage` / subagent-report 信封

## 日志

- `background job finished but result not committed` — task 投递失败
- `auto-reentry depth cap reached` — 续轮限流（pending 仍在，用户再发一句可续）
- `resultCh full, dropping result` — 通道满
