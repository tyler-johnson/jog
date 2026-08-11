package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/selfupdate"
	"github.com/tyler-johnson/jog/internal/snap"
)

// ShellHook handles `jog shell-hook [--history] -- <cmdline>` — the
// runtime entry `jog shell install` wires into a shell's preexec
// mechanism (bash PS0, zsh preexec_functions, fish's fish_preexec
// event). It runs synchronously, causally before the typed command, so
// the snapshot is guaranteed to predate whatever the command destroys.
// The repo is discovered from this process's working directory — the
// shell's cwd is the truth.
//
// Iron rule: ALWAYS exit 0, and NEVER write to stdout or stderr —
// output would land in the terminal ahead of every command the user
// types. Diagnostics only under JOG_DEBUG=1. The `--` is load-bearing:
// main stops help scanning at `--`, so a typed command literally equal
// to `--help` can never surface a help page from the hook.
func ShellHook(args []string) int {
	history := false
	payload := []string(nil)
	sawSep := false
	for i, a := range args {
		if a == "--" {
			sawSep = true
			payload = args[i+1:]
			break
		}
		if a == "--history" {
			history = true
		}
		// Any other dash-arg is ignored: hooks wired by a newer jog keep
		// snapshotting under an older one.
	}
	if !sawSep {
		// No `--` means a human typed this, not a wired hook line.
		fmt.Fprintln(os.Stderr, "jog: shell-hook is wired by `jog shell install`, not run by hand")
		return 0
	}

	// The cmdline arrives as one argv entry after `--`; rejoin defensively
	// in case a shell split it.
	cmdline := strings.TrimSpace(strings.Join(payload, " "))
	if history {
		cmdline = stripHistoryIndex(cmdline)
	}
	if f := strings.Fields(cmdline); len(f) > 0 && f[0] == "jog" {
		return 0 // jog commands snapshot themselves
	}

	wd, err := os.Getwd()
	if err != nil {
		return hookDone("getwd", err)
	}
	repo, err := gitx.Discover(wd)
	if err != nil {
		return hookDone("discover", err) // outside a repo: the common, silent case
	}
	if repo.Bare {
		return 0
	}

	detail := cmdline
	if detail == "" {
		detail = "shell command"
	}
	res, err := snap.Take(repo, provenance.Pre(detail))
	if err != nil {
		return hookDone("snapshot", err)
	}
	debugf("shell-hook: %+v", res)

	// Maintenance rides here too — silent detached spawns, so a
	// preexec-only setup stays self-maintaining. Never Pending/notices:
	// the iron rule above owns this process's output.
	maybeSpawnTrim(repo)
	selfupdate.MaybeSpawnCheck(version)
	return 0
}

// historyIndexRE matches the leading entry number bash's `history 1`
// prints (`  42  cmd`); the `*` is history's modified-entry marker.
var historyIndexRE = regexp.MustCompile(`^\s*\d+\*?\s+`)

// stripHistoryIndex removes bash's history index from a `history 1`
// line, leaving the typed command.
func stripHistoryIndex(s string) string {
	return strings.TrimSpace(historyIndexRE.ReplaceAllString(s, ""))
}
