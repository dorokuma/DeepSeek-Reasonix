package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/diag"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/memory"
	"reasonix/internal/outputstyle"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func (m chatTUI) View() tea.View {
	boxW := m.width
	if boxW < 10 {
		boxW = 10
	}
	narrow := boxW < 80 // phone-width: compact status lines
	hideComposer := m.hideComposer()
	shellMode := strings.HasPrefix(strings.TrimSpace(m.input.Value()), "!")
	var box string
	if !hideComposer {
		style := inputBoxStyle.Width(boxW)
		if shellMode {
			style = style.BorderForeground(lipgloss.Color(statusShellColor.hex))
		}
		box = style.Render(m.input.View())
	}

	var modeTag string
	switch {
	case shellMode:
		modeTag = lipgloss.NewStyle().
			Background(lipgloss.Color(statusShellColor.hex)).
			Foreground(lipgloss.Color("#ffffff")).
			Bold(true).
			Padding(0, 1).
			Render("Shell")
	case m.ctrl.Bypass():
		modeTag = lipgloss.NewStyle().
			Background(lipgloss.Color(statusYoloColor.hex)).
			Foreground(lipgloss.Color("#ffffff")).
			Bold(true).
			Padding(0, 1).
			Render("YOLO")
	default:
		modeTag = lipgloss.NewStyle().
			Background(lipgloss.Color(statusAutoColor.hex)).
			Foreground(lipgloss.Color("#111827")).
			Bold(true).
			Padding(0, 1).
			Render("Auto")
	}

	ctxTag := m.contextTag()
	var status string
	if narrow {
		// Narrow (phone) mode: just mode + effort, no status hints, no git.
		switch {
		case m.rewind != nil:
			status = "  " + modeTag + " · ⟲ rw"
		case m.resumePick != nil:
			status = "  " + modeTag + " · resume"
		case m.mcpImport != nil:
			status = "  " + modeTag + " · MCP import"
		case m.mcp != nil:
			status = "  " + modeTag + " · MCP"
		case m.skillPick != nil:
			status = "  " + modeTag + " · skills"
		case m.chooser != nil:
			status = "  " + modeTag + " · question"
		case m.pendingApproval != nil:
			if m.pendingApproval.Scope == "task" {
				status = "  " + modeTag + " · task approve"
			} else {
				status = "  " + modeTag + " · approve"
			}
		case shellMode:
			status = "  " + modeTag + " · shell"
		case m.state == tuiRunning:
			status = "  " + modeTag + " · running"
		default:
			status = "  " + modeTag
		}
		if et := m.effortTag(); et != "" {
			status += " · " + et
		}
	} else {
		switch {
		case m.rewind != nil:
			status = "  " + modeTag + " · ⟲ rewind"
		case m.mcpImport != nil:
			status = "  " + modeTag + " · MCP import"
		case m.resumePick != nil:
			status = "  " + modeTag + " · " + i18n.M.StatusResumePicker
		case m.mcp != nil:
			status = "  " + modeTag + " · MCP"
		case m.skillPick != nil:
			status = "  " + modeTag + " · " + i18n.M.SkillPickerStatusLabel
		case m.chooser != nil:
			status = "  " + modeTag + " · " + i18n.M.ChatStatusQuestion
		case m.pendingApproval != nil:
			if m.pendingApproval.Scope == "task" {
				status = "  " + modeTag + " · Task 审批中 · Enter 批准 · Esc 拒绝"
			} else {
				status = "  " + modeTag + " · " + i18n.M.ChatStatusToolApproval
			}
		case shellMode:
			status = "  " + modeTag + " · " + i18n.M.ShellModeHint
		case m.ctrl.Bypass():
			status = "  " + modeTag + " · " + i18n.M.ChatStatusYoloIdle
		default:
			status = "  " + modeTag + " · " + i18n.M.ChatStatusIdle
		}
		if et := m.effortTag(); et != "" {
			status += " · " + et
		}
		if gt := m.gitTag(boxW - visibleWidth(status) - visibleWidth(" · ")); gt != "" {
			status += " · " + gt
		}
	}
	// The spinning "thinking…" indicator is its own line ABOVE the input box (shown
	// only while a turn runs); the status/data rows stay below. This mirrors Claude
	// Code: live progress over the composer, shortcuts + stats under it.
	var working string
	if m.state == tuiRunning {
		if m.retryAttempt > 0 {
			working = fmt.Sprintf("  "+i18n.M.ChatStatusRetryingFmt, m.spinner.View(), m.retryAttempt, m.retryMax)
		} else {
			working = fmt.Sprintf("  "+i18n.M.ChatStatusThinkingFmt, m.spinner.View(), m.elapsed)
			if m.turnTokens > 0 {
				working += " · ↓" + shortTokens(m.turnTokens)
			}
			if n := len(m.pendingInterject); n > 0 {
				if n == 1 {
					working += dim(" · ✎ feedback queued")
				} else {
					working += dim(fmt.Sprintf(" · ✎ %d queued", n))
				}
			}
		}
	}
	// Cache rates get their own fixed second row so they're never truncated off
	// the status line; a fixed height also avoids wrap-induced resize ghosting.
	var data []string
	if narrow {
		// Narrow (phone) mode: cache rates + cost only.
		if cache := m.cacheTag(); cache != "" {
			data = append(data, cache)
		}
		if cost := m.sessionCostTag(); cost != "" {
			data = append(data, cost)
		}
	} else {
		if mt := m.modelTag(); mt != "" {
			data = append(data, mt)
		}
		if cache := m.cacheTag(); cache != "" {
			data = append(data, cache)
		}
		if ctxTag != "" {
			data = append(data, ctxTag)
		}
		if jt := ""; jt != "" {
			data = append(data, jt)
		}
		if m.balance != "" {
			data = append(data, dim(m.balance))
		}
		if cost := m.sessionCostTag(); cost != "" {
			data = append(data, cost)
		}
	}
	dataLine := "  " + strings.Join(data, " · ")
	// A configured custom status line replaces the built-in data row entirely
	// (except in narrow mode where we keep cache+cost).
	if !narrow && m.statuslineCmd != "" && m.statuslineOut != "" {
		dataLine = "  " + m.statuslineOut
	}

	// Bottom region pinned under the transcript viewport: optional panels, the
	// composer when visible, then the two status rows. Its height feeds
	// transcriptHeight so the viewport above fills exactly the rest of the screen.
	var parts []string
	rowsAboveBox := 0 // terminal rows occupied by panels/working line before the composer
	if banner := m.renderApprovalBanner(); banner != "" {
		parts = append(parts, banner)
		rowsAboveBox += strings.Count(banner, "\n") + 1
	}
	if card := m.renderChooser(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderRewind(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderMCPImport(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderResumePicker(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if menu := m.renderCompletion(); menu != "" {
		parts = append(parts, menu)
		rowsAboveBox += strings.Count(menu, "\n") + 1
	}
	// Layout: the working spinner (when running), then the composer when visible,
	// then the two status rows (line 1 = mode + run config + worktree identity, line 2 = live run data).
	// Each row is clamped to width independently so neither wraps; padding to full
	// width keeps a short row from leaving stale cells from the prior frame.
	if working != "" {
		parts = append(parts, workingStyle.Width(boxW).MaxWidth(boxW).Render(clampStatusLine(working, boxW)))
		rowsAboveBox++
	}
	if footer := m.renderMainManagerFooter(); footer != "" {
		parts = append(parts, footer)
		rowsAboveBox += strings.Count(footer, "\n") + 1
	}
	statusBlock := clampStatusLine(status, boxW) + "\n" + clampStatusLine(dataLine, boxW)
	if !hideComposer {
		if qi := m.renderQueueIndicator(); qi != "" {
			parts = append(parts, qi)
			rowsAboveBox += strings.Count(qi, "\n") + 1
		}
		parts = append(parts, box)
	}
	parts = append(parts, statusBlockStyle.Width(boxW).MaxWidth(boxW).Render(statusBlock))

	// Full-screen frame: the transcript viewport on top (it pads to exactly its
	// height), the pinned bottom region beneath. Alt-screen owns the grid, so
	// resize repaints cleanly — no scrollback reflow, no ghost borders.
	// Native-scrollback mode also uses alt-screen now, but with mouse mode off:
	// Conduit (Android) translates swipe gestures to arrow keys when alt-buffer is
	// active, and mouse-mode-off keeps taps from being intercepted so the soft
	// keyboard still raises on tap.
	mainArea := m.renderTranscript()
	if card := m.renderMainManager(); card != "" {
		mainArea = m.renderTranscriptWithMainManager(card)
	}
	v := tea.NewView(mainArea + "\n" + strings.Join(parts, "\n"))
	if m.nativeScrollback {
		v.AltScreen = true
		// MouseMode stays at default (MouseModeNone)
	} else {
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion // wheel scrolls the transcript
	}
	// Anchor the real terminal cursor at the textarea's insertion point only when
	// the composer is visible. input.Cursor() is relative to the textarea; offset
	// by the viewport height + rows above + the box's top border row (+1 column
	// for PaddingLeft).
	if !hideComposer {
		if cur := m.input.Cursor(); cur != nil {
			cur.X += 1
			cur.Y += m.viewport.Height() + rowsAboveBox + 1
			v.Cursor = cur
		}
	}
	return v
}

// compactionCardLines renders a finished compaction as a titled card: a header
// with the message count and trigger, then the structured summary under a dim
// gutter so it reads as one block in scrollback. The summary is also the new
// context base, so this card is the user's window into exactly what was kept.
func compactionCardLines(c event.Compaction) []string {
	trigger := c.Trigger
	switch c.Trigger {
	case "auto":
		trigger = i18n.M.CompactionAuto
	case "manual":
		trigger = i18n.M.CompactionManual
	}
	header := fmt.Sprintf("%s · %d %s · %s", i18n.M.CompactionTitle, c.Messages, i18n.M.CompactionUnit, trigger)
	lines := []string{accent("◆ " + header)}
	for _, ln := range strings.Split(strings.TrimRight(c.Summary, "\n"), "\n") {
		lines = append(lines, dim("  │ "+ln))
	}
	if c.Archive != "" {
		lines = append(lines, dim("  │ archived "+c.Archive))
	}
	return lines
}

// contextTag renders the prompt-vs-context-window gauge for the status line,
// framed around the auto-compaction threshold: it shows how much headroom is
// left until the next compaction, and colours by proximity to that point rather
// than the raw window. Falls back to a plain percentage when compaction is disabled.
func (m chatTUI) contextTag() string {
	used, window := m.ctrl.ContextSnapshot()
	if used == 0 || window == 0 {
		return ""
	}
	pct := used * 100 / window
	ratio := m.ctrl.CompactRatio()
	if ratio <= 0 || ratio >= 1 {
		// Compaction disabled: just the raw gauge, coloured on window fill.
		body := fmt.Sprintf("%s / %s ctx (%d%%)", shortTokens(used), shortTokens(window), pct)
		switch {
		case pct >= 85:
			return themeStyle(activeCLITheme.danger).Render(body)
		case pct >= 60:
			return themeStyle(activeCLITheme.warn).Render(body)
		default:
			return dim(body)
		}
	}
	threshold := int(ratio * 100)
	// Headroom to the compaction point, as a percentage of the window (clamped at 0).
	left := threshold - pct
	if left < 0 {
		left = 0
	}
	body := fmt.Sprintf("%s ctx (%d%%) · %d%% to compact", shortTokens(used), pct, left)
	switch {
	case pct >= threshold:
		return themeStyle(activeCLITheme.danger).Render(fmt.Sprintf("%s ctx (%d%%) · compacting soon", shortTokens(used), pct))
	case left <= 10:
		return themeStyle(activeCLITheme.warn).Render(body)
	default:
		return dim(body)
	}
}

func cacheRateLabel(format string, hit, denom int) string {
	if denom <= 0 {
		return ""
	}
	return fmt.Sprintf(format, fmt.Sprintf("%.2f%%", float64(hit)*100/float64(denom)))
}

// cacheTag renders both prompt cache-hit rates for the status line —
// "turn hit 88.00% · avg 78.00%": the single-turn rate (latest turn, the higher/steeper
// number on a non-compacting DeepSeek session) and the session-aggregate rate
// Σhit/Σ(hit+miss) (the steadier, cost-oriented number that matches the legacy
// dashboard). "" before any cache tokens have been reported.
func (m chatTUI) cacheTag() string {
	now := ""
	if u := m.ctrl.LastUsage(); u != nil {
		// Never invent a denominator from PromptTokens alone — that shows a
		// fake "0% hit" when the provider simply omitted cache fields.
		d := u.CacheHitTokens + u.CacheMissTokens
		if d > 0 {
			now = cacheRateLabel(i18n.M.ChatStatusCacheNowFmt, u.CacheHitTokens, d)
		}
	}
	avg := ""
	if hit, miss, ok := m.ctrl.SessionCacheRate(); ok {
		avg = cacheRateLabel(i18n.M.ChatStatusCacheAvgFmt, hit, hit+miss)
	}
	switch {
	case now != "" && avg != "":
		return dim(now + " · " + avg)
	case now != "":
		return dim(now)
	case avg != "":
		return dim(avg)
	}
	return ""
}

func (m chatTUI) modelTag() string {
	if strings.TrimSpace(m.label) == "" {
		return ""
	}
	return dim(m.label)
}

// sessionCostTag returns the cumulative conversation cost in CNY, e.g. "¥0.1234".
// Empty string when no cost has been accumulated yet.
func (m chatTUI) sessionCostTag() string {
	if m.sessCost <= 0 {
		return ""
	}
	s := fmt.Sprintf("%.4f", m.sessCost)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	// Session totals are always CNY after CostInCNY accumulation.
	return dim("¥" + s)
}

func (m chatTUI) effortTag() string {
	if m.effortLevel == "" {
		return ""
	}
	body := "effort " + m.effortLevel
	if m.effortLevel != "auto" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#2563eb")).Bold(true).Render(body)
	}
	return dim(body)
}

// shortTokens prints token counts compactly: 1_500 → "1.5K", 142_000 → "142.0K", 1_000_000 → "1.0M".
func shortTokens(n int) string {
	switch {
	case n >= 999_950:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// renderApprovalBanner is the slim notice shown above the input while a tool
// call (or a plan) awaits the user's decision.
func (m chatTUI) renderApprovalBanner() string {
	w := m.width
	if w < 10 {
		w = 10
	}
	if m.pendingApproval == nil {
		return ""
	}
	// Task-scope approval gets a simple two-button prompt.
	if m.pendingApproval.Scope == "task" {
		name, detail := approvalToolDetails(m.pendingApproval.Tool)
		preview := strings.TrimSpace(m.pendingApproval.Preview)
		banner := "⏸ 子代理任务审批\n\n将委派子代理执行任务。"
		if name != "" {
			banner += "\n工具: " + name
		}
		if preview != "" {
			banner += "\n目的：" + preview
		}
		if detail != "" {
			banner += "\n" + detail
		}
		banner += "\n\n[Enter] 批准  [Esc] 拒绝"
		return approvalBannerStyle.Width(w).Render(banner)
	}
	name, detail := approvalToolDetails(m.pendingApproval.Tool)
	subj := strings.TrimSpace(m.pendingApproval.Subject)
	preview := strings.TrimSpace(m.pendingApproval.Preview)
	// Show Preview when available, fall back to Subject.
	var extra string
	if preview != "" {
		extra = " " + preview
	} else if subj != "" {
		extra = " " + truncateSubject(subj, w)
	}
	text := fmt.Sprintf(i18n.M.ToolApprovalPromptFmt, name, extra, detail)
	return approvalBannerStyle.Width(w).Render("⏸ " + text)
}

// approvalToolDetails turns provider-visible tool IDs into user-facing labels.
// MCP tools are advertised as mcp_<server>__<tool>; showing the short tool name
// first keeps the approval prompt readable while preserving the source.
func approvalToolDetails(toolName string) (name, detail string) {
	if server, short, ok := tool.SplitMCPName(toolName); ok {
		lines := []string{}
		if strings.EqualFold(short, "understand_image") {
			lines = append(lines, i18n.M.ToolApprovalImageUse)
		}
		lines = append(lines, fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, server))
		return short, strings.Join(lines, "\n")
	}
	return toolName, fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, i18n.M.ToolApprovalBuiltIn)
}

func marshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// truncateSubject trims a tool subject so the approval banner fits one line.
func truncateSubject(s string, width int) string {
	max := width - 28
	if max < 16 {
		max = 16
	}
	return ansi.Truncate(s, max, "…")
}

// clampStatusLine truncates a status line to `width` visible columns, ANSI-aware,
// appending an ellipsis and a reset. The bottom region must stay a fixed height —
// the non-alt-screen renderer commits scrollback by clearing the prior frame's
// lines, so a status that wraps to a second row strands input-box borders in
// history. Truncating (not wrapping) keeps it one row regardless of how many tags
// (ctx · cache · avg · jobs · balance) it carries on a narrow terminal.
func clampStatusLine(s string, width int) string {
	// ansi.Truncate is ANSI-aware, counts wide chars, and appends the tail when
	// it actually clips — one row regardless of how many tags the status carries.
	return ansi.Truncate(s, width, "…")
}

// growInputToFit resizes the textarea to the number of lines its value spans,
// capped at maxInputRows so a long paste doesn't crowd the screen.
const maxInputRows = 5
const foldedPasteMinChars = 1000
const foldedPasteMinLines = 5

type pastedBlock struct {
	label string
	text  string
	image bool // an image attachment: expands to its bare @ref, not a wrapped block
}

func pastedLineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), "\n") + 1
}

