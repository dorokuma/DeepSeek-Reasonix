# reasonix-telegram（桥）

Reasonix 的 **Telegram 薄适配前端**：对接同一套 `boot.Build` → `Controller`，不另起 agent 运行时。

## 权威设计

见主机文档：`/root/docs/spark/2026-07-13-bridge-thin-adapter.md`。

## 原则

- **复用主程序**：输入默认 `Submit`，事件/审批/工具/多代理走核心 API  
- **壳只做 Telegram**：官方草稿流式（过程）+ 定稿完整（结果）+ typing + 内联审批/Ask + 多 chat 会话索引  
- **不加** TG 专用 system prompt，不 per-chat `reasonix serve`，不 chat-only 锁死工具  

## 运行

- 二进制：`/usr/local/bin/reasonix-telegram`（`go build -o … ./cmd/bridge`）  
- 环境：`/etc/reasonix-telegram.env`（`TG_BOT_TOKEN`、`ALLOWED_USERS`、`WORK_DIR`、`STATE_DIR`、提供商密钥等）  
- systemd：`reasonix-telegram.service`  

## 已废弃叙事

以下均已作废，勿再实现：

- 每 chat 一个 `reasonix serve` + HTTP/SSE  
- `tools.enabled = ["none"]` 纯聊天锁  
- 独立 reasonix-telegram 仓库 async-bridge spark 设计（已删）  
