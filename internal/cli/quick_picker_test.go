package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQuickPickerNavigationFilterAndConfirm(t *testing.T) {
	p := &quickPicker{
		title: "Select model",
		items: []quickPickerItem{
			{ID: "one", Label: "Alpha"},
			{ID: "two", Label: "Beta model"},
			{ID: "three", Label: "Gamma"},
		},
	}
	p.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.selected != 1 {
		t.Fatalf("down selected = %d, want 1", p.selected)
	}
	p.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if p.selected != 0 {
		t.Fatalf("up selected = %d, want 0", p.selected)
	}
	p.handleKey(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if got := p.filteredItems(); len(got) != 1 || got[0].ID != "two" {
		t.Fatalf("filtered items = %+v, want Beta only", got)
	}
	result := p.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if result.choice == nil || result.choice.ID != "two" {
		t.Fatalf("enter choice = %+v, want two", result.choice)
	}
}

func TestQuickPickerVimKeysBecomeTextAfterSearchStarts(t *testing.T) {
	p := &quickPicker{
		selected: 1,
		items: []quickPickerItem{
			{ID: "one", Label: "Alpha"},
			{ID: "two", Label: "Beta"},
			{ID: "three", Label: "Gamma"},
		},
	}
	p.handleKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if p.selected != 0 || p.query != "" {
		t.Fatalf("empty-query k = selected %d, query %q; want navigation to 0", p.selected, p.query)
	}
	p.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if p.selected != 1 || p.query != "" {
		t.Fatalf("empty-query j = selected %d, query %q; want navigation to 1", p.selected, p.query)
	}

	p.handleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	p.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	p.handleKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if p.query != "ajk" {
		t.Fatalf("search query = %q, want ajk", p.query)
	}
}

func TestQuickPickerWindowTracksSelection(t *testing.T) {
	start, end := quickPickerWindow(20, 18)
	if start != 12 || end != 20 {
		t.Fatalf("window = [%d,%d), want [12,20)", start, end)
	}
}

func TestQuickPickerRendersWithinNarrowPanel(t *testing.T) {
	p := &quickPicker{
		title: "Select model",
		items: []quickPickerItem{{
			ID: "long", Label: strings.Repeat("very-long-model-name-", 5),
			Description: strings.Repeat("provider description ", 5), Status: "active",
		}},
	}
	out := p.render(32)
	for _, line := range strings.Split(out, "\n") {
		if got := visibleWidth(line); got > 32 {
			t.Fatalf("rendered line width = %d, want <= 32: %q", got, line)
		}
	}
}

func TestQuickPickerEscCancels(t *testing.T) {
	p := &quickPicker{items: []quickPickerItem{{ID: "one", Label: "One"}}}
	if result := p.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); !result.cancelled {
		t.Fatal("Esc should cancel picker")
	}
}
