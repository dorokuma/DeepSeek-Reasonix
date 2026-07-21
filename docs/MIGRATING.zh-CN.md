# 迁移到 Reasonix 1.0（Go 重写版）

<a href="./MIGRATING.md">English</a>

Reasonix 1.0 是一次从零开始的 **Go 重写**。它使用全新的代码库，并不是 `0.x` TypeScript 版本的增量升级。本文说明两个版本的差异以及迁移方法。

## 摘要

| | 旧版（v1） | Reasonix 1.0+（v2） |
| --- | --- | --- |
| 语言 | TypeScript / Node.js | Go |
| 分支 | [`v1`](https://github.com/esengine/DeepSeek-Reasonix/tree/v1)（仅维护） | `main-v2`（默认、活跃开发） |
| 版本 | `0.x`（最高 v0.54.x） | `1.0.0`+ |
| 安装 | `npm i -g reasonix@0.53.2`（固定到某个 `0.x` 版本） | `npm i -g reasonix`；也可使用 release 归档或源码构建 |
| 代码智能 | embedding 语义搜索 + tree-sitter 符号索引 | LSP 辅助代码读取，以及 grep/read_file/glob；语义索引尚未移植 |

“v1”和“v2”表示代码库代际，而不是 semver 主版本：v1 从未发布 1.0，因此 Go 重写版使用 `1.x` 版本号。

## 安装 1.0

`npm` 仍是主要安装渠道。npm 包会下载预编译的 Go 二进制文件，方式与 esbuild/biome 类似；二进制本身是独立的 Go 可执行文件，npm 不是运行时依赖。

**`npm i -g reasonix` 会安装当前稳定的 `1.x` 版本。** npm 的 `latest` 标签已从 `1.17.5` 起切换到 Go 版本。候选版本继续使用 `next` 标签；旧版 `0.x` 仍可通过固定版本安装：

```sh
npm i -g reasonix          # 当前稳定的 1.x
npm i -g reasonix@next     # 候选版本（当其领先于稳定版时）
npm i -g reasonix@0.53.2   # 固定到旧版 TypeScript 构建
```

每个 GitHub release 都附带预编译归档（`reasonix-<os>-<arch>.tar.gz` / `.zip`）和桌面安装包。它们与 npm 是不同的安装渠道：桌面安装包不会改动通过 `npm i -g` 安装的 CLI，因此 shell 中的 npm `0.53` 与 `1.x` 桌面应用可以共存，并不冲突。

也可以从源码构建：

```sh
git clone https://github.com/esengine/DeepSeek-Reasonix   # 默认分支 main-v2（Go）
cd DeepSeek-Reasonix && make build                        # -> bin/reasonix(.exe)
```

## 配置

| 旧版 | Reasonix 1.0 |
| --- | --- |
| TypeScript 配置文件 | 项目使用 `reasonix.toml`；从 v1.8.1 起，全局配置为 Reasonix home 下的 `config.toml`（macOS/Linux：`~/.reasonix/`；Windows：`%AppData%\reasonix\`）。参见 `reasonix.example.toml` 和[配置路径](./CONFIG_PATHS.zh-CN.md) |
| 环境变量 / API key | provider 配置保留 `api_key_env`；保存的 key 位于 Reasonix home 的 `.env`（`DEEPSEEK_API_KEY`、`MIMO_API_KEY` 等） |
| 项目记忆 | `REASONIX.md`（含自动记忆），兼容 Claude Code |
| MCP server | 在 `reasonix.toml` 中使用 `[[plugins]]`，或直接读取 Claude Code 的 `.mcp.json` |

首次启动时，v1.8.1+ 会执行一次非破坏性导入。它会读取以下旧配置：

- `~/Library/Application Support/reasonix/config.toml`
- `~/.config/reasonix/config.toml`
- `~/.reasonix/reasonix.toml`
- v0.x 的 `~/.reasonix/config.json`

导入内容包括 API key、base URL、语言和 MCP server；缺失的旧凭据会迁移到 `<Reasonix home>/.env`，旧会话也会从历史目录导入。原文件不会被修改，Reasonix 会在导入后显示启动提示。

会话会根据 v0.x sidecar 元数据回到原工作区，并沿用旧摘要作为标题；工作区已不存在的会话会进入全局会话目录。可通过 `--resume` 或历史面板恢复这些会话。自动配置导入仅在尚未存在 v1.8.1+ 配置时运行；若新配置已经生成，请手动补入缺失值。

如果首次启动时旧路径尚不可用，可在交互式会话中运行 `/migrate`。若看到 `unknown command`，请先升级到包含该命令的 Go 版本。该命令会扫描旧配置、凭据、记忆和会话，并仅导入尚未导入的内容；它不会覆盖已有 `config.toml` 或记忆文件，也不会绕过会话导入标记。

若旧 v0.x 会话位于自定义 Windows 目录，可指定来源：

```text
/migrate --from "D:\OldReasonix"
```

完整路径和限制见[配置路径](./CONFIG_PATHS.zh-CN.md)。

## 保持不变的部分

agent 核心延续了原有能力：循环、读写编辑与 glob/grep/bash 等工具、子智能体（`task`、explore/research/review）、Skill、Hook、Plan 模式、MCP 客户端，以及针对 DeepSeek 前缀缓存的设计。

## 主要变化

- **代码智能**：Go 重写版通过 LSP 辅助代码读取，并结合 `grep`、`read_file` 和 `glob` 理解本地代码。v1 的语义搜索与 tree-sitter 符号索引尚未移植，CodeGraph 也不再以内置 MCP server 形式提供。
- **Plan 模式**：新增 `complete_step`，用于基于证据确认步骤完成。
- **MCP 身份与 schema 缓存 URL 感知凭据**：userinfo 和 token/api_key/password 等查询值不会进入本机身份或缓存指纹，因此轮换凭据不会使项目授权失效。旧工作区凭据可一次性迁移为启动授权，旧工具快照不会沿用。
- **MCP 添加后即可使用**：用户通过桌面端、全局配置、旧配置导入或已验证插件包添加的 server 会立即连接；未显式配置 MCP 审批策略时默认允许调用。仓库内 `reasonix.toml` / `.mcp.json` 声明的 server 则必须先针对稳定身份确认一次，确认前不会启动进程或发起网络请求。
- **stdio MCP 连接持久化**：writer 调用不再创建新进程，浏览器或会话类 server 的状态可以保留。
- **Plan 与权限策略相互独立**：普通内置工具和 Bash 仍遵循 Ask/Auto/YOLO 与 Sandbox；已安装或代理解析的 MCP 写入/破坏性工具及不受信任的读取工具在整个规划阶段保持阻止。`complete_step` 等执行阶段工具也要等计划获批后才能使用。
- `[agent].plan_mode_allowed_tools` 与 `plan_mode_read_only_commands` 仍可解析和保存，以兼容旧配置，但不再决定主 Plan 流程能否调用工具。需要可信读取能力时，应在 `trusted_read_only_tools` 中声明已审计的原始 MCP 工具名。
- 使用 `read_only_task` / `read_only_skill` 创建技术上只读的子智能体；普通 `task` / `run_skill` 仍可写入，并受权限与 Sandbox 控制。第三方 MCP 的 `readOnlyHint` 只影响常规权限和调度，不会自动获得专用 planner 或只读子智能体的信任。
- MCP 可通过 `default_tools_approval_mode`、`tools.<raw>.approval_mode` 和 `approvals_reviewer` 覆盖默认审批策略。标记 `destructiveHint: true` 的调用在 `auto`、`prompt` 或 `writes` 下每次都需要人工重新批准；有效的 `approve` 模式则直接允许。
- **Web Dashboard 仍然可用，桌面端更推荐**：需要浏览器访问时，可运行
  `reasonix serve` 启动本地 Web UI；日常可视化使用优先选择 Wails 桌面端，
  终端工作流继续使用 CLI/TUI。
- 一些细粒度 v1 工具被合并，例如文件管理操作改由 `bash` 完成；少数工具尚未移植，进度在 Discussions 中跟踪。

## 文件编码

Reasonix 1.0 支持读取和编辑 UTF-8、UTF-8 BOM、UTF-16 LE/BE 与 GB18030（GBK 的超集），与 v1 行为一致。

- `read_file` 会把受支持编码解码为 UTF-8 后提供给模型。
- `edit_file` 和 `multi_edit` 会保留文件原编码；编辑 GB18030 文件后仍以 GB18030 保存。
- `write_file` 始终写入 UTF-8。
- `grep` 会在匹配前解码，因此正则表达式可用于非 UTF-8 文件。

## 报告问题

Issue 和 PR 按代码线标记：**`v1`** 表示旧 TypeScript 版，**`v2`** 表示 Go 版。请按实际使用版本提交报告。旧 `v1` 线处于维护模式，只接收 bug 修复，不再新增功能。

如有问题，请发起 [Discussion](https://github.com/esengine/DeepSeek-Reasonix/discussions)。
