package cli

import "github.com/charmbracelet/lipgloss"

// Shared output styles, matched to what exec'd git already renders in
// snaps/since: snap ids yellow like git log hashes. lipgloss drops all of
// it on non-TTY output, so piped output stays plain text.
var (
	styleTitle = lipgloss.NewStyle().Bold(true)
	styleID    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleGood  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	styleDim   = lipgloss.NewStyle().Faint(true)
)

// styleSnapID colors the leading 7-char snap id of a describe()-style
// string ("bad873d (2 minutes ago — manual: …)").
func styleSnapID(s string) string {
	if len(s) > 7 && s[7] == ' ' {
		return styleID.Render(s[:7]) + s[7:]
	}
	return s
}
