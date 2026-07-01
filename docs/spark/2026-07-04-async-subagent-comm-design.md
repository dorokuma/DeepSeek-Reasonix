# 子代理异步通信架构重构

> 2026-07-04 | 状态：待审批 | 参考：2026-06-29-steer-mechanism-design.md

## 目标

主代理永远不阻塞在子代理上。用户随时与主代理正常对话。主代理作为监督者，通过工具与子代理双向互通——子代理主动推送进度和结果，主代理可随时修正或取消子代理。

## 总体架构

```
用户 ←→ 主代理（Run 循环永不停转）
              │
              │ steerCh (用户消息注入，保留)
              │ drain: steerCh + ackCh/resultCh/notifyCh → session
              │
              ├─ task 工具 → 子代理 A (后台 goroutine)
              │     ├─ steerCh ← steer-job 工具
              │     └─ ackCh/resultCh/notifyCh → 主代理 drain 消费
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
| 前台分支（task.go:207-213） | `internal/agent/task.go` | 207-213 |
| `RunSubAgent()` 中的 activeChild 注册 | `internal/agent/task.go` | 343-345 |
| `subSink(ctx)`（foreground 专用） | `internal/agent/task.go` | 428-433 |
| `NestedSink()` | `internal/agent/task.go` | 412-418 |

Task 工具 `Execute()` 直接走现有后台路径逻辑：创建 job → `jm.Start()` → 立即返回 `"Started task <job-id> (<description>)"`。

> **关于 `runSub()`**：`runSub()` 保留不变，不删除。仅删除 `runSub()` 内部的前台分支（行 207-213，即 `if !p.RunInBackground` 条件块及其内部逻辑）。后台路径无条件执行。`subSinkFor()`（background 专用，行 436-451）同样保留，不删除。

### 1.2 activeChild 机制

| 删除内容 | 文件 | 行 |
|---|---|---|
| `steerCh` 字段（Agent 保留该字段本身，见 §2） | — | — |
| `activeChild atomic.Pointer[Agent]` | `internal/agent/agent.go` | 274-277 |
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

### 1.5 SubagentStop hook（完全删除）

| 删除内容 | 文件 | 行 |
|---|---|---|
| `SubagentStop` hook 触发 | `internal/agent/agent.go` | 1425-1426 |
| `isBackgroundTaskCall()` | `internal/agent/agent.go` | 1579-1585 |
| `ToolHooks` 接口中 `SubagentStop` 方法声明 | `internal/agent/agent.go` | 119 |
| `SubagentStop` 常量定义 | `internal/hook/hook.go` | 53 |
| `Events` 列表中 `SubagentStop` | `internal/hook/hook.go` | 62 |
| `Runner.SubagentStop()` 方法 | `internal/hook/runner.go` | 148-155 |
| `stubHooks.SubagentStop()` 和测试 | `internal/agent/hooks_test.go` | 39, 55-71 |
| `typedNilHooks.SubagentStop()` | `internal/agent/nil_boundary_test.go` | 30 |
| `SubagentStop` 文档注释 | `internal/hook/hook.go` | 47-48 |

---

## §2 Steer 通道保留

### 2.1 保留项

用户 steer 底层机制不动：

- `steerCh chan string`（`agent.go:272`）— 保留，缓冲大小定义为 8
- `Agent.Steer(input)` — 简化为仅写入 steerCh（删 activeChild 转发分支）
- `Agent.drainSteer()` — 保留，签名从 `func (a *Agent) drainSteer()` 改为 `func (a *Agent) drainSteer() string`，由 caller 决定是否将返回的字符串写入 session（pendingToolResult 路径不写，正常路径写入）
- `Run()` 循环中 `drainSteer()` 调用点 — 保留
- `Controller.Steer()` — 保留
- HTTP `POST /steer` — 保留
- TUI `tuiRunning` 状态 Enter → `ctrl.Steer()` — 保留
- `autoReenter` 中 `Send` 调用 `runGuarded`，如果当前 turn 未结束会排队，不会阻塞

### 2.2 语义变化

用户 steer 永远进主代理的 steerCh，不再被 activeChild 转发。用户体验：随时说话，不需要区分 steer 和普通消息。底层仍有 drain 延迟（等当前 stream 结束），但对用户透明。

---

## §3 通知通道（ackCh / resultCh / notifyCh）

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

拆分三个通知通道，定义在 `Job` 结构体上：

```go
// Job 新增字段
ackCh    chan JobNotify   // ack 通道，缓冲 4，非阻塞（满丢）
resultCh chan JobNotify   // result 通道，缓冲 1，阻塞发送
notifyCh chan JobNotify   // progress 通道，缓冲 16，满丢（非阻塞）
```

Manager 提供 `NotifyChannels` 方法供父代理获取指定 job 的三个通道读端：

```go
// NotifyChannels 返回 job 的三个通知通道的读端封装
func (m *Manager) NotifyChannels(jobID string) *JobChannels

