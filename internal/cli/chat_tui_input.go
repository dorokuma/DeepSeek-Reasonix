package cli

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"reasonix/internal/plugin"
)

func (m *chatTUI) rememberSubmittedInput(input string) {
	if strings.TrimSpace(input) == "" {
		return
	}
	if len(m.submittedInputs) == 0 || m.submittedInputs[len(m.submittedInputs)-1] != input {
		m.submittedInputs = append(m.submittedInputs, input)
	}
	m.submittedInputCursor = -1
	m.submittedInputDraft = ""
}

func (m *chatTUI) recallSubmittedInput(delta int) bool {
	if len(m.submittedInputs) == 0 {
		return false
	}
	cursor := m.submittedInputCursor
	if cursor < 0 {
		if delta > 0 {
			return false
		}
		if m.input.Line() != 0 {
			return false // first-line Up enters history; lower lines navigate the draft
		}
		m.submittedInputDraft = m.input.Value()
		cursor = len(m.submittedInputs) - 1
	} else {
		cursor += delta
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(m.submittedInputs) {
		m.submittedInputCursor = -1
		m.input.SetValue(m.submittedInputDraft)
		m.growInputToFit()
		return true
	}
	m.submittedInputCursor = cursor
	m.input.SetValue(m.submittedInputs[cursor])
	m.growInputToFit()
	return true
}

func (m *chatTUI) resetSubmittedInputRecall() {
	m.submittedInputCursor = -1
	m.submittedInputDraft = ""
}

// navigateQueue moves through the pending interject queue during tuiRunning.
// delta < 0 means ↑ (older), delta > 0 means ↓ (newer). Returns true if the
// input was updated.
func (m *chatTUI) navigateQueue(delta int) bool {
	if len(m.pendingInterject) == 0 {
		return false
	}
	cursor := m.queueEditCursor
	if cursor < 0 {
		if delta > 0 {
			return false // already at "new draft" — nothing newer
		}
		// First ↑: save the current draft and jump to the last queued item.
		m.queueEditDraft = m.input.Value()
		cursor = len(m.pendingInterject) - 1
	} else {
		cursor += delta
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(m.pendingInterject) {
		// Past the end: restore the draft the user was composing.
		m.queueEditCursor = -1
		m.input.SetValue(m.queueEditDraft)
		m.growInputToFit()
		return true
	}
	m.queueEditCursor = cursor
	m.input.SetValue(m.pendingInterject[cursor])
	m.growInputToFit()
	return true
}

// resetQueueNavigation resets the queue browsing cursor so the user returns to
// normal input mode. Any in-progress edit is discarded (the queued item keeps
// its previous value).
func (m *chatTUI) resetQueueNavigation() {
	m.queueEditCursor = -1
	m.queueEditDraft = ""
}

// renderQueueIndicator renders the pending-message queue as dim text to show
// above the input box when messages are queued during a running turn.
func (m chatTUI) renderQueueIndicator() string {
	if m.state != tuiRunning || len(m.pendingInterject) == 0 {
		return ""
	}
	queueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // dim grey
	highlightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	var lines []string
	for i, msg := range m.pendingInterject {
		preview := msg
		// Truncate long messages for the compact preview.
		runes := []rune(preview)
		if len(runes) > 50 {
			preview = string(runes[:47]) + "…"
		}
		cursor := " "
		style := queueStyle
		if m.queueEditCursor == i {
			cursor = "▸"
			style = highlightStyle
		}
		lines = append(lines, style.Render(fmt.Sprintf("  %s [%d] %s", cursor, i+1, preview)))
	}
	return strings.Join(lines, "\n")
}

// prompts returns the MCP prompts discovered at startup (nil when no plugins).
func (m *chatTUI) prompts() []plugin.Prompt {
	if m.host == nil {
		return nil
	}
	return m.host.Prompts()
}
