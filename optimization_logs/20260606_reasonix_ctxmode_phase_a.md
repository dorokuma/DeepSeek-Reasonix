# Reasonix Context Mode — Phase A

## 目标
原生实现 context-mode 的「出站压缩」：大体积 read_file / grep / MCP 结果先入旁路存储，再给模型摘要 + ref，用 ctx_read / ctx_search 按需取回。

## Phase A 范围
- `internal/ctxmode`：Store、Transform、REASONIX_CTX* 环境变量
- builtin：`ctx_read`、`ctx_search`
- agent：`compactToolOutput` 集成；executeOne 注入 Store

## 不在本阶段
- ctx_run（Phase B）
- PreCompact + SQLite FTS 续接（Phase C）

## 验证
```bash
cd /root/reasonix && go test ./internal/ctxmode/... ./internal/agent/... -count=1
go build -o /usr/local/bin/reasonix ./cmd/reasonix
```