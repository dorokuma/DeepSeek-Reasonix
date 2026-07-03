# Reasonix 插件包

Reasonix 插件包把 skills、hooks 和 MCP servers 组织成一个可安装单元。

## CLI 模式

在终端里使用 `reasonix plugin` 安装和管理插件包。插件包当前按全局范围安装，
写入 Reasonix home 目录。

### 通过 CLI 安装

`install` 接收一个来源：

- GitHub 仓库，例如 `git:github.com/obra/superpowers` 或
  `https://github.com/obra/superpowers`。
- GitHub 分支或子目录 URL，例如
  `https://github.com/owner/repo/tree/main/path/to/plugin`。
- 本地目录，目录内需要包含 `reasonix-plugin.json` 或
  `.codex-plugin/plugin.json`。

只预览安装计划，不写文件：

```bash
reasonix plugin install git:github.com/obra/superpowers --dry-run
```

确认计划后安装：

```bash
reasonix plugin install git:github.com/obra/superpowers --yes
```

指定安装名称，或覆盖已安装的同名插件：

```bash
reasonix plugin install git:github.com/obra/superpowers --name superpowers --replace --yes
```

以开发模式使用本地目录：

```bash
reasonix plugin install /path/to/plugin --link --replace --yes
```

CLI 安装参数：

- `--dry-run` 只规划和校验安装，不写文件。
- `--yes` 用于确认执行会写文件的安装。
- `--replace` 允许当前来源替换已安装的同名插件。
- `--name <name>` 或 `--name=<name>` 覆盖插件 manifest 里的名称，
  作为本次安装名称。
- `--link` 链接本地插件目录，而不是复制到 Reasonix 的插件存储目录。
  移动或删除该目录会导致这个链接插件失效。

如果运行 `reasonix plugin install <source>` 时既没有 `--dry-run`，
也没有 `--yes`，CLI 会拒绝写文件，并提示使用其中一个参数重新运行。
安装和移除命令会输出结构化 JSON，来源于桌面端同一套 install-source 后端。

插件状态和内容写入：

```text
~/.reasonix/plugin-packages.json
~/.reasonix/plugins/<name>/
```

### 通过 CLI 管理

列出已安装插件：

```bash
reasonix plugin list
```

查看某个插件的元数据、根目录、来源以及导出的能力数量：

```bash
reasonix plugin show superpowers
```

检查 manifest 和 skill roots 是否可读：

```bash
reasonix plugin doctor superpowers
```

在不卸载的情况下启用或禁用插件：

```bash
reasonix plugin disable superpowers
reasonix plugin enable superpowers
```

移除插件：

```bash
reasonix plugin remove superpowers --yes
```

`remove` 也可以写成 `uninstall`。它需要 `--yes`，
因为会写入状态并删除复制安装的插件内容。如果是链接模式安装的本地插件，
外部源目录会保留。

## 桌面端设置

打开 **设置 -> 插件**，可以不用 CLI 直接安装和管理插件包。

### 安装插件

安装区有两种模式：

- **本地目录**：点击 **选择插件目录**，从磁盘选择一个插件目录。
  选中路径会显示在按钮右侧。
- **Git 仓库**：填写 Git 来源，例如 `git:github.com/obra/superpowers`。
  **安装名称（可选）** 可覆盖插件 manifest 声明的名称，用于本次安装或覆盖。

选择来源和选项后，再使用操作按钮：

- **预检** 校验来源并展示计划安装动作，不写入文件。
- **安装插件** 按当前来源和选项执行安装。
- **刷新插件** 从磁盘和配置重新读取已安装插件列表。

安装选项：

- **覆盖同名插件** 允许当前来源替换已安装的同名插件。关闭时，同名安装会失败，
  而不是覆盖已有内容。
- **开发模式：链接源目录** 只在 **本地目录** 模式出现。它不会复制插件，
  而是直接链接所选目录；适合开发或调试插件。移动或删除该目录会导致这个链接插件失效。

对新的 Git 来源或本地插件目录，建议先点 **预检**。

### 管理已安装插件

已安装插件列表会展示每个插件包以及它导出的 skills、hooks 和 MCP servers。
通过应用外编辑插件文件或配置后，可点 **刷新插件** 重新读取。

展开插件行后可以：

- 启用或禁用插件。
- 使用 **更新** 拉取或刷新具备更新来源的插件。
- 使用 **诊断** 检查插件 manifest，并查看警告或诊断信息。
- 使用 **移除插件**，确认后卸载该插件包。

## 原生 Manifest

Reasonix 原生插件在根目录声明 `reasonix-plugin.json`：

```json
{
  "name": "example",
  "version": "1.0.0",
  "description": "Example plugin",
  "skills": "skills",
  "hooks": {
    "SessionStart": [
      {
        "command": "hooks/session-start",
        "description": "Load startup context"
      }
    ]
  },
  "mcpServers": {
    "helper": {
      "command": "bin/helper"
    }
  }
}
```

相对路径都按插件根目录解析。Reasonix 安装插件时不会执行第三方安装脚本。

## Codex 兼容

Reasonix 也会读取 `.codex-plugin/plugin.json`。对于 Superpowers 这类插件，
Reasonix 会映射：

- `skills` 到 Reasonix skill root。
- 如果存在 `hooks/session-start-codex`，映射为 Reasonix `SessionStart` hook。

插件 hook 会收到这些环境变量：

- `REASONIX_PLUGIN_ROOT`
- `REASONIX_PLUGIN_NAME`
- `REASONIX_PLUGIN_VERSION`
- `REASONIX_HOME`
- `REASONIX_WORKSPACE_ROOT`

## 桌面端后端方法

Desktop 通过 Wails 方法暴露插件包操作：

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`
