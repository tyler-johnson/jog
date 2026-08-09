// Package tui holds jog's terminal UI, kept deliberately thin: data comes
// in as plain values, a choice goes out. Nothing here touches git — the
// engine and CLI stay dependency-free (PLAN-V1 §2).
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// PickItem is one selectable row.
type PickItem struct {
	ID    string
	Label string
}

// The full-screen frame: title above, footer below, and between them two
// bordered boxes — list over preview — that together fill the window.
// Focus is the border color: the focused frame is cyan, the other dim.
var (
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleFooter = lipgloss.NewStyle().Faint(true)
	styleAsk    = lipgloss.NewStyle().Reverse(true).Bold(true)
	boxFocused  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6"))
	boxBlurred  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
)

// RunPick shows a two-frame browser — a list over a preview — and returns
// the chosen item's ID, or aborted=true when the user backs out (q) —
// aborting must always be the safe, obvious exit.
//
// One frame is focused at a time: ↑/↓ (or j/k) move the list or scroll the
// preview depending on focus, enter focuses the preview, esc returns to
// the list (and quits from it). r chooses and q quits from either frame —
// no pgup/pgdn or fn-layer keys required (they still scroll the preview
// for keyboards that have them).
//
// On short windows (SSH from a phone) the frames become separate
// full-screen views instead of a split — the same keys, but only the
// focused frame is drawn, and the diff view is headed by the selected
// row instead of the title.
//
// confirm, when non-empty, makes r ask before choosing: it is a fmt
// template whose single %s receives the highlighted item's short id
// (e.g. "restore the whole tree to %s? y/n"); y chooses, anything else
// returns to browsing. Empty means r chooses instantly.
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
	focusDiff     bool
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
			if m.focusDiff {
				m.scroll(-1)
			} else if m.cursor > 0 {
				m.cursor--
				m.offset = 0
			}
		case "down", "j":
			if m.focusDiff {
				m.scroll(1)
			} else if m.cursor < len(m.items)-1 {
				m.cursor++
				m.offset = 0
			}
		case "pgdown", "ctrl+d":
			m.scroll(m.previewH() - 1)
		case "pgup", "ctrl+u":
			m.scroll(-(m.previewH() - 1))
		case "enter":
			m.focusDiff = true
		case "esc":
			if m.focusDiff {
				m.focusDiff = false
			} else {
				return m, tea.Quit
			}
		case "r":
			if m.confirm != "" {
				m.confirming = true
				return m, nil
			}
			m.chosen = m.cursor
			return m, tea.Quit
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *pickModel) View() string {
	if m.height == 0 || m.width < 4 {
		return ""
	}
	listH, previewH := m.layout()
	inner := m.width - 2 // box content width, inside the borders

	// List window scrolled around the cursor; the cursor row is inverse
	// video padded to the full box width.
	var list []string
	start := max(0, min(m.cursor-listH/2, len(m.items)-listH))
	for i := start; i < start+listH && i < len(m.items); i++ {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		line := ansi.Truncate(strings.ReplaceAll(prefix+m.items[i].Label, "\t", " "), inner, "…")
		if i == m.cursor {
			line = "\x1b[7m" + line + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + "\x1b[0m"
		}
		list = append(list, line)
	}

	// Preview window: scrolled and clipped, every line truncated and
	// color-reset so git's ANSI can't run into the border.
	lines := m.previewLines()
	off := min(m.offset, max(0, len(lines)-previewH))
	var prev []string
	for _, l := range lines[off:min(off+previewH, len(lines))] {
		prev = append(prev, ansi.Truncate(l, inner, "…")+"\x1b[0m")
	}

	// Compact wording on narrow paged screens; hard-truncated either way
	// so a long footer can never wrap and push the layout off by a row.
	keysDiff, keysList := "↑↓ scroll · esc back to list", "↑↓ move · enter read diff"
	if m.paged() {
		keysDiff, keysList = "↑↓ · esc list", "↑↓ · enter diff"
	}
	// Truncate BEFORE styling: cutting a styled string can drop its ANSI
	// reset and bleed the style into every line below.
	var footer string
	switch {
	case m.confirming:
		footer = styleAsk.Render(ansi.Truncate(fmt.Sprintf(m.confirm, shortID(m.items[m.cursor].ID)), m.width, "…"))
	case m.focusDiff:
		footer = styleFooter.Render(ansi.Truncate(fmt.Sprintf("%d/%d  %s · r restore · q quit", m.cursor+1, len(m.items), keysDiff), m.width, "…"))
	default:
		footer = styleFooter.Render(ansi.Truncate(fmt.Sprintf("%d/%d  %s · r restore · q quit", m.cursor+1, len(m.items), keysList), m.width, "…"))
	}

	title := styleTitle.Render(ansi.Truncate(m.title, m.width, "…"))

	// Short window: one frame at a time, full screen. The diff view is
	// headed by the selected row so it stands on its own.
	if m.paged() {
		if m.focusDiff {
			header := styleTitle.Render(ansi.Truncate(strings.ReplaceAll(m.items[m.cursor].Label, "\t", " "), m.width, "…"))
			return header + "\n" +
				boxFocused.Width(inner).Height(previewH).Render(strings.Join(prev, "\n")) + "\n" +
				footer
		}
		return title + "\n" +
			boxFocused.Width(inner).Height(listH).Render(strings.Join(list, "\n")) + "\n" +
			footer
	}

	listBox, prevBox := boxFocused, boxBlurred
	if m.focusDiff {
		listBox, prevBox = boxBlurred, boxFocused
	}
	return title + "\n" +
		listBox.Width(inner).Height(listH).Render(strings.Join(list, "\n")) + "\n" +
		prevBox.Width(inner).Height(previewH).Render(strings.Join(prev, "\n")) + "\n" +
		footer
}

// paged reports whether the window is too short for the split layout —
// below the threshold each frame gets the whole screen instead. 24 rows
// is the classic full-size terminal: anything shorter (phone SSH, a
// squeezed pane) reads better one frame at a time.
func (m *pickModel) paged() bool {
	return m.height < 24
}

// layout splits the window: one line each for title and footer, two border
// rows per box, and the rest is content — the list gets ~30% of it (never
// more rows than items), the preview fills the remainder, so the two boxes
// always fill the window together. Paged, the visible frame gets all of it.
func (m *pickModel) layout() (listH, previewH int) {
	if m.paged() {
		h := max(1, m.height-4)
		return h, h
	}
	budget := max(2, m.height-6)
	listH = min(len(m.items), max(3, budget*3/10))
	listH = max(1, min(listH, budget-1))
	previewH = budget - listH
	return
}

// previewLines fetches (and caches) the preview under the cursor, split
// into lines — the unit both rendering and scroll clamping work in. Tabs
// are expanded here: the terminal renders \t as a jump to the next 8-wide
// stop, so a tab that survives to the screen makes the line wider than
// the width math says, the line wraps, and every row below it is drawn
// one row off until the next full repaint.
func (m *pickModel) previewLines() []string {
	p, ok := m.cache[m.cursor]
	if !ok {
		// \r would send the cursor to column 0 mid-line (CRLF files).
		p = strings.NewReplacer("\t", "    ", "\r", "").Replace(m.preview(m.items[m.cursor].ID))
		m.cache[m.cursor] = p
	}
	return strings.Split(p, "\n")
}

func (m *pickModel) previewH() int {
	_, previewH := m.layout()
	return previewH
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
