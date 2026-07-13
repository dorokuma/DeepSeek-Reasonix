# Multiagent (子代理) 实现机制 — 完整调查报告

## 0. 文件清单

### /root/reasonix/internal/multiagent/
| 文件 | 大小 | 职责 |
|------|------|------|
| `tools.go` | 9.5K | 6 个模型可见的工具定义 (spawn_agent, wait_agent, list_agents, send_message, followup_task, interrupt_agent) |
| `control.go` | 13.3K | **核心控制器**: Control 结构体, Spawn/Wait/List/Interrupt/SendMessage 实现, 生命周期管理, AgentStatus 事件发射 |
| `mailbox.go` | 4.1K | 会话级信箱 (Mailbox): Enqueue/DrainFor/Subscribe 实现 |
| `status.go` | 2.8K | Status 枚举类型, 路径工具函数 (JoinPath/LeafName/ParentPath/ResolveRelative) |
| `context.go` | 844B | Context 工具: WithControl/FromContext, WithAgentPath/AgentPathFrom |
| `control_test.go` | 9.4K | 多场景测试 |

### /root/reasonix/internal/agent/
| 文件 | 职责 |
|------|------|
| `agent.go` | Agent 结构体持有 `multiAgent *multiagent.Control`；`flushMultiAgentMailbox()` 在主循环前注入信箱消息 |
| `multiagent_runner.go` | `MultiAgentRunner` 实现 `multiagent.Runner` 接口，桥接 TaskTool.runSub |
| `task.go` | `TaskTool`（子代理内核）+ `RunSubAgent()`，子代理会创建独立 Session 运行 |
| `tool_exec.go` | 工具执行上下文注入: `multiagent.WithControl()` + `multiagent.WithAgentPath()` |
| `context.go` | `MaySpawnAsyncSubagent()` 检查深度 |

### /root/reasonix/internal/control/
| 文件 | 职责 |
|------|------|
| `controller.go` | Controller 持有 subAgentGate，构建时绑定 multiagent Control 的回调 |
| `controller_jobs.go` | `autoReenter()` 子代理完成后唤醒主 agent 的自动重入逻辑 |
| `controller_memory.go` | `gateApprover` 审批桥接 + `requestApproval()` |
| `controller_lifecycle.go` | Close/Wait/SetBypass |

### /root/reasonix/internal/event/
| 文件 | 职责 |
|------|------|
| `event.go` | `AgentStatus` 事件 Kind + `AgentStatusData` 结构体 |
| `sync.go` | `Sync()` 包装 Sink 使其并发安全 |

### /root/reasonix/internal/boot/boot.go (行 ~486)
多 agent 系统的初始化入口。

---

## 1. spawn_agent 工具的完整实现流程

### 1.1 工具注册
`multiagent.RegisterTools(reg)` 注册 6 个工具到全局工具注册表：
- `spawn_agent` — 创建子代理
- `wait_agent` — 等待子代理完成/新消息
- `list_agents` — 列出活跃子代理
- `send_message` — 发送消息给子代理
- `followup_task` — 发送消息并触发新 turn
- `interrupt_agent` — 中断子代理

### 1.2 spawn_agent 执行 (tools.go:63-90)
```
模型调用 spawn_agent(task_name, message)
  → Execute() 解析参数
  → 从 context 获取 Control (c)
  → 计算当前深度 depth
  → c.Spawn(ctx, parentPath, taskName, message, depth)
```

### 1.3 Spawn() 核心逻辑 (control.go:107-164)
```go
func (c *Control) Spawn(ctx, parentPath, taskName, message, parentDepth) (taskPath, nickname, err) {
    // 1. 验证: message 非空, taskName 规范化
    // 2. **深度限制**: childDepth = parentDepth+1 > c.maxDepth(默认3) → 拒绝
    // 3. **并发限制**: runningCount >= maxConcurrent(默认6) → 拒绝
    // 4. 分配路径: path = JoinPath(parentPath, taskName)
    //    - 重名冲突: 追加 _<纳秒后缀>
    // 5. 分配昵称: 确保叶子名唯一 (byLeaf map)
    // 6. 创建 Metadata{Status: StatusPendingInit, ...}
    // 7. 存入 agents map + byLeaf map
    // 8. 创建独立 context (context.WithoutCancel 不继承父 cancel)
    // 9. 状态 → StatusRunning
    // 10. runningCount++
    // 11. go c.runAgent(runCtx, rec, path, message, childDepth)
}
```

