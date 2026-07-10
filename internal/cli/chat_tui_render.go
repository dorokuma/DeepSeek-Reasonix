package cli

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m chatTUI) renderMainManager() string {
	if card := m.renderMCPManager(); card != "" {
		return card
	}
	return m.renderSkillPicker()
}

func managerContentPanelStyle(width int) lipgloss.Style {
	return choicePanelStyle.
		Border(lipgloss.NormalBorder(), true, false, false, false).
		Width(width)
}

func managerFooterPanelStyle(width int) lipgloss.Style {
	return choicePanelStyle.
		Border(lipgloss.NormalBorder(), false, false, true, false).
		Width(width)
}

func (m chatTUI) renderMainManagerFooter() string {
	hint := ""
	switch {
	case m.mcp != nil:
		hint = m.mcp.footerHint()
	case m.skillPick != nil:
		hint = m.skillPickerFooterHint()
	}
	if strings.TrimSpace(hint) == "" {
		return ""
	}
	w := max(viewWidth(m.width), 40)
	return managerFooterPanelStyle(w).Render(dim(hint))
}

func (m chatTUI) renderTranscriptWithMainManager(card string) string {
	h := m.viewport.Height()
	if h <= 0 {
		return ""
	}
	cw := m.viewport.Width()
	if cw <= 0 {
		cw = max(m.width-1, 1)
	}

	cardLines := strings.Split(strings.TrimRight(card, "\n"), "\n")
	if len(cardLines) > h {
		cardLines = cardLines[:h]
	}
	maxTranscriptRows := h - len(cardLines)
	if maxTranscriptRows > 0 && len(cardLines) > 0 && len(m.wrappedLines) > 0 {
		maxTranscriptRows--
	}

	var rows []string
	if maxTranscriptRows > 0 {
		lines := m.wrappedLines
		start := max(0, len(lines)-maxTranscriptRows)
		rows = append(rows, lines[start:]...)
	}
	if len(rows) > 0 && len(cardLines) > 0 {
		rows = append(rows, "")
	}
	rows = append(rows, cardLines...)
	for len(rows) < h {
		rows = append(rows, "")
	}
	for i, row := range rows {
		rows[i] = padRight(ansi.Cut(row, 0, cw), cw)
	}
	return strings.Join(rows, "\n")
}

// reasoningViewMax bounds the live thinking buffer the streamed block renders
// from. Re-rendering the full chain of thought on every delta was O(n²) (a 2k-
// token thought churned ~4.7GB); rendering only the trailing window keeps each
// delta O(1). The full text still lives in m.reasoning for verbose mode.
const reasoningViewMax = 4096

// reasoningTailLines caps how many trailing visual lines the live block shows.
const reasoningTailLines = 12

// streamReasoning appends a chunk and rewrites the live reasoning block from a
// bounded trailing view (mirrors streamToolOutput), so the chain of thought is
// visible while the model works without re-rendering the whole thing per token.
