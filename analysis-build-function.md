# Build 函数分析报告：为什么子代理无法自动重构

## 1. Build 函数到底有多少行？

```
起始行: 93   (func Build(ctx context.Context, opts Options) (*control.Controller, error) {)
结束行: 695  (})
总行数: 603 行  (695 - 93 + 1 = 603)
文件总行数: 695 行
```

Build 函数占据了整个文件的 **86.8%**（603/695），是典型的上帝函数（God Function）。

---

## 2. 函数内部的逻辑阶段划分

| 阶段 | 行区间 | 功能 | 可独立提取? | 依赖的局部变量 |
|------|--------|------|-------------|---------------|
| **P0** 初始化默认值 | 93-104 | 设置 stderr、root、cfgRoot 的默认值 | ✅ 可提取 | 无（仅读 opts） |
| **P1** 配置加载 | 105-130 | 加载 config、迁移旧配置、处理错误 | ✅ 可提取，但返回 cfg 和 error | 无 |
| **P2** Provider 模型刷新 | 131-170 | 可选刷新模型、OpenCode 定价抓取 | ⚠️ 需 cfg | cfg |
| **P3** 模型解析 | 170-180 | 解析 modelName → entry，EffortOverride | ⚠️ 需 cfg | cfg |
| **P4** 早期验证 | 180-200 | RequireKey / API key 检查 | ⚠️ 需 cfg、entry、modelName | cfg, entry, modelName |
| **P5** 网络层 | 200-240 | proxySpec、CA 证书、HTTP client、ProviderWithProxy | ⚠️ 需 cfg, entry | cfg, entry |
| **P6** 系统提示词 & Memory | 240-280 | ResolveSystemPrompt、memory.Load、skills | ⚠️ 需 cfg, root | cfg, root |
| **P7** 工具注册表 & 内置工具 | 280-310 | reg = NewRegistry(), addBuiltins, 代码补全 | ⚠️ 需 cfg, root, stderr, searchSpec 等 | cfg, root, stderr, 多个派生值 |
| **P8** 插件系统 | 310-440 | pluginHost、eager/lazy/bg 分档、注册工具 | ❌ 严重 | pluginHost, reg, ctx, cfg, sink, opts, root, 多个闭包 |
| **P9** 权限 & Hook | 440-465 | permission policy、hook runner | ⚠️ 需 cfg, root | cfg, root |
| **P10** 子代理 & 多智能体 | 465-510 | task tool、multi-agent、session/memory/ask 工具 | ❌ 严重 | reg, execProv, entry, proxySpec, cfg, mem, policy, 等大量变量 |
| **P11** Skill & 安装源 | 510-570 | skill tools、install_source、slash commands | ⚠️ 需 cfg, root, reg, skillStore, 等 | 大量 |
| **P12** 权限过滤 | 570-600 | mainAgentAllowed 和 toolsDynamic 映射 | ⚠️ 需 cfg | cfg |
| **P13** Executor 创建 | 600-620 | agent.NewSession、agent.New | ⚠️ 需 execProv, reg, sysPrompt, agentOpts, sink | execProv, reg, sysPrompt, agentOpts, sink |
| **P14** Slash 命令 | 620-660 | 加载 commands、构建 slashEntries | ⚠️ 需 skills, cmds, root | skills, cmds, root |
| **P15** Planner 模型 | 660-700 | 双模型 Coordinator | ❌ 严重 | 几乎所有变量：pe, pm, proxySpec, mem, reg, executor, agentOpts, 等 |
| **P16** 最终组装 | 700-695 | control.Options 结构体赋值 & return | ✅ 可提取（参数很多） | 所有变量 |

**结论：只有 P0、P1、P16 三个阶段相对独立；其余阶段都共享大量状态。**

---

## 3. 提取辅助函数的难点

### 3.1 大量共享局部变量（至少 30+ 个）

在 603 行中，以下变量**贯穿**整个函数并在多处被读写：

```
stderr, root, cfgRoot, cfg, modelName, entry, sink,
proxySpec, caPool, trOpts, balanceClient, execProv,
sysPrompt, mem, projectChecks, skillStore, skills,
allSkillStore, allSkills, reg, searchSpec, bashTimeout,
pluginHost, eagerSpecs, lazySpecs, bgSpecs, cleanup,
policy, hooksTrusted, hookRunner, maxSteps,
subAgentGate, maCtrl, taskTool, agentOpts,
mainAgentAllowed, toolsDynamic, executor, execSess,
label, runner, ctrlOpts
```