### 1.4 runAgent — 子代理执行与终结 (control.go:167-216)
```go
func (c *Control) runAgent(runCtx, rec, path, message, depth) {
    defer runningCount.Add(-1)
    // 1. 调用 runner.Run(runCtx, path, message, depth) — 阻塞直到完成
    // 2. 根据结果设置终端状态:
    //    - ctx cancelled → StatusInterrupted
    //    - runErr != nil → StatusErrored (记录 LastError + LastAnswer)
    //    - 成功 → StatusCompleted (记录 LastAnswer)
    // 3. **发射 AgentStatus 事件** (若 c.Sink != nil)
    // 4. **向父 mailbox 发完成消息** (Mailbox.Enqueue, triggerTurn=false)
    // 5. **调用 OnCompletion 回调** → 触发 autoReenter
}
```

### 1.5 MultiAgentRunner — 桥接 (agent/multiagent_runner.go)
```go
type MultiAgentRunner struct {
    Tool    *TaskTool
    Control *multiagent.Control
}

func (r *MultiAgentRunner) Run(ctx, path, message, depth) (string, error) {
    subReg := r.Tool.buildSubReg(nil, false)  // 继承工具（排除 meta-tools）
    bgCtx := WithNestingDepth(ctx, depth)
    bgCtx = multiagent.WithAgentPath(bgCtx, path)
    bgCtx = multiagent.WithControl(bgCtx, r.Control)
    return r.Tool.runSub(bgCtx, message, subReg, event.Discard, 0,
                         r.Tool.sysPrompt, "task", "", "")
}
```

runSub → RunSubAgent → 创建新 Session → 构建子 Agent → `sub.Run(ctx, prompt)`

---

## 2. 子代理在 Controller 层的实现

### 2.1 Controller 结构体中的多 agent 字段 (controller.go:43-148)
```go
type Controller struct {
    runner       agent.Runner
    executor     *agent.Agent
    subAgentGate *permission.Gate   // 子代理专属门（独立于主 agent 的 gate）
    ...
    pendingToolResult atomic.Bool   // 子代理完成标志 → 触发自动重入
    ...
}
```

### 2.2 初始化 (boot.go:483-492 + controller.go:251-273)
```go
// boot.go
subAgentGate := permission.NewGate(policy, nil)  // 初始为 headless (nil Approver)
maCtrl := multiagent.NewControl()
multiagent.RegisterTools(reg)
taskTool := agent.NewTaskTool(..., subAgentGate, ...)
maCtrl.SetRunner(&agent.MultiAgentRunner{Tool: taskTool, Control: maCtrl})

// Controller 构造时:
if ma := c.executor.MultiAgentControl(); ma != nil {
    ma.Sink = c.sink                              // 连接事件流
    ma.OnCompletion = func() {
        c.SetPendingToolResult(true)
        c.autoReenter()                           // 唤醒主 agent
    }
}
```

### 2.3 子代理生命周期

```
创建:   Spawn() → StatusPendingInit → StatusRunning
运行中: runner.Run() 阻塞（在独立 goroutine）
完成:   → StatusCompleted / StatusErrored / StatusInterrupted
消亡:   留在 agents map 中，但从 list_agents 隐藏 (仅 live 状态显示)
```

关键: 完成的子代理**不会从 agents map 删除**，但 `IsListLive()` 返回 false 所以 `list_agents` 不显示。结果通过 mailbox 传递。

### 2.4 销毁
- `Controller.Close()` → `c.Cancel()` → 取消所有运行中的 turn
- 单个子代理取消: `Interrupt(target)` → `rec.cancel()` (context cancel)

---

## 3. 事件流

### 3.1 AgentStatus 事件 (event.go:85-88, 242-252)
```go
const AgentStatus Kind = iota  // (AgentStatus 是第 17 个 Kind)

type AgentStatusData struct {
    AgentPath string // 如 "/root/task_1"
    Status    string // "completed" | "errored" | "interrupted"
    Error     string // 仅 errored 时非空
    Timestamp int64  // Unix 毫秒
}
```

### 3.2 AgentStatus 发射时机 (control.go:190-200)
- 只在**终端状态**发射：completed / errored / interrupted
- `pending_init` 和 `running` **不发射**事件
- 发射点：`runAgent()` 设置状态后立即发射

### 3.3 完成消息到父 mailbox (control.go:203-216)
```
子代理完成 →
  1. 发射 AgentStatus 事件（若 Sink 非空）
  2. 向父 mailbox 投递 Mail{From: 子路径, To: 父路径, TriggerTurn: false}
     - 完成:  "asub-agent_complete path=/root/foo name=foo status=completed]\n...answer..."
     - 错误:  "asub-agent_complete path=... status=errored]\n...err...\n...answer..."
     - 中断:  "asub-agent_complete path=... status=interrupted]"
  3. 调用 OnCompletion() → SetPendingToolResult(true) → autoReenter()
```

