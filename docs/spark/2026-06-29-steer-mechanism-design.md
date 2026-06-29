# Steer 机制设计规格

> 日期：2026-06-29
> 状态：设计完成，待实现
> 参考：Hermes agent steer 机制

## 目标

在 Reasonix 主项目和 reasonix-telegram 桥项目中实现真正的 mid-turn Steer 机制：
- 用户在 agent 执行任务期间发送新消息，消息注入到当前运行的 turn 中（作为独立 user message 追加到 session）
- agent 在下一轮 `stream()` 边界处感知并响应
- 不需要取消当前 turn
- `/stop` 命令仍然用于打断/取消

## 机制分工

| 场景 | 机制 |
|------|------|
| "下一步别用 Python 了，换 Go" | Steer — 等当前工具跑完，下轮生效 |
| "停！这个命令是错的立刻终止" | ESC / /stop — 立即打断 |

## 总体架构

```
用户输入 ──→ Controller.Steer() ──→ Agent.steerCh ──→ Run 循环边界 ──→ 追加 user message → 模型自然感知
                │                      │
                │ (not running?)       │
                ↓                      
           fallback Submit           
```

三层改动：

| 层 | 改动 | 文件 |
|----|------|------|
| Agent | 新增 steerCh + drainSteer() | agent.go |
| Controller | 新增 Steer() 方法 | controller.go |
| Serve | 新增 POST /steer | serve.go |

消费层：

| 层 | 改动 |
|----|------|
| TUI | running 时 Enter → Steer（替代 pendingInterject 排队） |
| 桥 | 默认 POST /steer，仅 /stop 走 cancel |

---

## §2 Agent 层

### 设计

```go
type Agent struct {
    steerCh        chan string    // 缓冲 8，外部非阻塞投递
}

func (a *Agent) Steer(input string) {
    select {
    case a.steerCh <- input:
    default:
        // 通道满则丢弃，避免阻塞调用方
    }
}

func (a *Agent) Run(ctx context.Context, input string) {
    a.session.AddUserMessage(input)
    for {
        a.drainSteer()              // 排干点：每次 stream 前
        resp := a.stream(ctx)
        if noToolCalls { return resp }
        a.executeBatch(ctx, calls)
        a.session.AddToolResults(...)
    }
}

func (a *Agent) drainSteer() {
    for {
        select {
        case msg := <-a.steerCh:
            a.session.AddUserMessage(msg) // 独立 user message
        default:
            return
        }
    }
}
```

### 关键决策

- **注入方式**：Option B — 独立 user message。现代 LLM API（OpenAI/Anthropic）原生支持 user 在 tool 之后，不需要改 system prompt
- **注入时机**：排干点放在 `stream()` 之前——这是自然迭代边界，模型正要开始新一轮思考
- **单排干**：不采用 Hermes 的双排干（pre-API + post-tool）。Go select 模式下一次排干即可，如果在 stream/executeBatch 期间到达，通道缓冲暂存，下一轮排干
- **缓冲 8**：足够容纳短时间多次消息。超出则丢弃（暂存队列可做 TUI 层兜底）

---

## §3 Controller 层

### 设计

```go
func (c *Controller) Steer(input string) {
    c.mu.Lock()
    running := c.running
    runner := c.runner
    c.mu.Unlock()

    if !running || runner == nil {
        go c.Submit(input)  // fallback — 启动新 turn
        return
    }

    runner.Steer(input)  // → Agent.steerCh
}
```

### 关键决策

- 不在持锁状态下调 `Submit`（避免死锁）
- 不持锁期间 `c.running` 可能刚好变 false，但 `runner.Steer()` 投递到缓冲 channel 无害——Agent 下次 drainSteer 照样处理

---

## §4 Serve 层

### 设计

```go
// serve.go 路由注册
mux.HandleFunc("POST /steer", s.steer)

func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
    var body struct {
        Input string `json:"input"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, "invalid body", http.StatusBadRequest)
        return
    }
    s.ctl().Steer(body.Input)
    w.WriteHeader(http.StatusAccepted)  // 202
}
```

### API 契约

| 端点 | 方法 | 请求体 | 响应 | 语义 |
|------|------|--------|------|------|
| `/steer` | POST | `{"input": "..."}` | `202 Accepted` | 注入到当前 turn；当前无 turn 时 fallback submit |
| `/submit` | POST | `{"input": "..."}` | SSE 事件流 | 启动新 turn（不变） |
| `/cancel` | POST | `{}` | `200 OK` | 取消当前 turn（不变） |

---

## §5 TUI 层

### 改动

`chat_tui.go` — running 状态时的 Enter 处理：

| 当前 | 改为 |
|------|------|
| `pendingInterject = append(...)` 排队 | `ctrl.Steer(input)` |

- ESC 打断逻辑不变
- idle 状态逻辑不变
- `pendingInterject` 保留备用（steer channel 满时 fallback 排队）

---

## §6 桥层

### 路由逻辑

```
收到 Telegram 消息 → 检查内容：
  ├─ "/stop" → POST /cancel → turn 终止
  └─ 其他任何消息 → POST /steer → 注入当前 turn
```

### 文件改动

- `serve_backend.go`：新增 `postSteer(port, input string)` 函数
- `stream.go`：`runTask` 中消息路由改为默认 steer，只有 /stop 走 cancel
- `handler.go`：限速器已白名单 /stop，steer 正常消息不受影响

---

## 与 Hermes 的对比

| 维度 | Hermes (Python) | Reasonix (Go) |
|------|-----------------|---------------|
| 注入方式 | 追加到 tool result + marker | 独立 user message |
| 排干策略 | 双排干（pre-API + post-tool） | 单排干（stream 前） |
| 并发模型 | threading + 线程级信号 | goroutine + channel |
| 子 agent 保护 | 自动降级 interrupt→queue | 未实现（后续迭代） |
| 反注入 | marker + system prompt | 不适用（独立 message 天然不混淆） |
| 模式切换 | busy_input_mode 配置 | 硬编码：steer 默认 + /stop 打断 |

---

## 实现范围

### 包含
- Agent: steerCh, Steer(), drainSteer()
- Controller: Steer()
- Serve: POST /steer
- TUI: Enter→Steer
- 桥: 默认 POST /steer, /stop→cancel
- 编译验证

### 明确不包含
- 子 agent 保护（后续迭代）
- busy_input_mode 配置切换（后续迭代）
- 新的 SSE 事件类型
