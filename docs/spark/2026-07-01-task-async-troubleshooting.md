# task 异步委派排障（主代理 / TUI）

## 架构（单路径）

```
主代理 task 工具调用
  → 同步 tool 结果：{status:started, job_id}   // 收据，永不改写
  → 子代理后台 Run（kind=task）
  → jobs 完成 → 唯一回调 SetOnCompletion → handleJobCompletion
  → 会话尾部追加 runtime 观察消息（user 角色，非工具）：
        <background-task-result job_id="task-N" status="completed">
        …子代理全文…
        </background-task-result>
  → auto-reentry 主代理续写
```

**为何不用假工具名投递**：把终答写成 `tool_call name=task_result` 会进入历史，模型多轮后会学着去「调用」该名。观察消息不是工具，从根上切断仿造。

### 与 bash 后台的区别

| | task 子代理 | bash 后台 shell |
|--|------------|-----------------|
| kind | `task` | `bash` |
| 终答进会话 | 是（`<background-task-result>`） | 否 |
| 看输出 | 等尾部观察消息 | `peek-job` |
| 自动续聊 | 是 | 否 |

## 禁止再引入

- 原地改写中间 Started 行
- 尾部合成 `task_result` / 其它假工具轮
- 尾部孤儿 `role=tool`（无配对 assistant tool_calls）
- 每 job 再挂一套 onComplete 与 SetOnCompletion 双轨
