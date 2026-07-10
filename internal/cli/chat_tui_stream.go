package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/i18n"
)

func (m *chatTUI) streamReasoning(chunk string) {
	m.reasoning.WriteString(chunk) // full text retained for verbose mode
	if m.reasoningTextIdx < 0 {
		return
	}
	m.reasoningView = append(m.reasoningView, chunk...)
	if len(m.reasoningView) > reasoningViewMax {
		drop := len(m.reasoningView) - reasoningViewMax
		for drop < len(m.reasoningView) && !utf8.RuneStart(m.reasoningView[drop]) {
			drop++
		}
		m.reasoningView = m.reasoningView[:copy(m.reasoningView, m.reasoningView[drop:])]
	}
	m.transcript[m.reasoningTextIdx] = reasoningBlock(string(m.reasoningView), m.width, reasoningTailLines)
	m.transcriptDirty = true
}

// reasoningBlock renders raw thinking text as dim, width-wrapped lines under a
// "⎿" connector that ties the block to the "▎ thinking…" marker above it. A
// positive maxLines keeps only the trailing visual lines (the live view); 0
// renders all (verbose collapse).
func reasoningBlock(raw string, width, maxLines int) string {
	// Reserve 1 column for the scrollbar so the block fits inside the viewport.
	w := (width - 1) - ansi.StringWidth(connector)
	if w < 8 {
		w = 8
	}
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		for _, wl := range strings.Split(ansi.Wrap(expandTabs(ln), w, ""), "\n") {
			lines = append(lines, dim(wl))
		}
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return connectorBlock(lines)
}

// toolStreamTailLines caps how many trailing output lines a running tool shows;
// the live block scrolls within this window so a chatty build doesn't flood.
const toolStreamTailLines = 8

// shellPreviewLines is how many lines of shell output to show by default after
// the command finishes. Ctrl+B toggles the full output.
const shellPreviewLines = 10

// shellExpandMaxLines caps how many lines Ctrl+B shows in expanded mode, so a
// very large output (e.g. thousands of lines) doesn't hang the TUI or push the
// input box off-screen.
const shellExpandMaxLines = 200

// streamToolOutput appends a chunk of a running tool's output and re-renders its
// live block (the last toolStreamTailLines lines) under the tool card, opening
// the block on the first chunk. Mirrors streamReasoning.
func (m *chatTUI) streamToolOutput(id, chunk string) {
	if id == "" {
		return
	}
	if m.toolStreamID != id {
		m.collapseToolOutput(m.toolStreamID)
		m.toolStreamID = id
		m.toolTail = m.toolTail[:0]
		m.toolPartial = ""
		m.toolLineCount = 0
		m.toolStreamIdx = len(m.transcript)
		m.commitLine("")
	}
	// Accumulate full output for shell commands so Ctrl+B can expand it.
	if strings.HasPrefix(id, "shell-") {
		m.shellOutputs[id] += chunk
	}
	// Fold completed lines into the bounded tail; keep the trailing partial.
	data := m.toolPartial + chunk
	for {
		i := strings.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		m.pushToolLine(strings.TrimRight(data[:i], "\r"))
		data = data[i+1:]
	}
	m.toolPartial = data

	vis := m.toolTail
	if m.toolPartial != "" {
		vis = append(append([]string{}, m.toolTail...), m.toolPartial)
	}
	lines := make([]string, len(vis))
	for i, ln := range vis {
		lines[i] = dim(clampPlain(ln, (m.width-1)-ansi.StringWidth(connector)))
	}
	m.transcript[m.toolStreamIdx] = connectorBlock(lines)
	m.transcriptDirty = true
}

// pushToolLine appends a completed output line to the bounded tail, dropping the
// oldest when it exceeds the window (the backing array stays ≤ window+1).
func (m *chatTUI) pushToolLine(line string) {
	m.toolLineCount++
	m.toolTail = append(m.toolTail, line)
	if len(m.toolTail) > toolStreamTailLines {
		copy(m.toolTail, m.toolTail[1:])
		m.toolTail = m.toolTail[:toolStreamTailLines]
	}
}

