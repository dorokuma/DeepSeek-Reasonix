# 关键词触发引擎 — 设计规格

> 状态：设计完成 · 待实现
> 日期：2025-07-17

## 1. 问题背景

Reasonix 的技能触发（Ship/Spark 等）完全依赖系统提示词 §3.2 让模型自行判断信号词。DeepSeek v4 pro 等遵从性较弱的模型经常忽略提示词规则，导致用户说"部署"后 ship 技能触发率接近 0%。需要将触发机制从"纯提示词驱动"改为"代码层硬拦截 + 用户确认"的架构。

## 2. 核心设计

### 2.1 架构概览

```
用户输入
  │
  ├── ① TriggerEngine.PreCheck() — 代码层关键词匹配
  │     ├── 命中 → 弹出确认（TUI [Y/n] / Telegram InlineKeyboard）
  │     │     ├── 确认 → TriggerSkill() 展开技能 body 注入模型输入
  │     │     └── 取消 → 注入 <trigger-denied> 标记 + 原始消息
  │     └── 未命中 → 原样透传到模型（提示词 §3.2 保留为后备）
  │
  └── ② 模型收到消息 → 正常处理（含技能 body 或原始消息）
```

**关键原则**：确认环节对模型完全透明。用户确认后模型收到的是已展开的技能指令，不需要"判断要不要触发"。用户取消后模型收到的是带 `<trigger-denied>` 标记的消息，提示词规则强制跳过触发判断。

### 2.2 数据流（命中 + 确认）

```
用户: "部署到生产"
  → POST /submit
  → Controller.PreCheck()
    → TriggerEngine.Check("部署到生产")
      → 扫描所有 skills/*/SKILL.md frontmatter
      → 命中 ship.triggers 中的 "部署"
      → 排除规则检查通过（无 "不要""怎么""昨天" 等抑制词）
    → 返回 TriggerResult{SkillName:"ship", MatchedWord:"部署", ...}
  → SSE 推送 trigger_notice 事件（turn 挂起，Agent 未启动）
    {
      kind: "trigger_notice",
      trigger: { id, skill, matched, summary }
    }
  → 桥接器渲染 InlineKeyboard:
    🚀 技能触发确认
    检测到信号词：**部署**
    匹配技能：ship
    📋 完整 CI 流程 → ...
    [✅ 确认触发] [❌ 取消]
  → 用户点击确认
  → POST /confirm {id, action:"confirm"}
  → Controller.ConfirmTrigger("ship", originalInput)
  → 展开技能 body → 注入为模型输入 → Agent.Run()
```

### 2.3 数据流（deny + 防二次触发）

```
用户: "部署到生产"
  → 代码层命中 → 弹出确认
  → 用户点 ❌ 取消
  → POST /confirm {id, action:"deny"}
  → Controller.ConfirmTrigger() 不返回原始消息
  → 返回被包裹的消息:
    <trigger-denied skill="ship" matched="部署">
    用户已拒绝上述技能触发。本条消息请按普通对话处理。
    </trigger-denied>
    
    部署到生产

  → 模型收到带 <trigger-denied> 标记的消息
  → 系统提示词 §3.2 规则: "如果消息包含 <trigger-denied> 标记，跳过一切 Spark/Ship 触发判断"
  → 模型不会二次触发
```

**代码层 / 提示词双保险**：
- 代码层：deny 时注入 `<trigger-denied>` 专用标记（在用户消息体内，模型注意力天然高）
- 提示词：§3.2 增加兜底规则 — 检测到标记则强制跳过触发判断

## 3. 组件设计

### 3.1 新增包：`internal/trigger/`

```
internal/trigger/
├── engine.go          # 引擎主逻辑：Check(), Reload()
├── engine_test.go     # 单元测试
├── matcher.go         # 关键词正则匹配 + 排除规则
├── loader.go          # 扫描 skills/ 目录，解析 SKILL.md frontmatter
└── types.go           # 类型定义
```

### 3.2 类型定义 (`types.go`)

```go
// TriggerRule 单个技能的触发规则
type TriggerRule struct {
    SkillName string   // "ship", "spark"
    SkillPath string   // SKILL.md 文件路径（用于 mtime 检测）
    Keywords  []string // ["部署", "上线", "ship it", ...]
    ExcludeIf []string // ["不要", "先别", "昨天", "上次", "如何", "怎么"]
    Summary   string   // 技能一行描述（取技能索引中的描述）
    MTime     time.Time // 文件修改时间（热重载用）
}

// TriggerResult 匹配结果
type TriggerResult struct {
    Triggered   bool
    SkillName   string
    MatchedWord string
    Summary     string
}

// TriggerEvent SSE 事件 payload
type TriggerEvent struct {
    ID          string `json:"id"`
    Skill       string `json:"skill"`
    Matched     string `json:"matched"`
    Summary     string `json:"summary"`
}
```

### 3.3 引擎入口 (`engine.go`)

