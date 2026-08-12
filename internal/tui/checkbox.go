package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var styleChecked = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

// CheckItem is one toggleable row in an inline checkbox prompt.
type CheckItem struct {
	Name    string
	Checked bool
}

// RunCheckboxes shows an inline, inquirer-style checkbox list — no
// altscreen, the prompt scrolls with the transcript and collapses to a
// one-line answer on enter — and returns the names left checked, in
// item order. aborted=true on q/esc/ctrl+c: the caller stops asking,
// same as EOF on a plain question.
func RunCheckboxes(title string, items []CheckItem) (chosen []string, aborted bool, err error) {
	m := checkModel{title: title, items: append([]CheckItem(nil), items...)}
	out, err := tea.NewProgram(&m).Run()
	if err != nil {
		return nil, true, err
	}
	final := out.(*checkModel)
	if final.aborted {
		return nil, true, nil
	}
	return final.chosen(), false, nil
}

type checkModel struct {
	title   string
	items   []CheckItem
	cursor  int
	done    bool
	aborted bool
}

func (m *checkModel) Init() tea.Cmd { return nil }

func (m *checkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case " ", "space":
		m.items[m.cursor].Checked = !m.items[m.cursor].Checked
	case "enter":
		m.done = true
		return m, tea.Quit
	case "q", "esc", "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	}
	return m, nil
}

// View renders the open prompt, and once answered, the compact line the
// transcript keeps — the same shape a plain question leaves behind.
func (m *checkModel) View() string {
	if m.done || m.aborted {
		answer := "cancelled"
		if m.done {
			answer = "none"
			if c := m.chosen(); len(c) > 0 {
				answer = strings.Join(c, ", ")
			}
		}
		return m.title + " " + styleFooter.Render(answer) + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", m.title, styleFooter.Render("— space toggles, enter confirms"))
	for i, it := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		box := "[ ]"
		if it.Checked {
			box = styleChecked.Render("[x]")
		}
		fmt.Fprintf(&b, "%s%s %s\n", cursor, box, it.Name)
	}
	return b.String()
}

func (m *checkModel) chosen() []string {
	var out []string
	for _, it := range m.items {
		if it.Checked {
			out = append(out, it.Name)
		}
	}
	return out
}