### 3.4 主 agent 如何消费 mailbox (agent.go:557, 579, 746-764)
```go
func (a *Agent) flushMultiAgentMailbox() {
    mails := a.multiAgent.Mailbox().DrainFor(a.agentPath)
    body := multiagent.FormatMailsForSession(mails)
    // body = "asub-agent_mailbox]\nfrom=/root/foo to=/root\n...\n[/multi_agent_mailbox]"
    a.session.Add(provider.Message{Role: RoleUser, Content: body})
}
```
- 在 `Run()` 循环开始前调用（行 557）
- 每个 tool step 之前再次调用（行 579）
- 格式化的 mailbox 内容以 **User 消息** 注入 session

---

## 4. 审批流程

### 4.1 spawn_agent 的审批判定
```
spawn_agent 工具调用 →
  tool_exec.go → gate.Check(ctx, "spawn_agent", args, readOnly=false)
    → Policy.Decide("spawn_agent", readOnly, args)
      → 默认 policy 模式:
         - "accept" → Always Allow (不需审批)
         - "ask" → 根据 args 匹配 ask 规则
         - "deny" → 直接拒绝
    → Ask 分支: 若 gate.Approver != nil 则弹窗审批
```

### 4.2 审批数据结构
```go
type Gate struct {
    Policy    Policy
    Approver  Approver    // nil = 静默允许（headless 模式）
    ...
}

type Approver interface {
    Approve(ctx, tool, subject string, args json.RawMessage) (allow, remember bool, err error)
}
```

### 4.3 两层 Gate 架构
```
主 agent: 使用 executor.SetGate(gate) — 在交互模式下启用 Approver
子 agent: 使用 subAgentGate (在 boot.go 创建) — 独立 gate
```

- **主 agent gate**: 通过 `EnableInteractiveApproval()` 注入 `gateApprover{c}`，发出 `ApprovalRequest` 事件
- **子 agent gate** (`subAgentGate`): 初始 Approver = nil → **静默允许**（无审批弹窗）
  - 交互模式下 `EnableInteractiveApproval()` 也会给 subAgentGate 注入 Approver → 子 agent 的 ask 规则也会弹窗

### 4.4 gateApprover → Controller 路径 (controller_memory.go:121-136)
```go
func (g gateApprover) Approve(ctx, tool, subject string, args) (bool, bool, error) {
    if c.bypass { return true, false, nil }  // YOLO 模式
    scope := "gate"
    if tool == "spawn_agent" { scope = "task" }  // 特殊标记
    preview := permission.Preview(tool, args)
    return c.requestApproval(ctx, tool, subject, preview, scope)
}
```

`spawn_agent` 的 `scope="task"` —— 前端可据此展示 2 按钮（允许/拒绝）而非 3 按钮（允许/本次会话/拒绝）

### 4.5 requestApproval 流程 (controller_memory.go:176-234)
```
requestApproval →
  1. 检查 bypass / session grant → 短路允许
  2. 获取 promptMu 锁（串行化）
  3. 分配 ID → 创建 reply chan → 存入 c.approvals
  4. 发射 ApprovalRequest 事件
  5. 阻塞 select: ← reply 或 ← ctx.Done()
  6. 用户通过 Approve(id, allow, session, persist) 响应
```

---

## 5. 关键问题

### 5.1 子代理完成后，结果如何到达主 agent？
```
子代理 goroutine 完成 →
  runAgent() 结束时:
    1. 向 Mailbox.Enqueue(Mail{To: parentPath, Message: 完成信息})
    2. OnCompletion() → Controller.autoReenter()
    3. autoReenter() → Controller.Send("") → runGuarded("") → runTurnWithRaw
    4. Agent.Run(ctx, "") 被调用 (input为空)
    5. flushMultiAgentMailbox() 将 mailbox 消息读入 session
    6. 模型看到 mailbox 内容后生成后续回复
```

关键: mailbox 是会话级共享数据结构，不是直接 channel 通信。父路径做 `DrainFor(recipient)` 只取出给自己的 mail。

### 5.2 wait_agent 怎么跟子代理通信？
```
wait_agent 工具 →
  Control.Wait(ctx, timeoutMs):
    1. Mailbox.Subscribe() → 获取 activity channel
    2. 阻塞 select:
       - ← ch (ActivityMailbox): 返回 "Wait completed."
       - ← ch (ActivitySteer): 返回 "Wait interrupted by new input."
       - ← timer.C: 超时返回 "Wait timed out."
       - ← ctx.Done(): 返回 "Wait interrupted by new input."
    3. Mailbox.Enqueue() 会 broadcastLocked 唤醒所有 waiter
```

**注意**: wait_agent 不返回消息内容 — 它只返回 "Wait completed." / "Wait timed out."。消息内容需通过 `flushMultiAgentMailbox()` 在下个 turn 注入。