// collapseToolOutput replaces a finished tool's live block with a dim
// "⎿ N lines" summary, so the scrollback keeps a marker of the run without the
// full output (which the model already received). For shell commands ("shell-"
// prefix), it shows the first shellPreviewLines with a Ctrl+B hint instead.
// No-op when id isn't streaming.
func (m *chatTUI) collapseToolOutput(id string) {
	if m.toolStreamIdx < 0 || id == "" || m.toolStreamID != id {
		return
	}
	n := m.toolLineCount
	if m.toolPartial != "" {
		n++
	}
	if n == 0 {
		if m.toolStreamIdx == len(m.transcript)-1 {
			m.transcript = m.transcript[:m.toolStreamIdx]
		} else {
			m.transcript[m.toolStreamIdx] = ""
		}
	} else if full, ok := m.shellOutputs[id]; ok {
		// Shell command: show first N lines + hint.
		lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
		total := len(lines)
		if total > shellPreviewLines {
			preview := make([]string, shellPreviewLines+1)
			for i := 0; i < shellPreviewLines; i++ {
				preview[i] = dim(clampPlain(lines[i], (m.width-1)-ansi.StringWidth(connector)))
			}
			preview[shellPreviewLines] = dim(fmt.Sprintf("… %d more lines (click/Ctrl+B)", total-shellPreviewLines))
			m.transcript[m.toolStreamIdx] = connectorBlock(preview)
		} else {
			rendered := make([]string, total)
			for i, ln := range lines {
				rendered[i] = dim(clampPlain(ln, (m.width-1)-ansi.StringWidth(connector)))
			}
			m.transcript[m.toolStreamIdx] = connectorBlock(rendered)
		}
		m.shellTranscriptIdx[id] = m.toolStreamIdx
	} else {
		m.transcript[m.toolStreamIdx] = connectorBlock([]string{dim(fmt.Sprintf("%d lines", n))})
	}
	m.transcriptDirty = true
	m.toolStreamIdx = -1
	m.toolStreamID = ""
	m.toolTail = m.toolTail[:0]
	m.toolPartial = ""
	m.toolLineCount = 0
}

// toggleShellOutput expands or collapses the output of the most recent shell
// command. When expanded, up to shellExpandMaxLines lines are shown; when
// collapsed, only the first shellPreviewLines are shown. Called on Ctrl+B.
func (m *chatTUI) toggleShellOutput() {
	// Find the most recent shell output that has a transcript entry.
	var lastID string
	var lastIdx int
	for id, idx := range m.shellTranscriptIdx {
		if idx > lastIdx {
			lastID = id
			lastIdx = idx
		}
	}
	if lastID == "" {
		return
	}
	full, ok := m.shellOutputs[lastID]
	if !ok {
		return
	}
	lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
	total := len(lines)
	innerW := (m.width - 1) - ansi.StringWidth(connector)
	if innerW < 10 {
		innerW = 80
	}

	if m.shellExpanded[lastID] {
		// Collapse back to preview.
		m.shellExpanded[lastID] = false
		if total > shellPreviewLines {
			preview := make([]string, shellPreviewLines+1)
			for i := 0; i < shellPreviewLines; i++ {
				preview[i] = dim(clampPlain(lines[i], innerW))
			}
			preview[shellPreviewLines] = dim(fmt.Sprintf("… %d more lines (click/Ctrl+B)", total-shellPreviewLines))
			m.transcript[lastIdx] = connectorBlock(preview)
		}
	} else {
		// Expand: show up to shellExpandMaxLines lines.
		m.shellExpanded[lastID] = true
		show := total
		if show > shellExpandMaxLines {
			show = shellExpandMaxLines
		}
		rendered := make([]string, show)
		for i := 0; i < show; i++ {
			rendered[i] = dim(clampPlain(lines[i], innerW))
		}
		if total > shellExpandMaxLines {
			rendered = append(rendered, dim(fmt.Sprintf("… %d more lines", total-shellExpandMaxLines)))
		}
		m.transcript[lastIdx] = connectorBlock(rendered)
	}
	m.transcriptDirty = true
}

