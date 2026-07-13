# ctxmode Phase A+B+C 部署后审计（Round 2）

日期：2026-06-06  
范围：ctxmode 出站沙箱（A）、ctx_run（B）、journal/PreCompact 续接（C）、RTK 并存、bridge 部署

## RUN

- `reasonix doctor`：ctx active=true，journal ok (FTS5)，rtk rewrite ok
- `/usr/local/bin/reasonix` 已重建（Phase C wiring）
- `reasonix-telegram.service` active，已 restart 加载新 binary
- 当前无活跃 `reasonix serve` 子进程（无进行中的 chat session）

## CODE — Phase C 接线核对

| 检查项 | 状态 |
|--------|------|
| Run → RecordUserPrompt | ok |
| executeOne → RecordTool（原始 result，压缩前） | ok |
| compact → CompactGuidance 进 summarizer | ok |
| compact → CompactResumeBlock 进 summary 消息 | ok |
| Store.Remove → journal.Close | ok |
| doctor journal probe | ok |
| agent TestMain REASONIX_CTX=off goleak | ok |

## 发现问题

### AUDIT-007 P2 — journal 索引工具覆盖面偏窄
`RecordTool` 仅索引 read_file / edit* / grep / git bash / ctx_run。glob、ls、web_fetch、MCP 大结果不入 journal，auto-compact 时这些操作的语义可能丢失。  
影响：长会话靠 glob/MCP 探索时，resume block 可能缺路径线索。  
建议：后续按需扩展 RecordTool case（低优先级）。

### AUDIT-008 P2 — ctxmode cache 目录累积
`~/.config/reasonix/cache/ctxmode/` 约 300+ 会话目录（含测试 `NewStore` 与未 Cleanup 的 session）。  
影响：磁盘占用缓慢增长；journal.db 与 ref 文件残留。  
现状：controller Close → CleanupCtxStore、/new → ResetCtxStore 已覆盖正常路径。  
建议：周期性清理无对应活跃 session 的 orphan 目录（可选 cron）；测试已 defer store.Remove。

### AUDIT-009 P2 — bridge .env 未设 REASONIX_CTX_LOG
建议：调试期设 `REASONIX_CTX_LOG=miss`，稳定后 off。

### AUDIT-010 P3 — journal Record 写库错误静默
`Record` 使用 `_, _ = j.db.Exec`，FTS 写入失败无日志。  
影响：极端磁盘/权限故障时续接退化且无告警。  
建议：REASONIX_CTX_LOG=all 时记录 write error（后续小改）。

## 无问题项（回归）

- RTK rewrite 门与 ctxmode agent 层 transform 分层未混用
- 子 agent 共享 CtxStore（AUDIT-002 修复保持）
- ctx_read 80 / ctx_search 40 默认分页（AUDIT-006 保持）
- ctx_run 需 REASONIX_CTX=on，60s/16KB cap 未变

## 修复（Round 2 跟进，2026-06-06）

- AUDIT-007：`RecordTool` 扩展 glob / ls / web_fetch / ctx_read / ctx_search / MCP
- AUDIT-008：`PruneOrphanCache`（.pid 存活检测）+ NewStore/controller/doctor 触发；`config.CacheDir` 尊重 `REASONIX_CACHE_DIR`
- AUDIT-009：bridge `.env` 增加 `REASONIX_CTX_LOG=miss`
- AUDIT-010：`Record` 写库失败 → `LogJournalErr`（REASONIX_CTX_LOG=all）

## 结论

Phase C 已落地并部署；A+B+C 功能链路完整，Round 2 审计项全部修复。