func foldedPasteLabel(id, lines int) string {
	return fmt.Sprintf("[Pasted text #%d · %d lines]", id, lines)
}

func renderFoldedPasteBlock(block pastedBlock) string {
	return fmt.Sprintf("%s\n\n--- Begin %s ---\n%s\n--- End %s ---", block.label, block.label, block.text, block.label)
}

func shouldFoldPastedText(s string) bool {
	return len([]rune(s)) >= foldedPasteMinChars || pastedLineCount(s) >= foldedPasteMinLines
}

func (m *chatTUI) chooserTyping() bool {
	return m.chooser != nil && m.chooser.typing
}

func (m *chatTUI) shouldFoldPaste(s string) bool {
	return shouldFoldPastedText(s)
}

func (m *chatTUI) insertFoldedPaste(s string) {
	label := foldedPasteLabel(m.nextPasteID, pastedLineCount(s))
	m.nextPasteID++
	m.pastedBlocks = append(m.pastedBlocks, pastedBlock{label: label, text: s})
	m.input.InsertString(label + " ")
}

// insertImageRef puts a deletable [image #N] token in the input box (mapped to
// the saved attachment's @ref, expanded on submit) so a dragged/pasted image is
// edited and removed like any other text, not stranded in a separate tray.
func (m *chatTUI) insertImageRef(path string) {
	label := fmt.Sprintf("[image #%d]", m.nextPasteID)
	m.nextPasteID++
	m.pastedBlocks = append(m.pastedBlocks, pastedBlock{label: label, text: "@" + path, image: true})
	m.input.InsertString(label + " ")
	m.growInputToFit()
	m.updateCompletion()
}