// toolWorkingFrames is the braille spinner cycled once per second on the
// "⎿ working · Ns" line of a tool that hasn't streamed output yet.
var toolWorkingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// beginToolRunning opens an empty live block under a just-dispatched tool card,
// keyed by the call id. tickToolRunning fills it with a "working · Ns" line each
// second; if the tool later streams output, streamToolOutput reuses the same
// block; collapseToolOutput closes it on the result.
func (m *chatTUI) beginToolRunning(id string) {
	if id == "" {
		return
	}
	m.toolStreamID = id
	m.toolTail = m.toolTail[:0]
	m.toolPartial = ""
	m.toolLineCount = 0
	m.toolStreamStart = time.Now()
	m.toolStreamFrame = 0
	m.toolStreamIdx = len(m.transcript)
	m.commitLine(connectorBlock([]string{dim(fmt.Sprintf(i18n.M.ChatToolWorkingFmt, toolWorkingFrames[0], 0))}))
}

// tickToolRunning re-renders the working line of a tool that's dispatched but
// hasn't produced output yet. A no-op once output streams in or no tool runs.
func (m *chatTUI) tickToolRunning() {
	if m.toolStreamIdx < 0 || m.toolLineCount != 0 || m.toolPartial != "" {
		return
	}
	m.toolStreamFrame++
	frame := toolWorkingFrames[m.toolStreamFrame%len(toolWorkingFrames)]
	secs := int(time.Since(m.toolStreamStart).Seconds())
	m.transcript[m.toolStreamIdx] = connectorBlock([]string{dim(fmt.Sprintf(i18n.M.ChatToolWorkingFmt, frame, secs))})
	m.transcriptDirty = true
}

// commitReasoning closes the live thinking block: the "▎ thinking…" marker is
// rewritten to a dim "▎ thought for Ns" summary and the streamed text below it is
// removed (collapsed) — kept only in verbose mode. The viewport re-wraps from
// m.transcript, so the change is flagged via transcriptDirty.
func (m *chatTUI) commitReasoning() {
	if m.reasoningNative {
		if strings.TrimSpace(m.reasoning.String()) != "" || !m.thinkStart.IsZero() {
			secs := int(time.Since(m.thinkStart).Seconds())
			m.commitSpacer()
			m.commitLine(dim(fmt.Sprintf("  ▎ "+i18n.M.ChatThoughtForFmt, secs)))
			if m.showReasoning && strings.TrimSpace(m.reasoning.String()) != "" {
				m.commitLine(reasoningBlock(m.reasoning.String(), m.width, 0))
			}
		}
		m.reasoning.Reset()
		m.reasoningView = m.reasoningView[:0]
		m.reasoningNative = false
		m.thinkStart = time.Time{}
		return
	}
	if m.reasoningLineIdx < 0 {
		return
	}
	secs := int(time.Since(m.thinkStart).Seconds())
	m.transcript[m.reasoningLineIdx] = dim(fmt.Sprintf("  ▎ "+i18n.M.ChatThoughtForFmt, secs))
	if m.reasoningTextIdx >= 0 {
		if m.showReasoning && strings.TrimSpace(m.reasoning.String()) != "" {
			m.transcript[m.reasoningTextIdx] = reasoningBlock(m.reasoning.String(), m.width, 0)
		} else {
			m.transcript = append(m.transcript[:m.reasoningTextIdx], m.transcript[m.reasoningTextIdx+1:]...)
		}
	}
	m.transcriptDirty = true
	m.reasoning.Reset()
	m.reasoningView = m.reasoningView[:0]
	m.reasoningLineIdx = -1
	m.reasoningTextIdx = -1
}

