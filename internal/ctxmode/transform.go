package ctxmode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"reasonix/internal/tool"
)

// Transform stores oversized tool output and returns a short summary for the model.
// ok is false when sandboxing does not apply (pass through to RTK/truncation).
func Transform(store *Store, toolName string, args json.RawMessage, body string) (summary, notice string, ok bool) {
	return TransformCooperative(store, toolName, args, body, "", "", false, 0)
}

// TransformCooperative stores the full tool output for ctx_read/ctx_search and
// returns a model-facing summary. When RTK pipe produced a smaller view
// (compactBody), that text is shown inline; the tool always keeps fullBody.
func TransformCooperative(store *Store, toolName string, args json.RawMessage, fullBody, compactBody, pipeNotice string, pipedOK bool, maxModelBytes int) (summary, notice string, ok bool) {
	if store == nil || !Active() || len(fullBody) < ThresholdBytes() {
		return "", "", false
	}
	if !sandboxTool(toolName) {
		return "", "", false
	}
	subject := subjectFromArgs(toolName, args)
	id, err := store.Put(toolName, subject, fullBody)
	if err != nil {
		LogMissStore(toolName, len(fullBody), err)
		return "", "", false
	}
	LogHitSandbox(toolName, id, len(fullBody))
	// Sandbox store records are sent only to the diagnostic log (via LogHitSandbox
	// when REASONIX_CTX_LOG=all). We do not return a user-facing notice for the
	// ctx sandbox store itself (nor the rtk pipeNotice): that previously became
	// a Notice event and caused spam in the chat. The model receives the
	// necessary ref + RTK compact view via the summary (first return value).
	notice = ""
	useCompact := pipedOK && compactBody != "" && len(compactBody) < len(fullBody) &&
		(maxModelBytes <= 0 || len(compactBody) <= maxModelBytes)
	if useCompact {
		summary = buildPipedSummary(id, toolName, subject, len(fullBody), compactBody)
	} else {
		summary = buildSummary(id, toolName, subject, fullBody)
	}

	// Record compaction stats
	store.Stats.Record(len(fullBody), len(summary))
	slog.Info("ctx status=hit", "ref", id, "tool", toolName, "bytes", fmt.Sprintf("%d→%d", len(fullBody), len(summary)))

	return summary, notice, true
}

func buildPipedSummary(id, toolName, subject string, fullBytes int, compactBody string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[ctx] stored %s output (ref=%s, bytes=%d", toolName, id, fullBytes)
	if subject != "" {
		fmt.Fprintf(&b, ", subject=%q", subject)
	}
	b.WriteString(")\n")
	b.WriteString("Full raw output is in ctx store. RTK-compacted view below; use ctx_read/ctx_search for the original.\n\n")
	b.WriteString("--- RTK compact ---\n")
	b.WriteString(compactBody)
	if compactBody != "" && !strings.HasSuffix(compactBody, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func sandboxTool(name string) bool {
	switch name {
	case "read_file", "grep", "glob", "ls", "ctx_run":
		return true
	default:
		return strings.HasPrefix(name, tool.MCPNamePrefix)
	}
}

func subjectFromArgs(toolName string, args json.RawMessage) string {
	switch toolName {
	case "read_file":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &p) == nil {
			return p.Path
		}
	case "grep":
		var p struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal(args, &p) == nil {
			return p.Pattern
		}
	case "glob":
		var p struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal(args, &p) == nil {
			return p.Pattern
		}
	case "ls":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &p) == nil {
			return p.Path
		}
	}
	if strings.HasPrefix(toolName, tool.MCPNamePrefix) {
		return toolName
	}
	return ""
}

func buildSummary(id, toolName, subject, body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[ctx] stored %s output (ref=%s, bytes=%d, lines=%d", toolName, id, len(body), len(lines))
	if subject != "" {
		fmt.Fprintf(&b, ", subject=%q", subject)
	}
	b.WriteString(")\n")
	b.WriteString("Raw output is NOT in context. Use ctx_read(ref, offset, limit) to page; ctx_search(ref, pattern) to find lines.\n\n")

	switch toolName {
	case "read_file":
		writePreview(&b, lines, 18, 8, true)
	case "grep":
		writeGrepSummary(&b, lines)
	default:
		writePreview(&b, lines, 15, 5, false)
	}
	return b.String()
}

func hasNumberedLines(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	n := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if numberedLine(ln) {
			n++
		}
		if n >= 3 {
			return true
		}
	}
	return false
}

func numberedLine(ln string) bool {
	ln = strings.TrimSpace(ln)
	i := 0
	for i < len(ln) && ln[i] >= '0' && ln[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	rest := strings.TrimSpace(ln[i:])
	return strings.HasPrefix(rest, "→")
}

func writePreview(b *strings.Builder, lines []string, headN, tailN int, maybeReadFile bool) {
	if len(lines) == 0 {
		b.WriteString("(empty)\n")
		return
	}
	numbered := maybeReadFile && hasNumberedLines(lines)
	emit := func(i int, ln string) {
		if numbered {
			b.WriteString(ln)
			b.WriteByte('\n')
			return
		}
		fmt.Fprintf(b, "%5d→%s\n", i+1, ln)
	}
	if len(lines) <= headN+tailN {
		b.WriteString("--- content ---\n")
		for i, ln := range lines {
			emit(i, ln)
		}
		return
	}
	b.WriteString("--- preview (head) ---\n")
	for i := 0; i < headN && i < len(lines); i++ {
		emit(i, lines[i])
	}
	fmt.Fprintf(b, "\n… %d lines omitted …\n\n--- preview (tail) ---\n", len(lines)-headN-tailN)
	start := len(lines) - tailN
	for i := start; i < len(lines); i++ {
		emit(i, lines[i])
	}
}

func writeGrepSummary(b *strings.Builder, lines []string) {
	files := map[string]int{}
	for _, ln := range lines {
		if path, _, ok := strings.Cut(ln, ":"); ok && path != "" {
			files[path]++
		}
	}
	fmt.Fprintf(b, "match_lines=%d unique_paths=%d\n", len(lines), len(files))
	if len(files) > 0 {
		b.WriteString("paths: ")
		n := 0
		for p, c := range files {
			if n > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%s(%d)", p, c)
			n++
			if n >= 12 {
				b.WriteString(", …")
				break
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n--- sample matches ---\n")
	limit := 20
	if len(lines) < limit {
		limit = len(lines)
	}
	for i := 0; i < limit; i++ {
		fmt.Fprintf(b, "%5d→%s\n", i+1, lines[i])
	}
	if len(lines) > limit {
		fmt.Fprintf(b, "… %d more match lines\n", len(lines)-limit)
	}
}