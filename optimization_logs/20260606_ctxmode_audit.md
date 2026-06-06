# ctxmode Phase A 审计

## AUDIT-001 P1 — Transform 存储失败静默降级
Put 失败时 ok=false，无日志，大结果直接截断。

## AUDIT-002 P1 — 子 agent 独立 Store，ref 不共享
task 子 agent 新建 Store，父会话 ctx-N 在子 agent 不可见。

## AUDIT-003 P1 — 会话结束未清理 cache 目录
ctxmode 目录累积，磁盘泄漏。

## AUDIT-004 P1 — read_file 摘要双重行号
预览对已有 `N→` 前缀的内容再次编号。

## AUDIT-005 P2 — doctor 无 ctxmode 段
无法自检 REASONIX_CTX 配置。

## AUDIT-006 P2 — ctx_read 默认 200 行偏大
分页工具输出仍可能偏大。

## 修复（已完成）
- AUDIT-001: LogMissStore / LogHitSandbox + REASONIX_CTX_LOG
- AUDIT-002: Options.CtxStore 子 agent 共享父 Store
- AUDIT-003: Store.Remove + ResetCtxStore/CleanupCtxStore on /new 和 Close
- AUDIT-004: read_file 预览检测已有行号，避免双重编号
- AUDIT-005: doctor ctx 段
- AUDIT-006: ctx_read 默认 80 行，ctx_search 默认 40 行

## Phase B（已完成）
- builtin `ctx_run`（javascript/python/bash，stdout only，60s，16KB cap）
- 大 ctx_run 输出走 ctxmode sandbox