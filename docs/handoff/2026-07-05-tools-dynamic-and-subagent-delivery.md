# 工具动态可见性与子代理结果投递设计

> 设计文档，非实现说明书。代码改动前需经确认。

## 1. 问题定义

### 1.1 工具可见性缺陷

当前 `peek-job` 的实现通过白名单控制见性，但其可见性策略与用户意图脱钩：

| 问题 | 场景 |
|------|------|
| **白名单与动态可见冲突** | `main_agent_allowed` 包含 `peek-job` → 永久可见，动态性被覆盖 |
| **Keyword 误触发** | 用户说"别 peek 了"含"peek" → `diagnosticRequested=true` → 违反用户意图反而激活了 peek |
| **回合间残留** | 白名单模式下 peek-job 暴露后不隐藏，模型随时可调 |

**根因**：白名单（永久可见）与动态可见性（条件可见）混用一个配置，逻辑拧巴。

### 1.2 子代理结果投递缺陷

子代理完成时，`deliverBackgroundToolResult` 先尝试 `PatchToolResult`——**原地修改历史中某条 tool 消息的内容**。结果是：

```
原始消息流：
  assistant [task]: ...          ← 模型派发 task
  tool [Started task task-5 ...] ← 占位符结果
  ... 中间几轮 ...
  assistant: 不再 peek。等 task-5 自然完成。
  user: 5都回报了？？              ← 用户看不到结果

PatchToolResult 后：
  tool [task-5 的完整结果]        ← 但这条消息在历史中部，不在尾部
  ... 中间几轮 ...
  assistant: 不再 peek。
  user: 5都回报了？？
```

**模型被 auto-reentry 激活时，注意力在对话尾部，看不到埋在中部的已更新消息。** 结果被物理送到了 session，但逻辑上丢失了。

---

## 2. 设计目标

1. **配置正交** — 白名单与动态可见性互不影响，每个工具只在一个配置里
2. **动态可见性消耗即隐** — 用户激活后模型可见一次，被消费后自动隐藏，不需要白名单拦截逻辑
3. **Keyword 精确** — 仅用户明确意图时激活，否定语境不触发
4. **子代理结果永远可见** — 追加尾部消息，不原地修改
5. **子代理消息身份正确** — role=tool，不是冒充用户

---

## 3. 方案 A：工具动态可见性

### 3.1 配置项

新增 `tools_dynamic` 配置，与 `main_agent_allowed` 完全独立：

```toml
# main_agent_allowed — 永久可见白名单
main_agent_allowed = ["task", "ask", "note", ...]

# tools_dynamic — 仅在用户明确要求时可见，一次消耗后隐藏
tools_dynamic = ["peek-job"]
```

- `main_agent_allowed` 中的工具**不受动态机制影响**，始终可见
- `tools_dynamic` 中的工具**不受白名单影响**，始终隐藏，仅在激活后一次性可见
- 一个工具不能同时出现在两个配置中（验证阶段报错）

### 3.2 运行时逻辑

```
stream() 前:
  schemas := registry.Schemas()
  
  // 1. 白名单过滤（mainAgentAllowed）
  在白名单中的工具 → 保留
  不在白名单中  → 跳过
  
  // 2. 动态可见性（toolsDynamic）
  if toolsDynamic 非空 {
    for each tool in toolsDynamic {
      if diagnosticRequested && current tool == this dynamic tool {
        追加到 schemas ← 不从白名单过，独立放行
      }
    }
  }
  
  // 3. 消耗 flag
  defer a.diagnosticRequested.Store(false)  // 回合结束自动隐藏
```

### 3.3 Keyword 检测

仅在用户原始输入（raw）中检测精确模式，否定语境不触发：

```go
if raw != "" {
    lo := strings.ToLower(raw)
    // 精确短语
    if strings.Contains(lo, "peek") &&
       !strings.Contains(lo, "别peek") &&
       !strings.Contains(lo, "不要peek") &&
       !strings.Contains(lo, "stop peek") {
        c.executor.SetDiagnosticRequested(true)
    }
}
```

### 3.4 生命周期

```
用户: 帮我peek一下任务跑得怎么样了
  → diagnosticRequested = true
  → 本轮 peek-job 在 tools 中可见
  → 模型调用 peek-job → 返回状态
  → 回合结束 → diagnosticRequested = false
  → peek-job 隐藏

用户: 别peek了！！！
  → "别peek" 匹配否定模式 → 不激活
  → peek-job 不可见
  → 模型看不到这个工具，不会主动调

用户: （只说其他内容）
  → 不触发 keyword → peek-job 不可见
```

---

## 4. 方案 B：子代理结果投递

### 4.1 当前流程（有缺陷）

```
task 执行 → jm.Start(...) → job goroutine
  ↓
job 完成 → resultCh ← JobNotify{Output: "完整结果"}
  ↓
drainNotify → TakeJobMeta → 拿到 toolCallID
  ↓
deliverBackgroundToolResult(toolCallID, output)
  ↓
PatchToolResult(toolCallID, output) → 找到历史中匹配 toolCallID 的 tool 消息 → 原地覆盖
```

**问题**：PatchToolResult 修改的是历史中某条老消息（task 占位符结果），不是追加到尾部。

### 4.2 新流程

```
drainNotify → TakeJobMeta → 拿到 toolCallID
  ↓
追加新 tool 消息到 session 尾部，而不是 patch 老消息：

a.session.Add(provider.Message{
    Role:       provider.RoleTool,
    Name:       "task",
    ToolCallID: toolCallID,
    Content:    output,   // 子代理完整结果
})
```

- 不删除、不覆盖旧的占位符消息（保持历史完整性）
- 新消息在对话尾部，模型 auto-reentry 激活后第一眼看到
- role=tool，模型知道这是工具返回结果，不是用户说的
- name="task" 让模型知道来源是哪个工具

### 4.3 对比

| | PatchToolResult | 追加新消息 |
|--|---------------|-----------|
| 消息位置 | 历史中部 | 对话尾部 |
| 模型可见性 | 注意力窗口外 | 第一眼可见 |
| 历史完整性 | 覆盖了旧占位符 | 完整保留 |
| 副作用 | 无 | 一条额外 tool 消息 |
| 兼容性 | 破坏后续 `PatchToolResult`（已被覆盖） | 无 |

`SendMessage` 子代理中途汇报（走 inbox）仍走原有流程，不做改变。

---

## 5. 依赖关系与兼容性

| 项目 | 影响 |
|------|------|
| Session 恢复 | `mergeResumedSession` 加载旧历史时，老占位符消息和尾部追加消息都能正常加载。旧 session 中的 PatchToolResult 已在保存时固化，无损 |
| 系统提示 | `AsyncBackgroundPolicy` 已简化为一行，不提及工具名 |
| PostCallGuidance | `taskPostCallGuidance` 不提及 peek-job，只含 job_id 信息 |
| Compact | compact 可能折叠历史中的老占位符消息，但不影响尾部追加结果 |

---

## 6. 未解决/需确认

*无*