任何阶段提取都需要传递 **5~15 个参数**，产生"参数传递灾难"（pass-through parameter hell）。

### 3.2 复杂的错误处理链（8 个早期返回点）

```
Line  25: return nil, err              (config load failure)
Line  78: return nil, ErrUnknownModel   (model not found)
Line  88: return nil, err              (validation failure)
Line 113: return nil, err              (network proxy validation)
Line 118: return nil, err              (CA cert load failure)
Line 125: return nil, err              (HTTP client creation)
Line 130: return nil, err              (provider creation)
Line 135: return nil, err              (system prompt resolution)
Line 547: return nil, err              (planner model not found)
Line 552: return nil, err              (planner provider creation)
```

这些 return 分布在不同阶段，但后面的阶段**假设前面都成功**。提取一个阶段为函数时，其返回值必须是 `(result, error)`，然后调用处必须检查 error。

### 3.3 闭包捕获外层作用域

函数内部定义了 **4 个闭包**，它们捕获了外层的大量局部变量：

```go
// (1) registerDeferred (line 264) — 捕获 pluginHost, ctx, reg, sink
registerDeferred := func(specs []plugin.Spec, kick bool) { ... }

// (2) resolveSubagentProviderForTask (line 324) — 捕获 cfg, proxySpec
resolveSubagentProviderForTask := func(role, modelRef, effort string) (...) { ... }

// (3) extractSharedSections (line 362) — 捕获无（纯函数）
extractSharedSections := func() string { ... }

// (4) disconnect (line 490) / OnDisconnect 等 — 在 install_source 注册中捕获 pluginHost
```

提取闭包所在的阶段后，这些闭包要么变成包级函数（失去捕获能力），要么必须改为显式传参。

### 3.4 变量被重新赋值 / 非局部修改

```go
// pluginHost 在 if 块内被重新赋值（line 245）
pluginHost := plugin.NewHost()           // line 201 声明
...
if len(eagerSpecs) > 0 {
    host, ptools := plugin.StartAvailable(ctx, eagerSpecs)
    pluginHost = host                     // ← 重新赋值！
}

// cleanup 被多次组合（defer chain）
cleanup := pluginHost.Close              // line 275
...
if cfg.LSP.Enabled {
    ...
    prev := cleanup
    cleanup = func() { prev(); lspMgr.Close() }  // line 286
}

// reg 在 10+ 个位置被增量添加工具
reg.Add(t)   // line 248, 249, 256, 267, 284, 394, 396, 405, 406, 410, 411, 412, 418, 465, 466, 467, 535, 536
```

这些 **副作用式状态累积** 导致你无法简单地将一段代码提取出去，因为提取后副作用落在函数内部无法作用到外层变量。

### 3.5 对外部包的类型依赖

Build 函数依赖 **20 个内部包 + 12 个标准库包**：

```
标准库: context, errors, fmt, io, log/slog, os, os/exec, path/filepath, strings, sync, time
内部包: agent, command, config, control, event, hook, installsource, instruction,
        lsp, memory, multiagent, netclient, permission, plugin, provider, shell,
        skill, tool, tool/builtin, tool/sessiontool
```

这些包定义了 Build 使用的大量类型（`config.Config`, `plugin.Host`, `tool.Registry`, `agent.Options` 等），子代理的编辑工具需要在海量源文件中定位这些类型定义——而它只有 `edit_file` 这类文本替换工具，没有 AST 级别的重构能力。

---

## 4. 为什么子代理会失败？

### 4.1 工具调用层面的限制（致命原因）

| 限制 | 具体表现 |
|------|---------|
| **edit_file 要求唯一匹配** | `edit_file` 的 `old_string` 必须在文件中精确匹配一次。但在 603 行的函数中，几乎每行都可能出现在多处（如 `reg.Add(t)` 出现 20+ 次）。子代理无法找到唯一锚点来安全替换。 |
| **无 AST 级别重构** | 没有 `extract_function`、`rename_variable`、`introduce_parameter` 这类 IDE 级别的重构工具。所有编辑都靠文本模式匹配。 |
| **无跨文件编辑能力** | 如果提取的函数需要放到新文件或需要修改多个文件的 import，子代理虽然可以依次编辑，但无法保证中间编译状态的一致性。 |
| **上下文窗口有限** | 603 行的函数超过了单次 LLM 调用的可靠处理范围。子代理即使能读全文件，也无法在 token 预算内保持对所有变量和分支的精确跟踪。 |

