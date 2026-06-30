# 子代理异步通信架构重构

> 2026-07-04 | 状态：待审批 | 参考：2026-06-29-steer-mechanism-design.md

## 目标

主代理永远不阻塞在子代理上。用户随时与主代理正常对话。主代理作为监督者，通过工具与子代理双向互通——子代理主动推送进度和结果，主代理可随时修正或取消子代理。

## 总体架构

```
用户 ←→ 主代理（Run 循环永不停转）
              │
              │ steerCh (用户消息注入，保留)
              │ drain: steerCh + notifyCh → session
              │
              ├─ task 工具 → 子代理 A (后台 goroutine)
              │     ├─ steerCh ← steer-job 工具
              │     └─ notifyCh → 主代理 drain 消费
              │
              ├─ task 工具 → 子代理 B (后台 goroutine)
              │
              ├─ steer-job <id> <msg>  → 往子代理 steerCh 发消息
              ├─ cancel-job <id>       → 取消子代理
              └─ peek-job <id>         → 非阻塞查子代理状态
```

## §1 删除项

### 1.1 task 前台路径

| 删除内容 | 文件 | 行 |
|---|---|---|
| `run_in_background` 参数定义 | `internal/agent/task.go` | 109-118 |
| `RunInBackground` 反序列化字段 | `internal/agent/task.go` | 141 |
| 后台分支 `if p.RunInBackground` | `internal/agent/task.go` | 163-205 |
| `runSub()` 方法和调用 | `internal/agent/task.go` | 208-313 |
| `RunSubAgent()` 中的 activeChild 注册 | `internal/agent/task.go` | 343-345 |
| `subSink()` / `subSinkFor()` | `internal/agent/task.go` | 428-451 |
| `NestedSink()` | `internal/agent/task.go` | 412-418 |

Task 工具 `Execute()` 直接走现有后台路径逻辑：创建 job → `jm.Start()` → 立即返回 `"Started task <job-id> (<description>)"`。

### 1.2 activeChild 机制

| 删除内容 | 文件 | 行 |
|---|---|---|
| `steerCh` 字段（Agent 保留该字段本身，见 §3.1） | — | — |
| `activeChild atomic.Pointer[Agent]` | `internal/agent/agent.go` | 275-277 |
| `setActiveChild()` | `internal/agent/agent.go` | 740-742 |
| `clearActiveChild()` | `internal/agent/agent.go` | 744-747 |
| `Steer()` 中 activeChild 转发逻辑 | `internal/agent/agent.go` | 753-765 |
| `WithActiveChild()` | `internal/agent/context.go` | 68-73 |
| `isActiveChild()` | `internal/agent/context.go` | 75-78 |
| `activeChildKey` | `internal/agent/context.go` | 14-16 |

### 1.3 wait 工具

删除 `internal/tool/builtin/bgjobs.go` 中 `waitJob` 类型及其 `init()` 注册行。

### 1.4 DrainCompletedNote 及 `<background-jobs>` 注入

| 删除内容 | 文件 | 行 |
|---|---|---|
| `DrainCompletedNote()` | `internal/jobs/jobs.go` | 462-472 |
| `m.completed` 字段和 `recordCompletion()` | `internal/jobs/jobs.go` | 搜索 completed |
| `<background-jobs>` XML 注入逻辑 | `internal/control/input.go` | 35-38 |
| `Compose()` 中调用 `DrainCompletedNote` | `internal/control/input.go` | 36 |

### 1.5 SubagentStop hook

| 删除内容 | 文件 | 行 |
|---|---|---|
| `SubagentStop` hook 触发 | `internal/agent/agent.go` | 1425-1426 |
| `isBackgroundTaskCall()` | `internal/agent/agent.go` | 1579-1585 |
| `SubagentStop` hook 常量和 runner 处理 | `internal/hook/hook.go` + `runner.go` | 保留常量定义但标记废弃，runner 处理删 |

---

## §2 Steer 通道保留

### 2.1 保留项

用户 steer 底层机制不动：

- `steerCh chan string`（`agent.go:272`）— 保留
- `Agent.Steer(input)` — 简化为仅写入 steerCh（删 activeChild 转发分支）
- `Agent.drainSteer()` — 保留
- `Run()` 循环中 `drainSteer()` 调用点 — 保留
- `Controller.Steer()` — 保留
- HTTP `POST /steer` — 保留
- TUI `tuiRunning` 状态 Enter → `ctrl.Steer()` — 保留

### 2.2 语义变化

用户 steer 永远进主代理的 steerCh，不再被 activeChild 转发。用户体验：随时说话，不需要区分 steer 和普通消息。底层仍有 drain 延迟（等当前 stream 结束），但对用户透明。

---

## §3 notifyCh — 子代理 → 主代理反向通道

### 3.1 类型定义

