package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCheckboxToggle: space toggles the row under the cursor, enter
// keeps the checked names in item order, and the answered view
// collapses to one line without the checkbox furniture.
func TestCheckboxToggle(t *testing.T) {
	m := checkModel{title: "install hooks?", items: []CheckItem{
		{Name: "claude", Checked: true}, {Name: "codex", Checked: true}}}
	m.Init()

	view := m.View()
	if !strings.Contains(view, "> ") || !strings.Contains(view, "claude") || !strings.Contains(view, "codex") {
		t.Errorf("open view missing cursor or rows:\n%s", view)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := strings.Join(m.chosen(), ","); got != "claude" {
		t.Errorf("chosen = %q, want claude", got)
	}
	if v := m.View(); !strings.Contains(v, "claude") || strings.Contains(v, "[") || strings.Count(v, "\n") != 1 {
		t.Errorf("answered view should be one line naming the choice:\n%q", v)
	}

	// Toggling codex back on before enter would have kept it — the
	// same key flips both ways.
	m2 := checkModel{title: "t?", items: []CheckItem{{Name: "vim"}}}
	m2.Init()
	m2.Update(tea.KeyMsg{Type: tea.KeySpace})
	if got := strings.Join(m2.chosen(), ","); got != "vim" {
		t.Errorf("toggle on: chosen = %q, want vim", got)
	}
}

// TestCheckboxBounds: the cursor clamps at both ends, and everything
// unchecked answers as "none".
func TestCheckboxBounds(t *testing.T) {
	m := checkModel{title: "t?", items: []CheckItem{
		{Name: "a", Checked: true}, {Name: "b", Checked: true}}}
	m.Init()
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.cursor != 0 {
		t.Errorf("up at top: cursor = %d, want 0", m.cursor)
	}
	for range 5 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.cursor != 1 {
		t.Errorf("down past end: cursor = %d, want 1", m.cursor)
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.chosen(); len(got) != 0 {
		t.Errorf("chosen = %v, want none", got)
	}
	if v := m.View(); !strings.Contains(v, "none") {
		t.Errorf("all-unchecked answer should read none:\n%q", v)
	}
}

// TestCheckboxAbort: esc cancels — nothing chosen, and the collapsed
// line says so.
func TestCheckboxAbort(t *testing.T) {
	m := checkModel{title: "t?", items: []CheckItem{{Name: "a", Checked: true}}}
	m.Init()
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.aborted {
		t.Fatal("esc did not abort")
	}
	if v := m.View(); !strings.Contains(v, "cancelled") {
		t.Errorf("aborted view should read cancelled:\n%q", v)
	}
}
