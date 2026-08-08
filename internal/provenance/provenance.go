// Package provenance builds snapshot commit subjects: `<source>: <detail>`
// (docs/DESIGN.md §3). The subject is the timeline's one-line answer to
// "what was this snapshot taken ahead of" — single line, bounded length.
package provenance

import (
	"strings"
	"unicode/utf8"
)

const maxDetail = 120

// Manual is a deliberate checkpoint: bare `jog` or `jog -m "msg"`.
func Manual(detail string) string {
	if detail == "" {
		return "manual"
	}
	return "manual: " + sanitize(detail)
}

// PreGit is a passthrough snapshot taken causally before a git command.
func PreGit(gitArgs []string) string {
	return "pre: " + sanitize("git "+strings.Join(gitArgs, " "))
}

func sanitize(s string) string {
	s = strings.Join(strings.Fields(s), " ") // collapse newlines/runs of space
	if utf8.RuneCountInString(s) <= maxDetail {
		return s
	}
	r := []rune(s)
	return string(r[:maxDetail-1]) + "…"
}
