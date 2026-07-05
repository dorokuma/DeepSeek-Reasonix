# Async Sub‑Agent 工作流锚定设计

> 设计文档，非实现说明书。代码改动前需经确认。

## 1. 问题定义

### 1.1 当前架构缺陷

reasonix 的 async sub‑agent 机制（`task` / `explore` / `run_skill`）没有给模型提供**结构化的工作流状态参考系**。模型只能通过对话历史推断自己处于哪个阶段，导致三种典型故障：

| 故障 | 根因 |
|------|------|
| **先 dispatch 再 ask** | 模型未澄清意图就派 task，ask 的反馈没在正确时机使用 |
| **ask 后重复 dispatch** | ask 用户回答后，模型的注意力被用户回答吸引，忘了之前派过 task |
| **阶段混乱** | 模型没有「当前正在干什么」的外部提示，全靠记忆力 |

全部三条根因是同一个：**模型每次决策应该看到的结构化状态是缺失的。**

### 1.2 现有方案的不足

| 方案 | 为什么不够 |
|------|-----------|
| `PostCallGuidance` | 只 append 在单次 tool result 尾部，后续工具调用会把它冲走 |
| `AsyncBackgroundPolicy`（系统 prompt） | 只能「说教」，不能「展示」，模型在具体上下文中容易忽略 |
| `pending-jobs guard` | 只拦 final answer，不拦中间行为；不适合处理 ask 问题 |
| 拦 `ask` / 拦工具 | 一刀切，破坏并行和 ask 的正常价值 |

## 2. 设计目标

1. **模型在每次 LLM 调用前都看到当前工作流状态** — 不靠记忆力，靠显式注入
2. **不对齐阶段（不分 phase）** — 所有工具始终可用，不拦截任何工具
3. **引导而非强制** — 状态可见后模型自然选择正确行为顺序
4. **缓存友好** — 不修改系统 prompt，不破坏 cache 前缀
5. **轻量** — 不引入新工具、新接口、新协议

## 3. 核心概念：Session Manifest

### 3.1 定义

Session Manifest 是一个**只读的状态摘要**，在每次 LLM 调用前作为一条轻量 tool 消息注入 prompt。它包含两个字段：

```
[confirmed: <用户通过 ask 确认的意图摘要>]
[background: <job-id> (<label>)]
```

- 没有 running job 时，`[background]` 行为空
- 没有调过 ask 时，`[confirmed]` 行为空
- Manifest 由 agent 在每次 `stream()` 之前即时生成，**不写入 session 历史**
- Manifest 注入为 `RoleTool` 消息（模型可见、UI 不展示正文）

### 3.2 Manifest 的可视化模型

模型每次看到的信息量 ≈ 两行文本：

```
[confirmed: audit /src for unused dependencies]
[background: task-1 (explore)]
```

或：

```
[confirmed: ]
[background: task-1 (explore)]
```

或全空（尚未澄清、尚未派任务）。

### 3.3 注入位置

> [!WARNING]
> **API 协议兼容性冲突（不合理之处）**：原设计在 `stream` 的消息末尾强行注入一条虚拟的 `RoleTool` 消息作为 manifest。主流 Provider（如 Anthropic Claude 3.5 / OpenAI GPT-4o）对于消息数组的角色交替及 `tool_result` 的前置关联有极严格的 API 级校验。`RoleTool` 消息不能孤立地在数组末尾出现，其前必须有包含 `tool_calls` 的 `RoleAssistant` 消息且 `tool_call_id` 必须严格一致。若按设计实施，每次 LLM 调用均会触发 Provider 参数校验崩溃，导致整个 Agent 系统死锁。
> 
> **改进方案**：不独立创建 `RoleTool` 消息，而是在 `stream` 构建 `msgs` 数组时，将 Manifest 文本块直接追加拼接到**消息列表中最末尾的一条 `RoleUser` 或 `RoleTool` 消息的 Content 尾部**。

修改后的注入逻辑如下：

将 Manifest 文本块直接追加拼接到消息列表中最末尾的一条 `RoleUser` 或 `RoleTool` 消息的 Content 尾部。

```
... 已有消息 ...
user: "请帮我审计代码" + "\n\n[confirmed: ]\n[background: ]"
→ LLM 下次调用时可以看到 manifest
```

