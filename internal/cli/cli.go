// Package cli implements jog's user-facing commands.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Snapshot is bare `jog` / `jog -m "msg"`: a deliberate checkpoint.
func Snapshot(message string) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	repo, err := gitx.Discover(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	res, err := snap.Take(repo, provenance.Manual(message))
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	report(res)
	// D6: bare jog mirrors jj's no-arg default — snapshot, then show the
	// top of the timeline.
	if lines := recentEntries(repo, 3); len(lines) > 0 {
		fmt.Println()
		for _, l := range lines {
			fmt.Println("  " + l)
		}
	}
	return 0
}

func report(res *snap.Result) {
	branch := strings.TrimPrefix(res.Ref, "refs/jog/")
	switch {
	case res.Contended:
		fmt.Println("snapshot skipped: a concurrent jog snapshot is in progress")
	case res.NoOp:
		fmt.Printf("no changes since the last snapshot on %s\n", branch)
	default:
		fmt.Printf("snapshot %s on %s\n", styleID.Render(res.Commit[:7]), branch)
	}
	for _, f := range res.SkippedFiles {
		fmt.Fprintf(os.Stderr, "jog: skipped %s (over jog.maxFileSize)\n", f)
	}
	if res.FirstSnapshot {
		// D3: the one-time notice for the lazily written per-repo gc keys.
		fmt.Fprintf(os.Stderr, "jog: created %s; set gc.refs/jog/* reflog-expire config for this repo\n", res.Ref)
		if !res.GCConfigured {
			fmt.Fprintln(os.Stderr, "jog: warning: could not set gc config; git gc may expire jog reflogs")
		}
	}
}

// Passthrough is every non-reserved invocation: snapshot causally before the
// command, then hand off to the real git binary — real TTY, real exit codes,
// jog reimplements nothing (DESIGN §5). On unix the handoff replaces this
// process and never returns on success; on windows it proxies git as a child
// and mirrors its exit code (execGit is per-platform).
func Passthrough(gitArgs []string) int {
	// Best-effort snapshot: an engine failure must never block the user's
	// command (plan D4) — warn and exec anyway.
	if wd, err := os.Getwd(); err == nil {
		if repo, err := gitx.Discover(wd); err == nil && !repo.Bare {
			if _, serr := snap.Take(repo, provenance.PreGit(gitArgs)); serr != nil {
				fmt.Fprintf(os.Stderr, "jog: snapshot skipped: %v\n", serr)
			}
		} else if err != nil && !errors.Is(err, gitx.ErrNotARepo) {
			fmt.Fprintf(os.Stderr, "jog: snapshot skipped: %v\n", err)
		}
		// Not a repo / bare: nothing to snapshot; stay silent — `git init`,
		// `git clone`, `git version` outside repos must feel exactly like git.
	}

	return execGit(gitArgs)
}
