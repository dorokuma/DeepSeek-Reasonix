package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	input string
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
		m.input += msg.String()
	}
	return m, nil
}

func (m model) View() tea.View {
	return tea.NewView(fmt.Sprintf("Tap anywhere to type. Press Esc to quit.\n\nYou typed: %s\n", m.input))
}

func main() {
	p := tea.NewProgram(model{})
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