### 5.3 子代理的上下文和工具集
**工具集** (`buildSubReg`):
- 默认继承父 agent 的所有工具（通过 `parentReg.Names()`）
- **排除 meta-tools**: `run_skill`, `install_skill`, `install_source`（防止子代理安装技能）
- **但不排除 spawn_agent 自身** — 子代理可以再 spawn 子代理

**上下文** (`multiagent_runner.go:22-36`):
- 全新的 Session（独立对话，不继承父的历史消息）
- `WithNestingDepth(ctx, depth)` — 记录嵌套深度
- `WithAgent(ctx, parentAgent)` — 保留父 agent 引用（用于合并 token 统计）
- `multiagent.WithAgentPath(ctx, path)` — 设置子代理路径
- `multiagent.WithControl(ctx, r.Control)` — 共享同一个 Control（所以 list_agents 能看到全树）
- `event.Discard` — 子 agent 的事件不直接输出（与 Codex 一致）

**系统提示**: `DefaultTaskSystemPrompt`（"You are a sub-agent invoked by a parent coding agent..."），在 boot.go 中还拼接了共享配置节

### 5.4 子代理是否可以再 spawn 子代理？
**可以**，但有层级限制：
- 深度限制: `DefaultMaxDepth = 3`（从 root 算起：root=0, 子=1, 孙=2, 曾孙=3 — 不能再深）
- 并发限制: `DefaultMaxConcurrent = 6`（整个会话同时运行的子代理总数）
- 路径格式: `/root/child1/grandchild1/great_grandchild1`

另外，`MaySpawnAsyncSubagent(ctx)` 检查 `NestingDepthFrom(ctx) == MainAgentDepth(0)` — 但这是旧 task 工具的约束；spawn_agent 不检查这个函数，深度由 Control.Spawn() 的 `childDepth > maxDepth` 限制。

### 5.5 如果桥进程重启，运行中的子代理怎么办？
**子代理全部丢失**。因为：
- Control 是**内存结构体** (`agents map[string]*Metadata`)，不持久化
- 子代理在各自 goroutine 中运行，父进程重启后 context 全部取消
- SubAgentRunner 使用 `context.WithoutCancel(ctx)` 创建独立 context，但独立于父进程的进程级生命周期
- Session 文件只保存主对话，不保存子代理状态
- 重启后 `NewControl()` 重新开始，没有恢复逻辑

### 5.6 子代理有超时机制吗？
**没有内置超时**。子代理通过 `wait_agent(timeout_ms)` 实现调用方超时：
- 默认超时: 600_000 ms = 10 分钟
- 最小: 1_000 ms
- 最大: 3_600_000 ms = 1 小时
- 超时后 wait_agent 返回 "Wait timed out."，但**子代理仍在后台运行**
- 子代理的 `runner.Run()` 没有任何 deadline
- 当子代理最终完成时，mailbox 仍会收到消息，下个 turn 通过 `flushMultiAgentMailbox` 看到

若需主动终止: 使用 `interrupt_agent(target)` → 调用 `rec.cancel()` 取消子代理的 context

---

## 6. 完整数据流图

```
用户输入 → Controller.Submit() → runTurnWithRaw → Agent.Run(ctx, input)
  └→ 模型调用 spawn_agent(task_name, message)
       └→ Control.Spawn()
            └→ 子 agent goroutine: MultiAgentRunner.Run()
                 └→ TaskTool.runSub() → RunSubAgent()
                      └→ 新建 Session → Agent.Run(ctx, message)
                           └→ 工具调用（共享 Control）
                                └→ 完成
                                     └→ runAgent 收尾:
                                          ├→ emit AgentStatus event
                                          ├→ Mailbox.Enqueue → parent mailbox
                                          └→ OnCompletion()
                                               └→ Controller.autoReenter()
                                                    └→ Send("") → runTurnWithRaw("")
                                                         └→ Agent.Run(ctx, "")
                                                              └→ flushMultiAgentMailbox()
                                                                   └→ 模型看到 mailbox 内容
```

## 7. 桥设计关键要点

1. **共享 Control**: Telegram 桥必须在一个会话中共享同一个 `*multiagent.Control` 实例
2. **事件流监控**: 订阅 `AgentStatus` 事件（通过 `event.Sink`）以跟踪子代理生命周期
3. **Mailbox 机制**: 子代理结果通过 `Mailbox.DrainFor(parentPath)` 读取，需在主循环的合适时机调用
4. **自动重入**: 子代理完成后通过 `OnCompletion` → `autoReenter()` 自动唤醒主 agent
5. **无持久化**: 重启后所有子代理丢失，桥需等待所有子代理完成后再关闭
6. **审批差异**: 子代理默认无审批弹窗（subAgentGate Approver=nil），可通过 `EnableInteractiveApproval()` 注入
7. **并发安全**: `event.Sync(sink)` 包装 Sink 保证并发安全，multiagent worker 和主 turn 并发发射事件时需要