### 4.2 代码复杂度层面的障碍

| 障碍 | 描述 |
|------|------|
| **30+ 共享变量** | 提取任何一段都需要传递大量参数，子代理无法自动推断哪些变量需要保留为参数、哪些可以内联。 |
| **闭包捕获** | 内部闭包引用外层变量，机械提取后闭包需要重构为显式参数——子代理无法识别这种"捕获关系"。 |
| **变量重新赋值** | `pluginHost` 和 `cleanup` 被重新赋值，提取后需要改为返回值——子代理不会发现这种模式。 |
| **副作用累积** | `reg.Add(t)` 分散在 10+ 处，提取后必须将 `reg` 传出再传回——子代理难以识别这些"累积点"。 |

### 4.3 类型系统约束

Go 的类型系统要求提取的函数签名必须精确匹配每个参数和返回值的类型。如果子代理误判了某个变量的类型（例如把 `*plugin.Host` 写成 `plugin.Host`），生成的代码就无法编译。没有编译运行验证，子代理无法自行纠正。

---

## 5. 人工重构的可行方案

### 方案 A：逐段提取 + 引入 Builder 模式（推荐）

引入 `BuildContext` 或 `Builder` 结构体，将分散的状态收集到一个对象中，各阶段作为方法：

```go
type controllerBuilder struct {
    ctx     context.Context
    opts    Options
    stderr  io.Writer
    root    string
    cfgRoot string
    cfg     *config.Config
    modelName string
    entry      *config.ModelEntry
    sink       *event.SyncSink
    // ... 所有其他字段
}
```

然后每个阶段变成 builder 的一个方法：

```go
func (b *controllerBuilder) loadConfig() error { ... }
func (b *controllerBuilder) resolveModel() error { ... }
func (b *controllerBuilder) setupNetwork() error { ... }
func (b *controllerBuilder) buildRegistry() error { ... }
func (b *controllerBuilder) setupPlugins() error { ... }
func (b *controllerBuilder) buildController() (*control.Controller, error) { ... }
```

**优点**：消除参数传递灾难；方法间共享 `b.cfg`、`b.reg` 等无需显式传参；闭包可以引用 `b` 的字段。
**缺点**：需要一次性重构整个函数，工程量大。

### 方案 B：顶部提取 + 结构体返回值

从函数顶部开始，每次提取一个独立阶段（如 P0→P1→P5），每个阶段返回一个包含所需状态的结构体，下一阶段接收该结构体：

```go
type configResult struct {
    cfg        *config.Config
    modelName  string
    entry      *config.ModelEntry
    // ...
}
```

**优点**：逐步进行，每次改动范围小，可编译验证。
**缺点**：最终会产生大量"中间结构体"，整体性不如方案 A。

### 方案 C：使用 IDE 的 Extract Function 重构

在 GoLand / VS Code + gopls 中，对选中代码段执行 **Extract Function**（Refactor → Extract → Function）。IDE 会自动：
- 检测哪些变量是输入（参数）、哪些是输出（返回值）
- 处理变量重命名冲突
- 生成正确的函数签名

然后人工微调参数列表和错误处理。

**建议**：先用方案 A 设计 Builder 结构，再用 IDE 的 Extract Method 逐个将段落移到 Builder 的方法中，最后将 Build 函数缩减为方法调用序列。

---

## 总结

Build 函数（603 行）无法被子代理自动重构的根本原因是 **三重瓶颈叠加**：

1. **工具瓶颈**：子代理只有文本替换工具（`edit_file`），没有 AST 重构工具，无法在 600+ 行中找到唯一匹配锚点，也无法感知变量捕获、重新赋值、类型推断等语义信息。
2. **代码复杂度**：30+ 共享变量、4 个闭包、变量重新赋值、8 个早期返回、10+ 处副作用累积——这些模式对 AST 工具来说是简单的，但对文本替换工具来说是不可能的。
3. **语言约束**：Go 的静态类型系统要求提取的函数签名精确无误，任何参数类型错误都导致编译失败，而子代理无法运行编译来验证。

**人工重构的推荐方案**：引入 `controllerBuilder` 结构体，将 Build 函数的各阶段映射为 Builder 的方法，最终 Build 成为一系列方法调用的组合。配合 IDE 的 Extract Method 功能，可在 1-2 小时内安全完成。