// JobChannels 封装 job 的三个通知通道的读端
type JobChannels struct {
    Ack     <-chan JobNotify  // ackCh 读端
    Result  <-chan JobNotify  // resultCh 读端
    Progress <-chan JobNotify // notifyCh 读端（进度，满丢）
}
```

**子代理工具事件转发机制**：保留 `subSinkFor()`（background 专用），子代理的 tool dispatch/result 事件仍通过 `subSinkFor` 转发到父代理的 event sink，用于工具执行的日志和调试。`subSink(ctx)`（foreground 专用）已删除。

### 3.2 推送时机

子代理 `Run()` 循环中：

| 时机 | Type | 通道 | 数据 |
|---|---|---|---|
| `drainSteer()` 消费到消息后 | `ack` | `ackCh`（缓冲 4） | `AckMsg: "received"` |
| 每轮 `stream()` 结束后 | `progress` | `notifyCh`（缓冲 16，满丢） | `Step`（从 `j.step` 原子读）, `LastTool` |
| `Run()` 正常返回前 | `result` | `resultCh`（缓冲 1，阻塞） | `Output: 最终答案`。使用 select 感知 ctx 取消： |
| `Run()` 因 cancel 返回 | `result` | `resultCh`（缓冲 1，阻塞） | `Output: ""`, 标记取消。如果 ctx 已取消，子代理不发送 result，直接退出，避免永久阻塞。 |

> **关于缓冲与丢消息**：`ackCh`（缓冲 4）和 `notifyCh`（缓冲 16）满则丢弃，不阻塞子代理。主代理长时间 `stream()` 期间（模型推理阶段）可能不调用 `drainNotify()`，导致 `ackCh` 和 `notifyCh` 中的非 result 通知被丢弃。设计接受此行为——ack 和 progress 是状态快照而非事件日志，丢失不影响最终结果。`resultCh`（缓冲 1，阻塞发送）保证 result 通知不丢，因为子代理 `Run()` 返回前阻塞等待主代理消费 result。

子代理 `Run()` 返回前通过 select 向 resultCh 发送 result：
```go
if ctx.Err() != nil {
    return  // 已取消，不发送
}
select {
case <-ctx.Done():
    return
case j.resultCh <- JobNotify{Type: "result", Output: ans}:
    // 成功发送
}
```

**Step 更新机制**：子代理 `Run()` 循环中维护 step 计数器，每轮 `stream()` 结束后执行 `atomic.StoreInt32(&j.step, step)`。`JobNotify.Step` 的值从 `j.step` 原子读取，确保主代理 peek-job 和 progress 通知中看到最新的 step。

### 3.3 消费端

主代理在每次 `Run()` 循环的 `drain()` 阶段消费所有已完成子代理的 notifyCh。

**循环顺序**：
- **正常路径**：`drainSteer()` → `drainNotify()` → `stream()`
- **pendingToolResult 路径**：`drainNotify()` → `stream()`（跳过 `drainSteer`）
- 顺序调整原因见 §6.1。

新增 `drainNotify()`，返回 bool 表示是否消费到 result：

```go
func (a *Agent) drainNotify() bool {
    // 从 jobs.Manager 获取活跃 job 列表（通过 Manager.ActiveJobs() 返回 []string jobID）
    // 对每个 jobID，通过 jm.NotifyChannels(jobID) 拿到 resultCh，非阻塞读取
    // 仅当从 resultCh 读到数据时，调用 Controller.TakeJobMeta(jobID)
    // 找到 type=result 的，用 jobMeta.ToolCallID 追加为 tool message 到 session
    // 找到 type=progress/ack 的，不追加到 session（仅通过 steer-job/peek-job 工具返回值暴露）
    // 返回是否成功消费到至少一个 result
}
```

**关键设计**：`drainNotify()` 从 `jobs.Manager.ActiveJobs()` 获取活跃 jobID 列表，对每个 job 通过 `NotifyChannels(jobID).Result` 非阻塞读取 resultCh，仅当读到数据时才调用 `Controller.TakeJobMeta(jobID)`。`taskResults` 从 `Agent` 移入 `Controller`。`Controller` 新增 `taskResults map[string]jobMeta` 字段维护 `jobID → toolCallID` 映射，并新增 `taskResultsMu sync.Mutex` 保护并发访问。Agent 的 task tool `Execute()` 通过 `CallContext(ctx)` 拿到当前 `toolCallID`，然后调用 `Controller.RegisterJobMeta(jobID, toolCallID)`（加锁写入）。`drainNotify()` 通过 `Controller.TakeJobMeta(jobID)` 获取映射并自动删除条目（加锁读写）。消费 result 后从 `Controller.taskResults` 中删除该条目，避免内存泄漏。

```go
// Controller 新增字段和方法
type Controller struct {
    // ... 现有字段
    taskResults   map[string]jobMeta  // jobID → 创建时的 tool call 元信息
    taskResultsMu sync.Mutex          // 保护 taskResults 并发访问
}