```go
type Engine struct {
    mu      sync.RWMutex
    rules   map[string]*TriggerRule // key: skill name
    index   map[string]string        // keyword → skill name（快速查找）
}

// Check 检查用户输入是否命中任何技能触发词
func (e *Engine) Check(input string) *TriggerResult
// 1. 遍历 index 检查关键词是否出现在 input 中
// 2. 命中后检查排除规则（否定/疑问/过去时态）
// 3. 返回第一个命中且未被排除的结果
// 注意：多命中时不在此处决策，返回所有命中结果供上层渲染多选确认

// Reload 热重载：检查所有已缓存文件的 mtime，变化时重新解析
func (e *Engine) Reload() error

// Load 全量加载：进程启动时调用，扫描 skills/ 目录
func (e *Engine) Load(skillsDir string) error
```

### 3.4 关键词匹配器 (`matcher.go`)

```go
// matchKeyword 检查 input 中是否包含关键词（词边界匹配）
func matchKeyword(input string, keyword string) bool

// checkExclude 检查 input 是否命中排除规则（否定/疑问/过去时态）
// 核心逻辑：关键词前 N 个字符内出现排除词 → 抑制触发
func checkExclude(input string, keyword string, excludeWords []string) bool
```

**排除规则示例**：

| 输入 | 关键词 | 排除词命中 | 结果 |
|------|--------|-----------|------|
| "部署到生产环境" | 部署 | 无 | 触发 |
| "怎么部署这个" | 部署 | "怎么"在部署前 | 抑制 |
| "昨天我们部署了" | 部署 | "昨天"在句中 | 抑制 |
| "先不要部署" | 部署 | "不要"在部署前 | 抑制 |
| "帮我看下部署文档" | 部署 | "看下"不影响 | 触发 |

### 3.5 加载器 (`loader.go`)

```go
// loadFromSkillFile 解析单个 SKILL.md 的 frontmatter
// 支持 YAML frontmatter（--- 包裹）
func loadFromSkillFile(path string) (*TriggerRule, error)

// scanSkillsDir 扫描 skills/ 目录下所有 SKILL.md
func scanSkillsDir(dir string) ([]*TriggerRule, error)
```

**Frontmatter 格式规范**：

```markdown
---
triggers:
  - 部署
  - 上线
  - 发布
  - ship it
  - ship
  - 推送上线
  - 打版本
  - 跑 CI
  - 走 CI 流程
  - 完整 CI
exclude_if:
  - 不要
  - 先别
  - 昨天
  - 上次
  - 之前
  - 如何
  - 怎么
  - 教程
---

# Ship — 完整 CI 流程
...
```

- `triggers`：必填。触发关键词列表。
- `exclude_if`：可选。抑制触发词列表。不填则使用全局默认排除词（"不要""怎么""如何""昨天""上次""先别"）。
- 引擎仅解析 `---` 包裹的 frontmatter；正文内容不解析。
- 无 frontmatter 的 SKILL.md → 引擎忽略该技能的关键词触发（仍可通过斜杠命令 / 提示词触发）。

## 4. 确认机制

### 4.1 TUI 模式

```
检测到信号词「部署」→ 触发 ship 技能？
[Y] 确认触发  [N] 取消（普通对话）
```

直接利用现有 TUI 的确认机制，与 approval 确认共用手势。

### 4.2 Telegram 模式 (InlineKeyboard)

**确认消息模板**：

```
🚀 技能触发确认

检测到信号词：部署
匹配技能：ship

📋 完整 CI 流程
检测项目 → 测试循环到绿 → commit + push → 编译安装

[✅ 确认触发] [❌ 取消]
```

**回调格式**：`tg:{triggerID}:{action}`（新增 `prefixTrigger = "tg:"` 前缀）

- 确认 → `POST /confirm {"id": triggerID, "action": "confirm"}`
- 取消 → `POST /confirm {"id": triggerID, "action": "deny"}`

**按钮点击后**：
- 原始确认消息的 InlineKeyboard 移除
- 确认 → 显示"🚀 技能已触发 — 正在执行..."，跟进模型输出
- 取消 → 显示"已取消 — 消息作为普通对话处理"

### 4.3 多技能命中

当一条消息同时命中多个技能的关键词时，弹出多选确认：

```
🎯 检测到多个技能匹配

请选择要触发的技能：

[🚀 ship — 完整CI流程]  [💡 spark — 头脑风暴与设计]
[❌ 取消 — 普通对话]
```

- 用户点击任一技能 → 仅触发该技能
- 所有按钮自适应排列（每行 1-2 按钮，根据技能数量自动调整；取消按钮独立一行）

**无权重机制**：选择权完全交给用户，不做自动优先级排序。

### 4.4 确认超时

- 确认等待设为 60 秒
- 超时 → 自动视为取消（deny），消息原样透传给模型
- 超时后推送一条系统消息："⏰ 技能触发确认超时，已自动取消"

## 5. 防二次触发机制

### 5.1 `<trigger-denied>` 标记格式

```xml
<trigger-denied skill="ship" matched="部署">
用户已拒绝上述技能触发。本条消息请按普通对话处理。
</trigger-denied>
```