// streamAnswer renders the answer streamed so far up to its last completed
// paragraph (flushableMarkdownPrefix) and writes it as one transcript block,
// rewritten in place as later paragraphs land — so a long reply appears chunk by
// chunk instead of all at once on turn end. The trailing, still-streaming block
// stays buffered (a half-written fence/list never renders early), and it only
// re-renders when a new paragraph actually closes.
func (m *chatTUI) streamAnswer() {
	prefix := flushableMarkdownPrefix(m.pending.String())
	if len(prefix) <= m.answerFlushed {
		return
	}
	rendered := m.renderer.Render(prefix)
	if rendered == "" {
		return
	}
	m.answerFlushed = len(prefix)
	block := strings.TrimRight(rendered, "\n")
	block = assistantBlock(block)
	if m.answerIdx < 0 {
		m.answerIdx = len(m.transcript)
		m.commitLine(block)
	} else {
		m.transcript[m.answerIdx] = block
		m.transcriptDirty = true
	}
}

// commitPending freezes the full accumulated answer as markdown — overwriting the
// streamed block if one is open (streamAnswer), else committing fresh. Joining
// commitReasoning then commitPending puts the answer on its own line, restoring
// the thinking→answer break the renderer strips.
func (m *chatTUI) commitPending() {
	if m.pending.Len() == 0 {
		m.answerIdx = -1
		m.answerFlushed = 0
		return
	}
	raw := m.pending.String()
	rendered := m.renderer.Render(raw)
	if rendered == "" {
		rendered = raw
	}
	block := strings.TrimRight(rendered, "\n")
	block = assistantBlock(block)
	if m.answerIdx < 0 {
		m.commitLine(block)
	} else {
		m.transcript[m.answerIdx] = block
		m.transcriptDirty = true
	}
	m.pending.Reset()
	m.answerIdx = -1
	m.answerFlushed = 0
}

// flushableMarkdownPrefix returns the longest prefix of buf made of complete
// markdown blocks — text up to the last blank line outside any open fenced code
// block. A blank line inside a ``` / ~~~ fence isn't a boundary, so a half-written
// code block stays buffered until it closes.
func flushableMarkdownPrefix(buf string) string {
	lines := strings.Split(buf, "\n")
	inFence := false
	boundary := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && t == "" {
			boundary = i
		}
	}
	if boundary <= 0 {
		return ""
	}
	return strings.Join(lines[:boundary], "\n")
}

// handleApprovalKey resolves a pending approval from a keystroke and re-arms the
// listener. 1/y/Enter allows once, 2/a allows for the rest of the session,
// 3/p writes an "always allow" rule to the config file, 4/n/Esc denies.
// Ctrl-C cancels the whole turn via the run context.
// When Scope == "task", only Enter (approve), Esc (deny), and Ctrl-C (cancel)
// are accepted; the multi-button prompts are hidden.
func (m chatTUI) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	answer := func(allow, session, persist bool) (tea.Model, tea.Cmd) {
		m.ctrl.Approve(m.pendingApproval.ID, allow, session, persist)
		m.pendingApproval = nil
		return m, nil // the next ApprovalRequest / event arrives on eventCh
	}
	// Task-scope approvals are a simple two-button gate: approve or deny.
	if m.pendingApproval.Scope == "task" {
		switch msg.String() {
		case "ctrl+c":
			m.ctrl.Cancel()
			return answer(false, false, false)
		case "enter":
			return answer(true, false, false)
		case "esc":
			return answer(false, false, false)
		}
		return m, nil // ignore everything else
	}
	switch msg.String() {
	case "ctrl+c":
		m.ctrl.Cancel() // cancels the run; the approver unblocks via ctx.Done()
		return answer(false, false, false)
	case "enter":
		return answer(true, false, false)
	case "esc":
		return answer(false, false, false)
	}
	switch strings.ToLower(msg.String()) {
	case "y", "1":
		return answer(true, false, false)
	case "a", "2":
		return answer(true, true, false) // session grant
	case "p", "3":
		return answer(true, true, true) // persist to config
	case "n", "4":
		return answer(false, false, false)
	}
	return m, nil // ignore anything else while awaiting a decision
}

var (
	// Input box: only top + bottom borders, no sides. The concrete colors are
	// refreshed from the active CLI theme during startup.
	inputBoxStyle       lipgloss.Style
	approvalBannerStyle lipgloss.Style
	statusBlockStyle    lipgloss.Style
	workingStyle        lipgloss.Style
)
