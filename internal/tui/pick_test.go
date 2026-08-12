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
		{ID: "deadbee1234567", Label: "\x1b[2m● deadbee  commit: fix parser — 20 minutes ago\x1b[0m", Inert: true},
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

// Inert rows are context markers: the cursor starts below a leading one,
// hops over them in both directions, page jumps never rest on one, the
// footer counts selectable rows only, and r on an inert row (forced — a
// state the movement keys can't reach) chooses nothing.
func TestInertRows(t *testing.T) {
	items := []PickItem{
		{ID: "e0", Label: "● event", Inert: true},
		{ID: "s1", Label: "snap 1"},
		{ID: "e1", Label: "● event", Inert: true},
		{ID: "s2", Label: "snap 2"},
		{ID: "s3", Label: "snap 3"},
		{ID: "e2", Label: "● event", Inert: true},
	}
	m := pickModel{title: "t", items: items, preview: func(string) string { return "p" }, cache: map[int]string{}}
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if m.cursor != 1 {
		t.Errorf("initial cursor=%d, want 1 (skip leading inert)", m.cursor)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 3 {
		t.Errorf("down: cursor=%d, want 3 (hop inert row 2)", m.cursor)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 4 {
		t.Errorf("down at last selectable: cursor=%d, want 4 (trailing inert unreachable)", m.cursor)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.cursor != 3 {
		t.Errorf("up: cursor=%d, want 3", m.cursor)
	}

	// Page jumps clamp then slide off inert landings.
	m.jump(100)
	if m.cursor != 4 {
		t.Errorf("jump past end: cursor=%d, want 4 (slide off trailing inert)", m.cursor)
	}
	m.jump(-100)
	if m.cursor != 1 {
		t.Errorf("jump past start: cursor=%d, want 1 (slide off leading inert)", m.cursor)
	}

	if rank, count := m.selPos(); rank != 1 || count != 3 {
		t.Errorf("selPos = %d/%d, want 1/3", rank, count)
	}

	// r on an inert row must be a no-op, not a choice.
	m.cursor = 2
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.chosen != -1 || m.confirming {
		t.Errorf("r on inert: chosen=%d confirming=%v, want no-op", m.chosen, m.confirming)
	}
}

// TestPageKeys: pgup/pgdn page whichever frame has focus — the list jumps
// by a page of rows (clamped at both ends), the preview scrolls as before.
func TestPageKeys(t *testing.T) {
	var items []PickItem
	for i := 0; i < 100; i++ {
		items = append(items, PickItem{ID: fmt.Sprintf("%07d", i), Label: fmt.Sprintf("row %d", i)})
	}
	m := pickModel{title: "t", items: items, preview: func(string) string { return strings.Repeat("x\n", 200) }, cache: map[int]string{}}
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	page := m.listH()
	if page < 2 {
		t.Fatalf("test wants a multi-row list, got listH=%d", page)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.cursor != page {
		t.Errorf("pgdown: cursor=%d, want %d", m.cursor, page)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.cursor != 0 {
		t.Errorf("pgup: cursor=%d, want 0", m.cursor)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.cursor != 0 {
		t.Errorf("pgup at top: cursor=%d, want clamp at 0", m.cursor)
	}
	for i := 0; i < 100; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if m.cursor != len(items)-1 {
		t.Errorf("pgdown past end: cursor=%d, want clamp at %d", m.cursor, len(items)-1)
	}

	// Preview focus: the same keys scroll the preview, not the cursor.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cursorBefore, offBefore := m.cursor, m.offset
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.cursor != cursorBefore || m.offset <= offBefore {
		t.Errorf("pgdown in diff: cursor=%d offset=%d, want cursor unchanged and offset advanced", m.cursor, m.offset)
	}
}