type jobMeta struct {
    ToolCallID string
    StartStep  int
}

func (c *Controller) RegisterJobMeta(jobID string, meta jobMeta) {
    c.taskResultsMu.Lock()
    defer c.taskResultsMu.Unlock()
    c.taskResults[jobID] = meta
}

func (c *Controller) GetJobMeta(jobID string) (jobMeta, bool) {
    c.taskResultsMu.Lock()
    defer c.taskResultsMu.Unlock()
    meta, ok := c.taskResults[jobID]
    return meta, ok
}

// TakeJobMeta 读取并删除 job 元信息，避免已完成 job 的元数据无限累积
func (c *Controller) TakeJobMeta(jobID string) (jobMeta, bool) {
    c.taskResultsMu.Lock()
    defer c.taskResultsMu.Unlock()
    meta, ok := c.taskResults[jobID]
    if ok {
        delete(c.taskResults, jobID)
    }
    return meta, ok
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

`Job` 结构体增加 `steerCh chan<- string`（写入端），子代理启动时注册。

```go
type steerJob struct{}

// Schema: {"job_id": "string (required)", "message": "string (required)"}
// Execute:
//   1. 从 context 获取 jobs.Manager
//   2. jm.Steer(jobID, message) → 找到 Job，往 j.steerCh 写消息
//   3. 返回当前状态: {"status": "queued", "job_step": N, "message": "queued at step N"}
//   4. 不阻塞等 ack — ack 后续通过 ackCh → 主代理 drain 自然感知
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
- `Result` 类型（`jobs.go:48-54`）及其所有相关方法 — wait 工具删除后无消费者，`Wait()` 方法一并删除

### 5.2 新增

`Job` 结构体新增 `steerCh` 字段，子代理启动时由 Manager 创建并注册：

```go
// Job 新增字段
steerCh chan string  // 父代理 → 子代理 steer 消息通道
```

Manager 的 `Start()` 方法在创建 Job 时初始化 `steerCh`，并将写入端传给子代理。子代理的 `Run()` 循环通过 `drainSteer()` 消费该通道。

```go
// Steer 往指定 job 的子代理 steerCh 发消息（非阻塞）
func (m *Manager) Steer(jobID string, message string) error {
    job, ok := m.jobs[jobID]
    if !ok {
        return ErrJobNotFound
    }
    select {
    case job.steerCh <- message:
        return nil
    default:
        return ErrSteerBufferFull
    }
}

// ActiveJobs 返回当前所有活跃 job 的 ID 列表
func (m *Manager) ActiveJobs() []string {
    m.mu.Lock()
    defer m.mu.Unlock()
    ids := make([]string, 0, len(m.jobs))
    for id := range m.jobs {
        ids = append(ids, id)
    }
    return ids
}

// Peek 非阻塞查询 job 状态
func (m *Manager) Peek(jobID string) (JobStatus, error)

type JobStatus struct {
    JobID    string
    Status   string  // "running" | "done" | "cancelled" | "error"
    Step     int
    LastTool string
    LastAck  string
}

// NotifyChannels 返回 job 的三个通知通道读端
func (m *Manager) NotifyChannels(jobID string) *JobChannels

type JobChannels struct {
    Ack     <-chan JobNotify  // ackCh 读端
    Result  <-chan JobNotify  // resultCh 读端
    Progress <-chan JobNotify // notifyCh 读端（进度，满丢）
}
```

### 5.3 SetOnCompletion 保留但改造

`SetOnCompletion` 回调保留，但触发后不再走 `DrainCompletedNote` 注入路径。改为：

子代理完成时 → `jm` 把 `resultCh` 中 result 排队到主代理可见的队列 → 主代理 `autoReenter` 触发新 turn → 主代理 `drainNotify()` 消费 result → 追加为 tool message → `stream()`。

---

## §6 Controller 层改造

文件：`internal/control/controller.go`

### 6.1 保留 autoReenter（A 方案）

保留现有 `Run(ctx context.Context, input string) error` 签名，不改为无限循环。`autoReenter()` 方法保留，通过 `c.Send("")` 启动新 turn，与 `runGuarded` 兼容——如果当前 turn 未结束会排队，turn 结束后自动处理。

Controller 新增 `pendingToolResult atomic.Bool` 字段（atomic 类型确保并发安全）。子代理完成 → `SetOnCompletion` 回调 → `autoReenter()` 调用 `c.pendingToolResult.Store(true)` 然后调用 `c.Send("")`（空字符串，仅触发 turn）。`Run()` 循环在下一次迭代中**先**检测 `c.pendingToolResult`（在 `drainSteer()` 之前），通过 `CompareAndSwap(true, false)` 原子地重置为 false，跳过 `drainSteer()` 和 `session.Add(userMessage)`，直接进入 `drainNotify()` + `stream()`。不再产生空 user message。

**顺序调整的原因**：`pendingToolResult` 检查必须在 `drainSteer()` 之前进行，否则 `drainSteer()` 可能已消费掉一条用户消息，然后被 `pendingToolResult` 分支跳过导致该消息丢失。

**Run() 循环中的特殊标记检测逻辑**：

```go
func (a *Agent) Run(ctx context.Context, input string) error {
    // 处理 input（首次用户消息或空字符串）
    if input != "" {
        a.session.Add(provider.Message{Role: provider.RoleUser, Content: input})
    }

    for step := 0; ; step++ {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        // 1. 先检查是否有待处理的 tool result（auto-reentry）
        if a.ctrl.pendingToolResult.CompareAndSwap(true, false) {
            consumed := a.drainNotify()  // 返回是否消费到 result
            if consumed {
                a.stream(ctx)
            }
            // 若未消费到 result（多个 job 同时完成的冗余触发），跳过 stream
            continue
        }

        // 2. 正常 drain 用户输入
        userInput := a.drainSteer()

        if userInput != "" {
            a.session.Add(session.UserMessage(userInput))
        }

        a.drainNotify()  // 常规 drain（非阻塞检查已完成的 job）
        a.stream(ctx)
    }
}
```

**兼容性说明**：`autoReenter` 与现有 `pendingReentry`/`pendingInput` 机制兼容——`c.Send("")` 走 `runGuarded`，如果当前 turn 未结束会排队，turn 结束后自动处理。`pendingToolResult` 标记确保队列中的空消息被识别为 reentry 触发，而非用户输入。

### 6.2 删除 DrainCompletedNote 调用

`runTurnWithRaw()` 中调用 `DrainCompletedNote()` 的行删除。`Compose()` 中不再注入 `<background-jobs>`。

### 6.3 Run() 循环改造要点

1. **pendingToolResult 原子标记检测**：`Run()` 循环在 `drainSteer()` 之前通过 `a.ctrl.pendingToolResult.CompareAndSwap(true, false)` 原子地检测并重置标记。若返回 true，跳过 `drainSteer()` 和 `session.Add(userMessage)`，直接进入 `drainNotify()` + `stream()`。
2. **drainNotify 位置**：在现有 `Run(ctx, input)` 循环的每次 `stream()` 前增加 `drainNotify()` 调用点，无论是普通用户消息路径还是 autoReenter 触发路径。
3. **autoReenter 不再直接调用 drainNotify**：改为设置 `pendingToolResult` 标记并发送空字符串触发 turn，由 `Run()` 循环统一处理，避免并发写 session 的问题。
4. **兼容性**：保留现有 `Runner` 接口签名 `Run(ctx context.Context, input string) error`，不改为无限循环。`autoReenter` 通过原有的 `Send("")` 机制触发 turn，与 `runGuarded` 流程兼容。

---

## §7 嵌套子代理

子代理 A 调 task 启动孙代理 B → B 也是后台 job → B 完成 → B 的 `resultCh` result 推给 A → A 的 `drainNotify()` 消费 → 孙代理 result 进入 A 的 `taskResults`（由 A 的 Controller 管理）→ A 的 `drainNotify()` 消费后作为 tool result 写入 A 的 session → A 继续运行 → A 完成 → A 的 result 推给主代理。

**关键点**：
- 子代理通过 `runSub()` 启动时，`runSub()` 为子代理创建独立的 Controller 实例（或等效的 `taskResults` map + `pendingToolResult` 标记），确保子代理的通知消费和结果管理独立于父代理。
- 子代理的 `Run()` 循环结构与主代理一致，包含 `drainSteer()` + `drainNotify()` + `stream()` 三阶段。子代理的 `Run()` 循环中也包含 `drainNotify()` 调用，用于消费孙代理的通知。
- 孙代理的 result 通过 A 的 `drainNotify()` 写入 A 的 session（`RoleTool` message），供 A 的模型消费。具体路径：孙代理的 result 进子代理的 `resultCh`，子代理 `drainNotify` 消费后作为 tool result 写入子代理 session。
- A 的 `taskResults` 由 A 的 Controller 管理，不跨级。
- `ackCh` / `resultCh` / `notifyCh` 只在父子之间通信，祖父代理不感知孙代理的存在。

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
9. [task-1 drainSteer 消费 steer] → ackCh 推 ack
10. 主代理 drainNotify → 消费 ack
    > 注：ack 通过 `peek-job` 工具返回值暴露，不写入 session。模型若需确认需调用 `peek-job`。
11. [task-2 完成] → resultCh 推 result("YYY 的结果是...")
12. 主代理 SetOnCompletion 触发 → autoReenter → drainNotify
    → 追加 tool message(toolCallID=task-2-call-id, content="YYY 的结果是...")
13. 主代理 stream → "YYY 的结果出来了，是这样的..."
14. [task-1 完成] → 同上流程
15. 主代理 stream → "XXX 和 ZZZ 的结果也出来了..."
```

---

## §9 涉及文件完整清单

| 文件 | 改动类型 | 内容 |
|---|---|---|
| `internal/agent/task.go` | 重写 | 删前台分支（runSub 内）、activeChild、subSink；保留 runSub() 和 subSinkFor()；新增 ackCh/resultCh/notifyCh 推送逻辑 |
| `internal/agent/agent.go` | 删+增 | 删 activeChild/setActiveChild/clearActiveChild/SubagentStop 触发/isBackgroundTaskCall/SubagentStop 接口声明；新增 drainNotify、notifyCh 消费；保留 steerCh/drainSteer |
| `internal/agent/context.go` | 删 | 删 WithActiveChild/isActiveChild/activeChildKey |
| `internal/tool/builtin/bgjobs.go` | 删+增 | 删 wait；新增 steer-job/cancel-job/peek-job。**外部引用清理**（共 11 处 `"wait"` 工具名引用）：`internal/cli/toolcard.go:75/144/165`（映射表，其中 165 行含特殊 "wait" 逻辑需清理）、`internal/cli/toolcard_test.go:18`（测试）、`internal/cli/acp_test.go:34`（测试）、`internal/tool/builtin/workspace_test.go:118/119/129`（同一包测试）、`internal/agent/task.go:257`（工具限制列表 `plannerNonResearchTools` 中的 "wait" 条目需清理）、`internal/hook/rtk_rewriter.go:48/80`（switch case）、`internal/boot/boot_test.go:187`（测试） |
| `internal/tool/builtin/bash.go` | 改 | 更新 schema 描述中的 "wait" 引用 |
| `internal/jobs/jobs.go` | 删+增 | 删 DrainCompletedNote/completed/recordCompletion/Result 类型/Wait 方法；新增 Steer/Peek/NotifyChannels 方法、notifyCh 支持 |
| `internal/control/controller.go` | 删 | 删 DrainCompletedNote 调用；保留 autoReenter |
| `internal/control/input.go` | 删 | 删 `<background-jobs>` 注入 |
| `internal/hook/hook.go` | 删 | SubagentStop 常量、Events 条目、文档注释 |
| `internal/hook/runner.go` | 删 | SubagentStop 处理逻辑 |
| `internal/serve/serve.go` | 不动 | POST /steer 保留 |
| `internal/cli/chat_tui.go` | 不动 | steer 调用保留 |

## §10 不变项

- `bash_output` / `kill_shell` 工具不动（bash 后台任务独立体系）
- `jobs.Manager.Start()` / `Kill()` / `Output()` 核心逻辑不动（Steer/Peek 是新增方法）
- `event.Sink` 体系不动
- HTTP API `/submit` / `/steer` 端点不动
- 桥项目 `reasonix-telegram` 不动