func (m *chatTUI) expandPastedBlocks(displayed string) string {
	sent := displayed
	for _, block := range m.pastedBlocks {
		if !strings.Contains(sent, block.label) {
			continue
		}
		repl := renderFoldedPasteBlock(block)
		if block.image {
			repl = block.text
		}
		sent = strings.ReplaceAll(sent, block.label, repl)
	}
	return sent
}

func (m *chatTUI) pasteLabelsIn(s string) []string {
	var labels []string
	for _, block := range m.pastedBlocks {
		if strings.Contains(s, block.label) {
			labels = append(labels, block.label)
		}
	}
	return labels
}

func (m *chatTUI) clearSubmittedPastes() {
	if len(m.pendingPastes) == 0 {
		return
	}
	submitted := make(map[string]bool, len(m.pendingPastes))
	for _, label := range m.pendingPastes {
		submitted[label] = true
	}
	kept := m.pastedBlocks[:0]
	for _, block := range m.pastedBlocks {
		if !submitted[block.label] {
			kept = append(kept, block)
		}
	}
	m.pastedBlocks = kept
	m.pendingPastes = nil
}

func (m *chatTUI) growInputToFit() {
	if m.input.DynamicHeight {
		return
	}
	lines := strings.Count(m.input.Value(), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputRows {
		lines = maxInputRows
	}
	if lines != m.input.Height() {
		m.input.SetHeight(lines)
	}
}

func pasteClipboardImage() tea.Cmd {
	return func() tea.Msg {
		path, err := control.SaveClipboardImage()
		return clipboardImageMsg{path: path, err: err}
	}
}

func pasteClipboard() tea.Cmd {
	return func() tea.Msg {
		path, imageErr := control.SaveClipboardImage()
		if imageErr == nil {
			return clipboardPasteMsg{path: path}
		}
		text, textErr := clipboard.ReadAll()
		if textErr == nil && text != "" {
			return clipboardPasteMsg{text: text}
		}
		if textErr != nil {
			return clipboardPasteMsg{err: fmt.Errorf("%v; text paste failed: %w", imageErr, textErr)}
		}
		return clipboardPasteMsg{err: imageErr}
	}
}

func (m *chatTUI) attachPastedImages(text string) bool {
	sources, ok := pastedImageSources(text)
	if !ok {
		return false
	}
	for _, src := range sources {
		path, err := savePastedImageSource(src)
		if err != nil {
			m.notice("paste image: " + err.Error())
			continue
		}
		m.insertImageRef(path)
	}
	return true
}

var markdownImageSourceRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

func pastedImageSources(text string) ([]string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	if isDataImage(trimmed) {
		return []string{trimmed}, true
	}
	if matches := markdownImageSourceRe.FindAllStringSubmatch(trimmed, -1); len(matches) > 0 {
		rest := strings.TrimSpace(markdownImageSourceRe.ReplaceAllString(trimmed, ""))
		if rest == "" {
			sources := make([]string, 0, len(matches))
			for _, m := range matches {
				sources = append(sources, m[1])
			}
			return sources, true
		}
	}

	lines := nonEmptyPasteLines(trimmed)
	if len(lines) > 0 && allImageSources(lines) {
		return lines, true
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 1 && allImageSources(fields) {
		return fields, true
	}
	return nil, false
}

func nonEmptyPasteLines(text string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func allImageSources(sources []string) bool {
	if len(sources) == 0 {
		return false
	}
	for _, src := range sources {
		if !looksLikeImageSource(src) {
			return false
		}
	}
	return true
}

func looksLikeImageSource(src string) bool {
	if isDataImage(strings.TrimSpace(src)) {
		return true
	}
	path, ok := pastedImagePath(src)
	if !ok {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func savePastedImageSource(src string) (string, error) {
	src = strings.TrimSpace(src)
	if isDataImage(src) {
		return control.SaveImageDataURL(src)
	}
	path, ok := pastedImagePath(src)
	if !ok {
		return "", fmt.Errorf("unsupported pasted image source")
	}
	return control.SaveImageFile(path)
}

func isDataImage(src string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(src)), "data:image/")
}

func pastedImagePath(src string) (string, bool) {
	src = strings.TrimSpace(src)
	src = strings.TrimPrefix(src, "@")
	quoted := (strings.HasPrefix(src, `"`) && strings.HasSuffix(src, `"`)) || (strings.HasPrefix(src, `'`) && strings.HasSuffix(src, `'`))
	src = strings.Trim(src, "\"'")
	if src == "" {
		return "", false
	}
	if !quoted && strings.ContainsAny(src, " \t\r\n") {
		return "", false
	}
	lower := strings.ToLower(src)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "", false
	}
	if strings.HasPrefix(lower, "file://") {
		u, err := url.Parse(src)
		if err != nil || u.Path == "" {
			return "", false
		}
		src = u.Path
	}
	if strings.HasPrefix(src, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			src = filepath.Join(home, strings.TrimPrefix(src, "~/"))
		}
	}
	return filepath.Clean(src), true
}

// pastedFileRef turns a dragged/pasted non-image file path into an @reference so
// it attaches instead of landing as literal text (and, for a POSIX path, being
// misread as a slash command). Images are handled earlier; only path-shaped
// content (a separator) that points at a real file qualifies, so an ordinary
// pasted word is left alone.
func pastedFileRef(content string) (string, bool) {
	path, ok := pastedImagePath(content)
	if !ok || !strings.ContainsAny(path, `/\`) {
		return "", false
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", false
	}
	return "@" + path, true
}

func (m *chatTUI) toggleVerboseReasoning(notify bool) {
	m.showReasoning = !m.showReasoning
	if !notify {
		return
	}
	if m.showReasoning {
		m.notice("verbose on — thinking text will be shown")
	} else {
		m.notice("verbose off — thinking text will stay collapsed")
	}
}

// startTurn commits the user bubble to scrollback, resets the turn accumulator,
// and kicks off the controller turn. `sent` goes to the model uncomposed (the
// controller frames it with any plan marker); `displayed` is what the transcript
// shows, and `restore` is what Esc puts back while the bubble is still deferred.
func (m *chatTUI) startTurn(sent, displayed, restore string) tea.Cmd {
	return m.startTurnWithRaw(sent, displayed, restore, sent)
}

// startTurnWithRaw is startTurn plus an explicit `raw` (the un-resolved user
// prompt), so resolved @-reference payloads can't inflate the complexity signal.
func (m *chatTUI) startTurnWithRaw(sent, displayed, restore, raw string) tea.Cmd {
	// Flush any half-streamed leftover before the new turn (defensive).
	m.commitReasoning()
	m.commitPending()

	// Echo the user bubble to scrollback now so it appears the instant Enter is
	// pressed, not when the server's first packet lands. It stays un-sendable until
	// then: Esc before the reply pops these lines back off (unsendPending) and
	// restores the text to the input box, leaving nothing stranded.
	m.pendingRestore = restore
	m.pendingPastes = m.pasteLabelsIn(restore)
	m.bubbleStartIdx = len(m.transcript)
	m.commitLine("") // blank line separating turns
	m.commitLine(renderUserBubble(displayed, m.width))
	m.bubblePending = true
	m.turnDiscarded = false

	m.state = tuiRunning
	m.runStart = time.Now()
	m.elapsed = 0
	m.turnTokens = 0
	// The controller owns the run goroutine, its context, and cancellation; it
	// streams events to eventCh and emits TurnDone when the turn settles.
	m.ctrl.SendWithRaw(sent, raw)
	return tea.Batch(m.spinner.Tick, elapsedTick())
}

// confirmBubbleSent marks the already-echoed user bubble as really sent once a
// turn's first response packet arrives, so Esc no longer un-sends it (it cancels
// the stream instead). Also called defensively at turn end. A no-op once confirmed.
func (m *chatTUI) confirmBubbleSent() {
	if !m.bubblePending {
		return
	}
	m.bubblePending = false
	m.pendingRestore = ""
}

// unsendPending "un-sends" the in-flight turn while the server hasn't replied yet
// (bubblePending): it pops the echoed bubble back off the transcript, restores the
// just-sent text to the input box, and cancels the request — marking the turn
// discarded so its already-buffered events reach nothing. Once a packet has arrived
// the bubble is confirmed and this path isn't taken (Esc cancels normally instead).
func (m *chatTUI) unsendPending() {
	m.input.SetValue(m.pendingRestore)
	m.growInputToFit()
	m.transcript = m.transcript[:m.bubbleStartIdx]
	m.transcriptDirty = true
	m.bubblePending = false
	m.pendingRestore = ""
	m.pendingPastes = nil
	m.turnDiscarded = true
	m.ctrl.Cancel()
}

// ingestEvent routes one typed event from the agent. Reasoning (dim) and answer
// free-text accumulate in their live buffers; every other event first finalizes
// the reasoning and answer streamed so far, then commits its own line —
// preserving order. Switching on the event Kind replaces the old prefix-sniffing
// of a flattened byte stream: the structure is now explicit.
func (m *chatTUI) ingestEvent(e event.Event) {
	if e.Kind == event.Retrying {
		m.retryAttempt = e.RetryAttempt
		m.retryMax = e.RetryMax
		m.pending.Reset()
		m.answerFlushed = 0
		return
	}
	// Any other event means the connection got past the retry window (or the turn
	// ended), so the transient "retrying" indicator clears.
	m.retryAttempt = 0
	m.retryMax = 0
	if m.turnDiscarded {
		// The turn was un-sent (Esc before any packet); swallow whatever was already
		// buffered for it until it settles, so nothing lands in scrollback.
		if e.Kind == event.TurnDone {
			m.turnDiscarded = false
			m.state = tuiIdle
		}
		return
	}
	// The first packet of any kind means the server replied — confirm the send so
	// Esc cancels the stream instead of un-sending. TurnStarted is local (emitted
	// before the request) and TurnDone is handled in its own case.
	if e.Kind != event.TurnStarted && e.Kind != event.TurnDone {
		m.confirmBubbleSent()
	}
	switch e.Kind {
	case event.Reasoning:
		diag.LogHex("tui-reason", e.Text)
		if m.nativeScrollback {
			if !m.reasoningNative {
				m.thinkStart = time.Now()
				m.reasoningNative = true
			}
			m.streamReasoning(e.Text)
			break
		}
		if m.reasoningLineIdx < 0 {
			// Show the marker plus a live text block the moment thinking starts; the
			// text streams in below it and the block collapses to "thought for Ns"
			// when it closes (kept expanded only in verbose mode).
			m.commitSpacer()
			m.thinkStart = time.Now()
			m.reasoningLineIdx = len(m.transcript)
			m.commitLine(dim("  ▎ " + i18n.M.ChatThinking))
			m.reasoningTextIdx = len(m.transcript)
			m.commitLine("")
			m.reasoningView = m.reasoningView[:0]
		}
		m.streamReasoning(e.Text)

	case event.Text:
		m.commitReasoning() // reasoning ends as the answer begins
		diag.LogHex("tui-text", e.Text)
		m.pending.WriteString(e.Text)
		m.streamAnswer()

	case event.Message:
		// The answer stream is complete — freeze reasoning + the markdown answer.
		m.commitReasoning()
		m.commitPending()

	case event.ToolDispatch:
		// The early (partial) dispatch only carries the name — the full dispatch
		// with args prints the line. The running spinner covers the gap meanwhile.
		if e.Tool.Partial {
			break
		}
		m.finalizeStreamed()
		switch e.Tool.Name {
		default:
			m.commitSpacer()
			if block := diffBlock(e.Tool.Name, e.Tool.Args, e.Tool.FileDiff, m.width, diffScrollbackMaxLines); block != nil {
				for _, ln := range block {
					m.commitLine(ln)
				}
				break
			}
			m.commitLine(toolCard(e.Tool.Name, e.Tool.Args, m.width))
			m.beginToolRunning(e.Tool.ID)
		}

	case event.ToolProgress:
		m.streamToolOutput(e.Tool.ID, e.Tool.Output)

	case event.ToolResult:
		// A successful result is silent (it only feeds the model); a blocked/failed
		// call surfaces a red "⏺ Verb ⊘ <reason>" card. A live-output block (bash)
		// collapses to a one-line "⎿ N lines" summary first.
		m.collapseToolOutput(e.Tool.ID)
		if e.Tool.Err != "" {
			m.finalizeStreamed()
			prefix := "  " + red("●") + " " + bold(toolDisplayName(e.Tool.Name)) + " "
			errMsg := red("⊘ " + e.Tool.Err)
			prefixW := ansi.StringWidth(prefix)
			avail := (m.width - 1) - prefixW
			if avail < 10 {
				avail = 10
			}
			lines := strings.Split(ansi.Wrap(errMsg, avail, ""), "\n")
			var sb strings.Builder
			sb.WriteString(prefix)
			sb.WriteString(lines[0])
			pad := strings.Repeat(" ", prefixW)
			for _, l := range lines[1:] {
				sb.WriteString("\n")
				sb.WriteString(pad)
				sb.WriteString(l)
			}
			m.commitLine(sb.String())
		}

	case event.Usage:
		if e.Usage != nil {
			m.turnTokens += e.Usage.CompletionTokens
			m.sessCost = e.SessionCost
			m.sessCurrency = e.SessionCurrency
		}
		if line := agent.FormatUsageLine(e.Usage, e.Pricing, e.CacheDiagnostics); line != "" {
			m.finalizeStreamed()
			if ansi.StringWidth(line) > m.width {
				prefix := "  · "
				prefixW := ansi.StringWidth(prefix)
				avail := (m.width - 1) - prefixW
				if avail < 10 {
					avail = 10
				}
				rest := line[len(prefix):]
				wrapped := strings.Split(ansi.Wrap(rest, avail, ""), "\n")
				var sb strings.Builder
				sb.WriteString(prefix)
				sb.WriteString(wrapped[0])
				pad := strings.Repeat(" ", prefixW)
				for _, l := range wrapped[1:] {
					sb.WriteString("\n")
					sb.WriteString(pad)
					sb.WriteString(l)
				}
				m.commitLine(sb.String())
			} else {
				m.commitLine(line)
			}
		}

	case event.Notice:
		glyph := "·"
		if e.Level == event.LevelWarn {
			glyph = "!"
		}
		m.finalizeStreamed()
		prefix := fmt.Sprintf("  %s ", glyph)
		prefixW := ansi.StringWidth(prefix)
		avail := (m.width - 1) - prefixW
		if avail < 10 {
			avail = 10
		}
		lines := strings.Split(ansi.Wrap(e.Text, avail, ""), "\n")
		var sb strings.Builder
		sb.WriteString(prefix)
		sb.WriteString(lines[0])
		pad := strings.Repeat(" ", prefixW)
		for _, l := range lines[1:] {
			sb.WriteString("\n")
			sb.WriteString(pad)
			sb.WriteString(l)
		}
		m.commitLine(sb.String())

	case event.CompactionStarted:
		m.finalizeStreamed()
		m.commitLine(dim("  ⋯ " + i18n.M.CompactionWorking))

	case event.CompactionDone:
		// An aborted pass carries no summary; the accompanying Notice (auto) or
		// compactDoneMsg error (manual) explains why, so don't draw an empty card.
		if e.Compaction.Summary == "" {
			break
		}
		m.finalizeStreamed()
		for _, ln := range compactionCardLines(e.Compaction) {
			m.commitLine(ln)
		}

	case event.Phase:
		m.finalizeStreamed()
		m.commitLine(fmt.Sprintf("[%s]", e.Text))

	case event.ApprovalRequest:
		// The controller's run goroutine is now blocked inside the gate awaiting
		// this decision; the banner shows it in View and key input answers it via
		// ctrl.Approve. At most one prompt is outstanding (the controller
		// serialises them), so a plain field holds the current one.
		a := e.Approval
		m.pendingApproval = &a

	case event.AskRequest:
		// The `ask` tool raised a question card; the run goroutine blocks until
		// ctrl.AnswerQuestion resolves it. Keys drive the card while it's set.
		m.finalizeStreamed()
		m.chooser = newChooser(e.Ask)

	case event.MCPSurfaceReady:
		if m.ctrl != nil {
			m.host = m.ctrl.Host()
		}
		m.refreshMCPManager()

	case event.TurnDone:
		// The turn settled — freeze anything still streaming, surface a real error,
		// and gate a plan-mode proposal on the user's approval. Autosave already
		// happened in Controller so every frontend shares the same activity-time
		// semantics.
		m.commitReasoning()
		m.commitPending()
		// The bubble was echoed on Enter and an un-sent turn is swallowed above
		// (turnDiscarded), so any turn reaching here keeps its bubble in scrollback;
		// just clear the un-sendable flag.
		m.confirmBubbleSent()
		m.pendingApproval = nil // defensive: turn settled, drop stale approval banner
		m.state = tuiIdle
		m.queueEditCursor = -1
		m.queueEditDraft = ""
		m.clearSubmittedPastes()
		if e.Err != nil && e.Err.Error() != "" && !strings.Contains(e.Err.Error(), "context canceled") {
			m.commitLine(wrapForViewport(i18n.M.ErrorPrefix+" "+e.Err.Error(), m.width, activeCLITheme.warn))
		}
		// Plan-mode approval is now driven by the controller (it emits an
		// ApprovalRequest when a plan-mode turn produces a proposal), so there's
		// nothing to detect here.
	}
}

// finalizeStreamed freezes any in-progress reasoning + answer into scrollback so
// a following event line lands after them, preserving chronological order.
func (m *chatTUI) finalizeStreamed() {
	m.collapseToolOutput(m.toolStreamID)
	m.commitReasoning()
	m.commitPending()
}

func waitForAgentEvent(ch chan event.Event) tea.Cmd {
	return func() tea.Msg { return agentEventMsg(<-ch) }
}

func elapsedTick() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return elapsedTickMsg{} })
}

// runSlashCommand handles "/<cmd> <args>" input. Local commands queue their
// output to scrollback; MCP prompt / custom commands resolve to a model turn.
func (m *chatTUI) runSlashCommand(input string) tea.Cmd {
	cmd := strings.TrimSpace(strings.SplitN(input, " ", 2)[0])

	if strings.HasPrefix(cmd, "/mcp_") {
		return m.runMCPPrompt(input)
	}

	switch cmd {
	case "/compact":
		m.echoLocalCommand(input)
		// Compaction makes a (network) summarizer call; run it off the Update loop
		// so the TUI doesn't freeze. The CompactionStarted/Done events render the
		// card as they arrive; compactDoneMsg only handles the terminal error /
		// snapshot once the pass returns. Any text after "/compact" is focus
		// guidance steering what the summary keeps.
		focus := strings.TrimSpace(strings.TrimPrefix(input, cmd))
		return func() tea.Msg { return compactDoneMsg{err: m.ctrl.Compact(context.Background(), focus)} }
	case "/new":
		m.echoLocalCommand(input)
		if err := m.ctrl.NewSession(); err != nil {
			m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashNewFailed, err))
			return nil
		}
		// Native scrollback keeps the old transcript; mark the fork with a fresh
		// banner and reset live state.
		m.pending.Reset()
		m.reasoning.Reset()
		m.chooser = nil
		// Pull fresh cost from the new session (controller.ResetSessionCost already zeroed it).
		m.sessCost, m.sessCurrency = m.ctrl.SessionCost()
		m.commitLine("")
		m.commitLine(strings.TrimRight(renderTUIBanner(m.label, "", m.width), "\n"))
		m.notice(i18n.M.SlashNewDone)
	case "/resume":
		m.runResumeCommand(input)
	case "/verbose":
		m.toggleVerboseReasoning(true)
	case "/effort":
		return m.runEffortCommand(input)
	case "/rewind":
		m.echoLocalCommand(input)
		m.openRewind()
	case "/tree":
		m.echoLocalCommand(input)
		m.showBranchTree()
	case "/branch":
		m.echoLocalCommand(input)
		m.runBranchCommand(input)
	case "/switch":
		m.echoLocalCommand(input)
		m.runSwitchCommand(input)
	case "/mcp":
		m.echoLocalCommand(input)
		m.runMCPSubcommand(input)
	case "/model":
		m.echoLocalCommand(input)
		m.runModelSubcommand(input)
		if m.pendingModelSwitch != nil {
			return m.pendingModelSwitch
		}
	case "/skill", "/skills":
		m.echoLocalCommand(input)
		m.runSkillSubcommand(input)
		if m.pendingModelSwitch != nil {
			return m.pendingModelSwitch
		}
	case "/hooks":
		m.echoLocalCommand(input)
		m.runHooksSubcommand(input)
	case "/paste-image":
		return pasteClipboardImage()
	case "/output-style", "/output-styles":
		m.echoLocalCommand(input)
		styles := outputstyle.List(outputstyle.Dirs())
		if len(styles) == 0 {
			m.notice(i18n.M.OutputStyleNone)
		} else {
			m.commitLine(renderOutputStyles(m.width, styles, m.outputStyle))
		}
	case "/theme":
		m.echoLocalCommand(input)
		m.runThemeSubcommand(input)
	case "/language":
		m.echoLocalCommand(input)
		m.runLanguageSubcommand(input)
	case "/help":
		m.echoLocalCommand(input)
		m.showHelp()
	case "/memory":
		m.echoLocalCommand(input)
		m.showMemory()
	case "/remember":
		note := strings.TrimSpace(strings.TrimPrefix(input, cmd))
		if note == "" {
			m.notice("nothing to remember")
		} else if path, err := m.ctrl.QuickAdd(memory.ScopeProject, note); err != nil {
			m.notice("memory: " + err.Error())
		} else {
			m.notice("remembered → " + path)
		}
	case "/quit", "/exit":
		return tea.Quit
	case "/forget":
		m.forgetMemory(strings.TrimSpace(strings.TrimPrefix(input, cmd)))
	default:
		// A custom command wins over a skill of the same name; both resolve to a turn.
		if sent, ok := m.ctrl.CustomCommand(input); ok {
			return m.startTurn(sent, input, input)
		}
		if sent, ok := m.ctrl.RunSkill(input); ok {
			return m.startTurn(sent, input, input)
		}
		m.notice(fmt.Sprintf("%s: %s", i18n.M.SlashUnknown, cmd))
	}
	return nil
}

func (m *chatTUI) echoLocalCommand(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	m.commitLine(dim("  › " + input))
}

// commandNames renders the custom command list for /help, "" when there are none.
func (m *chatTUI) commandNames() string {
	if len(m.commands) == 0 {
		return ""
	}
	names := make([]string, len(m.commands))
	for i, c := range m.commands {
		names[i] = "/" + c.Name
	}
	return strings.Join(names, " · ")
}

// runMCPSubcommand handles "/mcp" (status), "/mcp add …" (connect a server live
// and persist it), and "/mcp remove <name>" (disconnect + drop from config). Add
// connects synchronously — like /compact, an explicit command may briefly block
// the UI while the handshake runs.
func (m *chatTUI) runMCPSubcommand(input string) {
	args := tokenizeArgs(input) // args[0] == "/mcp"
	if len(args) < 2 {
		m.openMCPManager("")
		return
	}
	switch args[1] {
	case "list", "ls":
		// The completion menu offers "list"; treat it as the status view (same as
		// the legacy /mcp output) rather than an unknown subcommand.
		m.showMCPStatus()
	case "show":
		if len(args) < 3 {
			m.notice("usage: /mcp show <name>")
			return
		}
		m.openMCPManager(args[2])
	case "tools":
		if len(args) < 3 {
			m.notice("usage: /mcp tools <name>")
			return
		}
		m.openMCPManager(args[2])
		if m.mcp != nil {
			m.mcp.stage = mcpStageTools
		}
	case "add":
		entry, err := parseMCPAdd(args[2:])
		if err != nil {
			m.notice(err.Error())
			return
		}
		n, err := m.ctrl.AddMCPServer(entry)
		if err != nil {
			m.notice("mcp add: " + err.Error())
			return
		}
		m.notice(fmt.Sprintf("connected %s — %d tools, saved to config (available next message)", entry.Name, n))
	case "connect":
		if len(args) < 3 {
			m.notice("usage: /mcp connect <name>")
			return
		}
		n, err := m.ctrl.ConnectConfiguredMCPServer(args[2])
		if err != nil {
			m.notice("mcp connect: " + err.Error())
			return
		}
		m.host = m.ctrl.Host()
		m.notice(fmt.Sprintf("connected %s — %d tools (available next message)", args[2], n))
	case "remove", "rm":
		if len(args) < 3 {
			m.notice("usage: /mcp remove <name>")
			return
		}
		name := args[2]
		disconnected, err := m.ctrl.RemoveMCPServer(name)
		if err != nil {
			m.notice("mcp remove: " + err.Error())
			return
		}
		if disconnected {
			m.notice("disconnected " + name + " and removed it from config")
		} else {
			m.notice("removed " + name + " from config")
		}
	case "import":
		m.openMCPImportPicker()
	default:
		m.notice("unknown /mcp subcommand " + args[1] + " — try: /mcp, /mcp list, /mcp show, /mcp add, /mcp connect, /mcp import, /mcp remove")
	}
}

// showMCPStatus queues the connected MCP servers, their counts, and the prompt
// commands / resource refs they expose — the discovery surface for /mcp.
func (m *chatTUI) showMCPStatus() {
	if m.host == nil || (len(m.host.Servers()) == 0 && len(m.host.Failures()) == 0) {
		m.notice(i18n.M.SlashMCPNone)
		return
	}
	m.commitLine(renderMCPStatus(m.width, m.host.Servers(), m.host.Prompts(), m.host.Resources(), m.host.Failures()))
}

// notice queues a dim informational line to scrollback.
func (m *chatTUI) notice(note string) {
	m.commitLine(dim("  · " + note))
}

// resolveRefs resolves a line's @references off the event loop via the
// controller, delivering a refsResolvedMsg with the tagged context block.
func (m *chatTUI) resolveRefs(sent, display, restore string) tea.Cmd {
	return func() tea.Msg {
		block, errs := m.ctrl.ResolveRefs(context.Background(), sent)
		return refsResolvedMsg{sent: sent, display: display, restore: restore, block: block, errs: errs}
	}
}

// runMCPPrompt resolves a /mcp_server_prompt command off the event loop via
// the controller, delivering a promptResolvedMsg with the rendered prompt.
func (m *chatTUI) runMCPPrompt(input string) tea.Cmd {
	return func() tea.Msg {
		sent, found, err := m.ctrl.MCPPrompt(context.Background(), input)
		if !found {
			name := strings.TrimPrefix(strings.Fields(input)[0], "/")
			return promptResolvedMsg{display: input, err: fmt.Errorf("%s: /%s", i18n.M.SlashUnknown, name)}
		}
		return promptResolvedMsg{display: input, sent: sent, err: err}
	}
}

// replaySectionsFor turns a loaded session into scrollback blocks: user bubbles
// and assistant markdown. Tool messages are dropped — needed in session state
// but noise in the visible transcript on resume.
func replaySectionsFor(history []provider.Message, width int, renderer *mdRenderer) []string {
	var out []string
	for _, m := range history {
		switch m.Role {
		case provider.RoleUser:
			out = append(out, renderUserBubble(m.Content, width)+"\n\n")
		case provider.RoleAssistant:
			body := strings.TrimSpace(m.Content)
			if body == "" {
				continue
			}
			rendered := renderer.Render(body)
			if rendered == "" {
				rendered = body
			}
			out = append(out, rendered+"\n")
		}
	}
	return out
}

// renderTUIBanner is the title + tip + optional missing-key warning printed once
// at the top of the session.
func renderTUIBanner(label, missing string, width int) string {
	var b strings.Builder
	b.WriteString(accent("◆") + " " + bold("reasonix chat") + "  " + dim("· "+label) + "\n")
	b.WriteString(dim("  "+i18n.M.ChatTip) + "\n")
	if missing != "" {
		b.WriteString(wrapForViewport("  ! "+missing, width, activeCLITheme.warn) + "\n")
	}
	return b.String()
}

// wrapForViewport hard-wraps text to fit width columns and colours every line.
func wrapForViewport(text string, width int, fg cliColor) string {
	if width <= 0 {
		width = 80
	}
	return themeStyle(fg).Width(width).Render(text)
}

// renderUserBubble renders the just-submitted prompt as a transcript line. Keep
// it visually lighter than the real bottom composer so a fresh session does not
// look like it has a second input box in the transcript. Long lines are
// word-wrapped to fit width, and continuation lines align under the "›" prefix.
func renderUserBubble(line string, width int) string {
	line = displayLineForImageRefs(line)
	if !colorEnabled {
		lines := strings.Split(line, "\n")
		if len(lines) == 1 {
			return "│ › " + line
		}
		var sb strings.Builder
		sb.WriteString("│ › " + lines[0])
		indent := "    "
		for _, ln := range lines[1:] {
			sb.WriteString("\n" + indent + ln)
		}
		return sb.String()
	}
	// Available width for content: reserve 4 cols for "  › " + 1 for scrollbar.
	avail := width - 1 - 4
	if avail < 1 {
		avail = 1
	}
	wrapped := ansi.Hardwrap(line, avail, true)
	return userBlock(wrapped)
}

var cliImageRefRe = regexp.MustCompile(`(?:^|\s)@\.reasonix/attachments/clipboard-\d{8}-\d{6}\.\d+(?:-(?:\d{6}|[a-f0-9]{8}))?\.(?:png|jpg|jpeg|gif|webp)`)

func displayLineForImageRefs(line string) string {
	idx := 0
	out := cliImageRefRe.ReplaceAllStringFunc(line, func(_ string) string {
		idx++
		return " [image" + strconv.Itoa(idx) + "]"
	})
	return strings.TrimSpace(out)
}

// eventSink is the event.Sink the agent emits to in TUI mode. Each event
// becomes an agentEventMsg. The channel is generously buffered so streaming
// bursts don't back-pressure the agent goroutine.
type eventSink struct {
	ch chan<- event.Event
}

func (s *eventSink) Emit(e event.Event) {
	defer func() {
		if recover() != nil {
			slog.Warn("event sink: channel closed, dropping event", "kind", e.Kind)
		}
	}()
	select {
	case s.ch <- e:
	default:
		slog.Warn("event sink: channel full, dropping event", "kind", e.Kind)
	}
}
