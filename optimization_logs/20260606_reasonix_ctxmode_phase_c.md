# Reasonix Context Mode — Phase C

## 目标
compaction 续接：会话事件写入 SQLite FTS5 journal，压缩前注入 CompactGuidance，压缩后插入 CompactResumeBlock。

## 实现
- `internal/ctxmode/journal.go`：FTS5 events 表 + RecordUserPrompt / RecordTool
- `internal/agent/agent.go`：Run 索引 user prompt；executeOne 索引 tool 结果
- `internal/agent/compact.go`：CompactGuidance → summarizer instructions；CompactResumeBlock → summary 正文
- `internal/doctor`：journal FTS probe
- `internal/agent/main_test.go`：默认 REASONIX_CTX=off 避免 sqlite goleak

## 验证
```bash
cd /root/reasonix && go test ./internal/ctxmode/... ./internal/agent/... ./internal/doctor/... -count=1
go build -o /usr/local/bin/reasonix ./cmd/reasonix
reasonix doctor   # ctx.journal ok
systemctl restart reasonix-telegram.service
```