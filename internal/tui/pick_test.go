package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Every rendered line must fit the window's display width and the frame
// must be exactly the window height — the invariants whose violation
// wraps a line and shifts every row below it.
func TestFrameInvariants(t *testing.T) {
	items := []PickItem{
		{ID: "1111111aaaaaaa", Label: "1111111  2 minutes ago  pre: git\tstatus with a very long provenance label that keeps going"},
		{ID: "2222222bbbbbbb", Label: "2222222  9 minutes ago  claude[abc]: Bash(go test ./...)"},
	}
	preview := func(id string) string {
		var b strings.Builder
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "\x1b[32m+\tfunc main() {\t// tabs galore, id %s\x1b[m\r\n", id[:7])
		}
		return b.String()
	}
	for _, size := range [][2]int{{45, 14}, {80, 24}, {120, 40}, {40, 10}} {
		w, h := size[0], size[1]
		m := pickModel{title: "snapshots on main — r restores, q leaves everything untouched",
			items: items, preview: preview, confirm: "restore the whole tree to %s? y/n", cache: map[int]string{}}
		m.Init()
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		check := func(phase string) {
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != h {
				t.Errorf("%dx%d %s: %d lines, want %d", w, h, phase, len(lines), h)
			}
			for n, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("%dx%d %s line %d: width %d > %d: %q", w, h, phase, n, lw, w, l)
				}
			}
		}
		check("list")
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		check("diff")
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		check("confirm")
	}
}