新增文件或追加到 `internal/agent/task.go`：

```go
// JobNotify is a notification sent from a background sub-agent to its parent.
type JobNotify struct {
    JobID    string       `json:"job_id"`
    Type     string       `json:"type"`   // "ack" | "progress" | "result"
    Step     int          `json:"step,omitempty"`
    AckMsg   string       `json:"ack_msg,omitempty"`    // for ack
    LastTool string       `json:"last_tool,omitempty"`  // for progress
    Output   string       `json:"output,omitempty"`     // for result (final answer)
}
```

`notifyCh` 缓冲 16，满则丢弃（非阻塞）。定义在子 Agent 上：

```go
// Agent 新增字段
notifyCh chan JobNotify   // 子代理推送到父代理
```

### 3.2 推送时机

子代理 `Run()` 循环中：

| 时机 | Type | 数据 |
|---|---|---|
| `drainSteer()` 消费到消息后 | `ack` | `AckMsg: "received"` |
| 每轮 `stream()` 结束后 | `progress` | `Step`, `LastTool` |
| `Run()` 正常返回前 | `result` | `Output: 最终答案` |
| `Run()` 因 cancel 返回 | `result` | `Output: ""`, 标记取消 |

### 3.3 消费端

主代理在每次 `Run()` 循环的 `drain()` 阶段（在 `drainSteer()` 之后、`stream()` 之前）消费所有已完成子代理的 notifyCh。

新增 `drainNotify()`：

```go
func (a *Agent) drainNotify() {
    // 遍历 a.taskResults（job-id → jobMeta）
    // 对每个 job，非阻塞读取其 agent.notifyCh
    // 找到 type=result 的，用 jobMeta.ToolCallID 追加为 tool message 到 session
    // 找到 type=progress/ack 的，不追加到 session（仅通过 steer-job/peek-job 工具返回值暴露）
}
```

**关键设计**：主代理通过新的 `taskResults map[string]jobMeta` 字段维护 `jobID → toolCallID` 映射，其中 `jobMeta` 记录创建该 job 时对应的 `toolCallID` 和 `step`，以便子代理 result 能正确追加为对应 tool call 的 tool role message。

```go
// Agent 新增字段
taskResults map[string]jobMeta  // jobID → 创建时的 tool call 元信息

type jobMeta struct {
    ToolCallID string
    StartStep  int
}
```

### 3.4 ack 和 progress 的 session 处理

`ack` 和 `progress` 通知不作为独立 message 追加到 session（避免污染对话历史）。仅在模型调 `steer-job` 或 `peek-job` 工具时，工具返回值中包含最新状态。

`result` 通知作为 `RoleTool` 消息追加，`ToolCallID` = 原始 task tool call ID。

---

## §4 工具变更

文件：`internal/tool/builtin/bgjobs.go`

### 4.1 删除 wait

删除 `waitJob` 类型、`Execute`、schema 定义、`init()` 中的注册行。

### 4.2 新增 steer-job

```go
type steerJob struct{}

// Schema: {"job_id": "string (required)", "message": "string (required)"}
// Execute:
//   1. 从 context 获取 jobs.Manager
//   2. jm.Steer(jobID, message) → 往子代理 steerCh 发消息
//   3. 返回当前状态: {"status": "queued", "job_step": N, "message": "queued at step N"}
//   4. 不阻塞等 ack — ack 后续通过 notifyCh → 主代理 drain 自然感知
```

### 4.3 新增 cancel-job

```go
type cancelJob struct{}

// Schema: {"job_id": "string (required)"}
// Execute:
//   1. 从 context 获取 jobs.Manager
//   2. jm.Kill(jobID) → 取消子代理
//   3. 返回 {"cancelled": true} 或 {"cancelled": false, "reason": "not found"}
```

### 4.4 新增 peek-job

```go
type peekJob struct{}

// Schema: {"job_id": "string (required)"}
// Execute:
//   1. 从 context 获取 jobs.Manager
//   2. jm.Peek(jobID) → 返回 {"status": "running", "step": N, "last_tool": "...", "last_ack": "..."}
//   3. 非阻塞，纯查询
```

### 4.5 保留不变

`bash_output` 和 `kill_shell` 不动。

---

## §5 jobs.Manager 改造

文件：`internal/jobs/jobs.go`

### 5.1 删除

- `DrainCompletedNote()` 方法
- `m.completed` 字段和 `recordCompletion()` 方法
- `Result` 类型中 `Output` 之外的字段（ID/Kind/Label/Status）是否保留待确认 — 当前仅 Wait() 使用 Result，Wait 删除后 Result 类型可精简或删除

### 5.2 新增

