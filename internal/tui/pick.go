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
//
// confirm, when non-empty, makes enter ask before choosing: it is a
// fmt template whose single %s receives the highlighted item's short id
// (e.g. "restore the whole tree to %s? y/n"); y chooses, anything else
// returns to browsing. Empty means enter chooses instantly.
func RunPick(title string, items []PickItem, preview func(id string) string, confirm string) (id string, aborted bool, err error) {
	m := pickModel{title: title, items: items, preview: preview, confirm: confirm, cache: map[int]string{}}
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
	confirm string
	cache   map[int]string

	cursor        int
	chosen        int
	offset        int // preview scroll, in lines
	confirming    bool
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
		if m.confirming {
			// Only an explicit y commits; every other key is a safe no.
			switch msg.String() {
			case "y", "Y":
				m.chosen = m.cursor
				return m, tea.Quit
			case "ctrl+c":
				return m, tea.Quit
			default:
				m.confirming = false
			}
			return m, nil
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.offset = 0
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.offset = 0
			}
		case "pgdown":
			m.scroll(m.previewH() - 1)
		case "pgup":
			m.scroll(-(m.previewH() - 1))
		case "ctrl+d":
			m.scroll(m.previewH() / 2)
		case "ctrl+u":
			m.scroll(-(m.previewH() / 2))
		case "enter":
			if m.confirm != "" {
				m.confirming = true
				return m, nil
			}
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

	if previewH > 0 {
		writeClipped(&b, m.previewLines(), m.offset, previewH)
	}

	footer := fmt.Sprintf("%d/%d  ↑↓ move · pgdn/pgup scroll · enter restore · q quit", m.cursor+1, len(m.items))
	if m.confirming {
		footer = "\x1b[7m" + fmt.Sprintf(m.confirm, shortID(m.items[m.cursor].ID)) + "\x1b[0m"
	}
	b.WriteString("\n" + footer)
	return b.String()
}

// previewLines fetches (and caches) the preview under the cursor, split
// into lines — the unit both rendering and scroll clamping work in.
func (m *pickModel) previewLines() []string {
	p, ok := m.cache[m.cursor]
	if !ok {
		p = m.preview(m.items[m.cursor].ID)
		m.cache[m.cursor] = p
	}
	return strings.Split(p, "\n")
}

// previewH mirrors View's layout math so Update can clamp scrolling.
func (m *pickModel) previewH() int {
	listH := max(3, min(len(m.items), m.height/3))
	return m.height - listH - 3
}

func (m *pickModel) scroll(delta int) {
	maxOff := max(0, len(m.previewLines())-m.previewH())
	m.offset = max(0, min(m.offset+delta, maxOff))
}

func shortID(id string) string {
	if len(id) > 7 {
		return id[:7]
	}
	return id
}

func writeClipped(b *strings.Builder, lines []string, offset, maxLines int) {
	if offset > len(lines) {
		offset = len(lines)
	}
	lines = lines[offset:]
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	b.WriteString(strings.Join(lines, "\n"))
}