Manifest 产生方式：
- 每轮 `stream()` 执行前调用 `a.buildManifest()` 
- `buildManifest()` 从 `a.intentSummary` 和 `a.jobs.Running()` 生成文本

### 3.4 工作流锚定效果

| Manifest 状态 | 模型倾向 | 正确行为 |
|-------------|---------|---------|
| `confirmed:` 空 + `background:` 空 | 不知道要干什么 | 用 `ask` 澄清意图 |
| `confirmed:` 有 + `background:` 空 | 意图已知，无待办 | 派 `task` / `explore` |
| `confirmed:` 有 + `background:` 有 | 任务正在执行 | 等待，不重复 dispatch，不调 ask |
| `confirmed:` 空 + `background:` 有 | （异常状态） | 仍执行，auto-reentry 返回后补充 |

**注意**：这四行不是 phase 定义，不是分阶段模式。模型在任何时候都可以调任何工具。Manifest 只在模型决策时作为当下状态的参考点——就像人在电脑上看到的「当前任务栏」。

## 4. Intent Anchor 机制

### 4.1 意图摘要的生成

> [!WARNING]
> **意图提取高耦合与脆弱性（不合理之处）**：原设计在 `executeOne` 中拦截 `ask` 工具执行，解析已格式化的结果字符串（即 `result` 文本）来提取意图。`executeOne` 是通用的工具执行层，其获取的 `result` 已经是为模型排版好的展示字符串。在此处进行正则解析或子串匹配，不仅逻辑冗余脆弱，而且当以后调整 `ask` 工具的文本排版时极易失效。
> 
> **改进方案**：将 `intentSummary` 的提取下放到 `AskTool.Execute` 内部。在 `AskTool` 完成交互并拿到结构化 answers 的瞬间，使用其持有的结构化对象进行计算，并通过 `AgentFromContext(ctx)` 拿到 `Agent` 实例直接赋值。

生成与提取逻辑：

当模型调用 `ask` 并获得用户回答后，在 `AskTool.Execute` 中：

1. 识别并获取结构化 answers 交互结果。
2. 从第一个问题的 `header` 和选择的 `label` 中提取简短摘要，不再依赖已格式化后的字符串。
3. 通过 `AgentFromContext(ctx)` 拿到 `Agent` 实例，将摘要存入 `Agent.intentSummary string` 字段。

提取规则：
- 取第一个 question 的 `header`（或 fallback `question` 的前 60 个字符）
- 追加第一个选项的 `label`（如果有选择）
- 总长度不超过 120 字符
- 格式：`"<header>: <selected labels>"`

示例：
```
用户问题: header="Scope", question="Which directories to audit?"
用户选了: ["/src", "/lib"]
→ intentSummary = "Scope: /src, /lib"
```

### 4.2 意图摘要的清除

> [!WARNING]
> **意图生命周期未重置（不合理之处）**：原设计中，用户新输入时不清除 `intentSummary`。但在多轮交互中，如果主代理通过 final answer 已经交付了答案，而在下一次 Turn 中用户输入了全新的、不相关的请求，此时 Manifest 依然广播旧的意图，这会严重干扰大模型对新任务的理解。
> 
> **改进方案**：当用户发起新一轮主动交互（`input != ""`），且上一轮已被主代理以 final answer 终结（或当前无活跃的 background job）时，系统应自动将 `intentSummary` 清空，保持意图上下文的新鲜度。

清除规则：
- 当用户发起新一轮主动交互（`input != ""`），且上一轮已被主代理以 final answer 终结（或当前无活跃的 background job）时，系统应自动将 `intentSummary` 清空。
- **用户手动清除**：当 Manifest 已有时，用户可以主动要求「重新问」。

### 4.3 Intent 与 ask 的关系

Intent 不取代 ask。ask 仍然是工具。Intent 只是 ask 调用后留下的**可见痕迹**。如果模型认为 intent 需要调整，可以再次调 ask（Manifest 会体现更新的 intent）。

## 5. Jobs Anchor 机制

### 5.1 现状

`jobs.Running()` 已存在，返回 `[]View`（id, kind, label, status, startedAt）。只是模型不知道去哪看。

