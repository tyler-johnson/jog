// Package tui holds jog's terminal UI, kept deliberately thin: data comes
// in as plain values, a choice goes out. Nothing here touches git — the
// engine and CLI stay dependency-free (PLAN-V1 §2).
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// PickItem is one selectable row.
type PickItem struct {
	ID    string
	Label string
}

// RunPick shows a scrollable list with a preview pane and returns the
// chosen item's ID, or aborted=true when the user backs out (q/esc) —
// aborting must always be the safe, obvious exit.
func RunPick(title string, items []PickItem, preview func(id string) string) (id string, aborted bool, err error) {
	m := pickModel{title: title, items: items, preview: preview, cache: map[int]string{}}
	out, err := tea.NewProgram(&m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", true, err
	}
	final := out.(*pickModel)
	if final.chosen < 0 {
		return "", true, nil
	}
	return items[final.chosen].ID, false, nil
}

type pickModel struct {
	title   string
	items   []PickItem
	preview func(id string) string
	cache   map[int]string

	cursor        int
	chosen        int
	width, height int
}

func (m *pickModel) Init() tea.Cmd {
	m.chosen = -1
	return nil
}

func (m *pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = m.cursor
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *pickModel) View() string {
	if m.height == 0 {
		return ""
	}
	// Layout: title, list window (≤ third of the screen), rule, preview.
	listH := max(3, min(len(m.items), m.height/3))
	previewH := m.height - listH - 3

	var b strings.Builder
	b.WriteString(m.title + "\n")

	// List window scrolled around the cursor.
	start := max(0, min(m.cursor-listH/2, len(m.items)-listH))
	for i := start; i < start+listH && i < len(m.items); i++ {
		line := "  " + m.items[i].Label
		if i == m.cursor {
			line = "\x1b[7m> " + m.items[i].Label + "\x1b[0m"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(strings.Repeat("─", max(0, m.width)) + "\n")

	if p, ok := m.cache[m.cursor]; ok || previewH <= 0 {
		writeClipped(&b, p, previewH)
	} else {
		p = m.preview(m.items[m.cursor].ID)
		m.cache[m.cursor] = p
		writeClipped(&b, p, previewH)
	}

	b.WriteString(fmt.Sprintf("\n%d/%d  ↑↓ move · enter restore · q quit", m.cursor+1, len(m.items)))
	return b.String()
}

func writeClipped(b *strings.Builder, s string, maxLines int) {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:max(0, maxLines)]
	}
	b.WriteString(strings.Join(lines, "\n"))
}