### 5.2 系统提示词变更（§3.2）

在 Spark/Ship 触发判断前各增加一行前置检查：

```markdown
## Spark 触发判断（阶段 0 用户确认后立即执行，跳过即 L1 阻断）

**前置检查**：若用户消息包含 `<trigger-denied` 标记，跳过一切触发判断，直接进入阶段 1。

## Ship 触发判断（Spark 不触发后执行，跳过即 L0 即死）

**前置检查**：若用户消息包含 `<trigger-denied` 标记，跳过一切触发判断，直接进入阶段 2。
```

## 6. 热重载机制

### 6.1 设计

每次 `PreCheck()` 调用时，引擎对比缓存中各 SKILL.md 的 mtime：

```
PreCheck(input):
  dirty = false
  for each rule in cached_rules:
    stat = os.Stat(rule.SkillPath)
    if stat.ModTime != rule.MTime:
      dirty = true
  if dirty:
    Engine.Reload()  // 重新扫描 skills/ 目录
  return Engine.Check(input)
```

### 6.2 触发时机

- 进程启动 → 首次全量扫描
- 每次用户输入 → mtime 对比，变化时热重载
- `install_skill` 工具调用后 → 主动通知引擎注册（补充路径，供将来使用）

### 6.3 性能

- 规模：10-20 个 SKILL.md 文件
- 每次 PreCheck 的 stat 开销：< 1ms，可忽略
- 实际重载频率：仅在文件新增/修改/删除时触发

## 7. 改动清单

### 7.1 Reasonix 核心

| 文件 | 改动类型 | 改动说明 |
|------|---------|---------|
| `internal/trigger/engine.go` | **新增** | 引擎主逻辑 |
| `internal/trigger/loader.go` | **新增** | Frontmatter 解析 + 目录扫描 |
| `internal/trigger/matcher.go` | **新增** | 关键词匹配 + 排除规则 |
| `internal/trigger/types.go` | **新增** | 类型定义 |
| `internal/trigger/engine_test.go` | **新增** | 单元测试 |
| `internal/control/controller.go` | 修改 | 新增 `PreCheck()` `ConfirmTrigger()` 方法；初始化 TriggerEngine |
| `internal/serve/wire.go` | 修改 | `wireEvent` 新增 `Trigger *TriggerEvent` 字段 |
| `internal/serve/serve.go` | 修改 | `/submit` 接入 PreCheck；新增 `/confirm` 端点 |
| `prompts/reasonix-system.md` | 修改 | §3.2 增加 `<trigger-denied>` 前置检查规则 |

### 7.2 技能文件

| 文件 | 改动 |
|------|------|
| `skills/ship/SKILL.md` | 新增 frontmatter（triggers + exclude_if） |
| `skills/spark/SKILL.md` | 新增 frontmatter（triggers + exclude_if） |

### 7.3 Reasonix-Telegram 桥接器

| 文件 | 改动 |
|------|------|
| `main.go` | 新增 `prefixTrigger = "tg:"` 回调前缀常量 |
| `serve_backend.go` | SSE 事件循环新增 `trigger_notice` case |
| `stream.go` | 新增 `onTriggerNotice()` — 构建确认消息 + InlineKeyboard；多命中时渲染多按钮 |
| `callback.go` | 新增触发确认回调处理（`handleTriggerCallback`） |

## 8. 测试用例

### 8.1 单元测试（matcher_test.go）

| # | 输入 | 预期 |
|---|------|------|
| 1 | "部署到生产" | 触发 ship，matched="部署" |
| 2 | "昨天部署了" | 不触发（"昨天"排除） |
| 3 | "怎么部署" | 不触发（"怎么"排除） |
| 4 | "不要部署" | 不触发（"不要"排除） |
| 5 | "帮我部署一下" | 触发 |
| 6 | "准备上线新版本" | 触发 |
| 7 | "设计方案讨论" | 触发 spark |
| 8 | "部署新的设计方案" | 多命中：ship + spark |
| 9 | "你的部署脚本在哪" | 触发（"在哪"不在排除词中） |
| 10 | "先不要上线" | 不触发 |

### 8.2 集成测试

- 无 frontmatter 的技能文件 → 引擎忽略
- 新技能文件创建 → 下次 PreCheck 自动热重载
- 技能文件删除 → 下次 PreCheck 自动清除
- /ship 斜杠命令仍然有效（TUI 层截获，不经过引擎）
- 用户点击确认 → 技能 body 正确展开并注入
- 用户点击取消 → `<trigger-denied>` 标记正确注入

## 9. 不变性保证

- **斜杠命令不受影响**：`/ship` 由 TUI 层截获，不经过 TriggerEngine
- **提示词后备机制保留**：代码层未命中的消息，§3.2 规则仍可生效
- **Approval / Ask 确认机制不受影响**：TriggerEngine 是独立模块，与其他确认事件并行不冲突
- **零 LLM 依赖**：引擎纯代码驱动（YAML frontmatter 解析 + 正则匹配）