### 5.2 注入方式

在 `buildManifest()` 中：

```
if running := a.jobs.Running(); len(running) > 0 {
    for _, j := range running {
        加入一行 fmt.Sprintf("[background: %s (%s)]", j.ID, j.Label)
    }
}
```

### 5.3 与 PostCallGuidance 的关系

Jobs Anchor 不取代 PostCallGuidance。PostCallGuidance 仍然是 task 调用后**立刻看到**的提示。Jobs Anchor 是**持续可见**的锚点。两者分工：

| 维度 | PostCallGuidance | Jobs Anchor |
|------|-----------------|-------------|
| 时机 | task 返回后立刻一次 | 每个 LLM 调用前都出现 |
| 内容 | 指导性文本（不要 peek，自动交付） | 结构化状态（哪些 job 在跑） |
| 位置 | RoleTool 消息末尾 | 本轮注入的虚拟消息 |
| 目的 | 防止模型立刻再调 task | 帮助模型记住还有任务在跑 |

## 6. peek-job 定位

peek-job 的定位不变：**DIAGNOSTIC 工具，仅用户明确要求时调用。**

- Manifest 让模型「知道自己有 job 在跑」
- auto-reentry 让模型「知道 job 结果已经回来」
- peek-job 是排障工具，不是常规轮询工具
- Manifest 不取代 peek-job 的诊断价值（Manifest 只显示 job-id 和 label，不显示 step/lastTool）

## 7. 不做的事情

| 不做 | 原因 |
|------|------|
| 不分 phase / stage | 所有工具始终可用。分 phase 是另一种一刀切 |
| 不 block ask | ask 在任务中仍然需要（新情况/调整方向） |
| 不 block task | 并行派多个 task 是完全合法的 |
| 不修改系统 prompt | 保持 cache 前缀稳定 |
| 不引入新工具接口 | Manifest 是内部生成，不是工具 |
| 不修改 PostCallGuidance | 已有方案已统一文字 |

## 8. 影响范围

### 8.1 新增代码

| 位置 | 内容 |
|------|------|
| `internal/agent/agent.go` | `intentSummary` 字段 + `buildManifest()` 方法 |

### 8.2 修改代码

| 位置 | 改什么 |
|------|--------|
| `internal/agent/agent.go` | `stream()` 函数：在构建 `msgs` 快照时，将 manifest 追加到最末尾消息的 Content 尾部 |
| `internal/tool/builtin/ask.go` | `AskTool.Execute`：在完成交互后，利用结构化 responses 提取意图，并通过 `AgentFromContext(ctx)` 写入 `Agent.intentSummary` |
| `internal/agent/agent.go` | 新增一轮主动交互时重置并清空 `intentSummary` 的逻辑 |

### 8.3 不修改

- `internal/config/config.go` — AsyncBackgroundPolicy 不动
- `internal/tool/builtin/bgjobs.go` — peek-job description 不动
- `internal/jobs/jobs.go` — Manager 接口不动
- `internal/control/controller.go` — 不动
- `internal/boot/boot.go` — 不动
- `internal/agent/task.go` — PostCallGuidance 不动

## 9. 验证标准

| 测试场景 | 预期行为 |
|---------|---------|
| 启动新会话，不输任何 ask | Manifest 显示 `confirmed:` 空、`background:` 空 |
| 模型调 ask，用户回答 | Manifest 显示 `confirmed: <摘要>` |
| 模型调 task | Manifest 显示 `background: task-1 (label)` + `confirmed` 不变 |
| 多个并行 task | Manifest 列出所有运行中的 job |
| ask → task → 等待 → auto-reentry 返回 | Manifest 在 job 完成后不再显示该 job |
| 用户新输入 | 意图重置（如果上一轮已终结且无运行中任务，清空 intentSummary；否则跨轮保留） |

## 10. 部署方案

1. 实现 `buildManifest()` 和 `stream()` 注入
2. `executeOne` 中 ask 的 intent 提取
3. `go build ./... && go vet ./... && go test ./internal/agent/`
4. `make build`
5. 部署二进制，重启服务
6. 观察日志确认 manifest 正确注入

---

> 此文档为架构设计文档，非实现说明书。确认后进入实现阶段，每个改动一步一确认。
