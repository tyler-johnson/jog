// Package provenance builds snapshot commit subjects: `<source>: <detail>`
// (docs/DESIGN.md §3). The subject is the timeline's one-line answer to
// "what was this snapshot taken ahead of" — single line, bounded length.
package provenance

import (
	"strings"
	"unicode/utf8"
)

const (
	maxSubject    = 120
	maxHookDetail = 80
	sessionPrefix = 8
)

// Manual is a deliberate checkpoint: bare `jog` or `jog -m "msg"`.
func Manual(detail string) string {
	if detail == "" {
		return "manual"
	}
	return "manual: " + Truncate(detail, maxSubject)
}

// Pre is a snapshot taken causally before a command runs.
func Pre(cmdline string) string {
	return "pre: " + Truncate(cmdline, maxSubject)
}

// PreGit is Pre for a passthrough git command.
func PreGit(gitArgs []string) string {
	return Pre("git " + strings.Join(gitArgs, " "))
}

// Claude is a Claude Code hook snapshot: `claude[<sid[:8]>]: <detail>`.
// The session prefix groups a session's snapshots in the timeline and pairs
// them with Claude's own checkpoints for the /rewind complement.
func Claude(sessionID, detail string) string {
	src := "claude"
	if sessionID != "" {
		id := []rune(sessionID)
		if len(id) > sessionPrefix {
			id = id[:sessionPrefix]
		}
		src = "claude[" + string(id) + "]"
	}
	if detail == "" {
		return src
	}
	return src + ": " + Truncate(detail, maxHookDetail)
}

// Truncate collapses all whitespace runs (including newlines) to single
// spaces and caps the result at n runes, ellipsizing.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