```go
// Steer 往指定 job 的子代理 steerCh 发消息
func (m *Manager) Steer(jobID string, message string) error

// Peek 非阻塞查询 job 状态
func (m *Manager) Peek(jobID string) (JobStatus, error)

type JobStatus struct {
    JobID    string
    Status   string  // "running" | "done" | "cancelled" | "error"
    Step     int
    LastTool string
    LastAck  string
}
```

### 5.3 SetOnCompletion 保留但改造

`SetOnCompletion` 回调保留，但触发后不再走 `DrainCompletedNote` 注入路径。改为：

子代理完成时 → `jm` 把 `notifyCh` 中 result 排队到主代理可见的队列 → 主代理 `autoReenter` 触发新 turn → 主代理 `drainNotify()` 消费 result → 追加为 tool message → `stream()`。

---

## §6 Controller 层改造

文件：`internal/control/controller.go`

### 6.1 保留 autoReenter

`autoReenter()` 方法保留。子代理完成 → `SetOnCompletion` 回调 → `autoReenter()` → 启动新 turn → 主代理 drain 中消费 result。

### 6.2 删除 DrainCompletedNote 调用

`runTurnWithRaw()` 中调用 `DrainCompletedNote()` 的行删除。`Compose()` 中不再注入 `<background-jobs>`。

---

## §7 嵌套子代理

子代理 A 调 task 启动孙代理 B → B 也是后台 job → B 完成 → B 的 notifyCh result 推给 A → A 的 drainNotify 消费 → A 拿到 tool result → A 继续运行 → A 完成 → A 的 result 推给主代理。

每层独立管理自己的 `jobID → toolCallID` 映射。notifyCh 只在父子之间通信，不跨级。

---

## §8 完整交互示例

```
1. 用户: "查一下 XXX 和 YYY"
2. 主代理 stream → 调 task(prompt="查 XXX") → 立即返回 "task-1 running"
3. 主代理 stream → 调 task(prompt="查 YYY") → 立即返回 "task-2 running"
4. 主代理 stream → "已启动两个查询，分别进行中，有结果告诉你"
5. [主代理空闲，等待]
6. 用户: "task-1 顺便也查一下 ZZZ"
7. 主代理 stream → 调 steer-job("task-1", "顺便也查一下 ZZZ")
   → 返回 "queued at step 3"
8. 主代理 stream → "已告诉 task-1 加查 ZZZ"
9. [task-1 drainSteer 消费 steer] → notifyCh 推 ack
10. 主代理 drainNotify → 消费 ack
11. 主代理 stream → "task-1 已确认收到，正在加查 ZZZ"
12. [task-2 完成] → notifyCh 推 result("YYY 的结果是...")
13. 主代理 SetOnCompletion 触发 → autoReenter → drainNotify
    → 追加 tool message(toolCallID=task-2-call-id, content="YYY 的结果是...")
14. 主代理 stream → "YYY 的结果出来了，是这样的..."
15. [task-1 完成] → 同上流程
16. 主代理 stream → "XXX 和 ZZZ 的结果也出来了..."
```

---

## §9 涉及文件完整清单

| 文件 | 改动类型 | 内容 |
|---|---|---|
| `internal/agent/task.go` | 重写 | 删前台路径、runSub、activeChild、subSink；新增 notifyCh 推送逻辑 |
| `internal/agent/agent.go` | 删+增 | 删 activeChild/setActiveChild/clearActiveChild/SubagentStop 触发/isBackgroundTaskCall；新增 drainNotify、notifyCh 消费；保留 steerCh/drainSteer |
| `internal/agent/context.go` | 删 | 删 WithActiveChild/isActiveChild/activeChildKey |
| `internal/agent/coordinator.go` | 确认 | Steer 方法是否涉及 activeChild 转发 |
| `internal/tool/builtin/bgjobs.go` | 删+增 | 删 wait；新增 steer-job/cancel-job/peek-job |
| `internal/jobs/jobs.go` | 删+增 | 删 DrainCompletedNote/completed/recordCompletion；新增 Steer/Peek方法、notifyCh 支持 |
| `internal/control/controller.go` | 删 | 删 DrainCompletedNote 调用；保留 autoReenter |
| `internal/control/input.go` | 删 | 删 `<background-jobs>` 注入 |
| `internal/hook/hook.go` | 标记废弃 | SubagentStop 常量保留但注释废弃 |
| `internal/hook/runner.go` | 删 | SubagentStop 处理逻辑 |
| `internal/serve/serve.go` | 不动 | POST /steer 保留 |
| `internal/cli/chat_tui.go` | 不动 | steer 调用保留 |

## §10 不变项

- `bash_output` / `kill_shell` 工具不动（bash 后台任务独立体系）
- `jobs.Manager.Start()` / `Kill()` / `Output()` 核心逻辑不动（Steer/Peek 是新增方法）
- `event.Sink` 体系不动
- HTTP API `/submit` / `/steer` 端点不动
- 桥项目 `reasonix-telegram` 不动
