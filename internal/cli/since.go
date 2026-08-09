package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Since is `jog since [--at T | T] [-p] [--] [path…]`: what changed between a
// snapshot and the working tree — the `jj st` analog (plan D12).
//
// It snapshots first (jj-style), then diffs snapshot ↔ snapshot: never the
// one-commit diff form, which misreports untracked files as deleted
// (verified trap, DESIGN §5). The default target is the chain tip as it was
// before this invocation's snapshot — "what changed since my last command
// boundary" — which equals @{1} whenever the fresh snapshot minted.
//
// An explicit T uses `back --at`'s exact grammar (snap id or reflog time,
// D1-identity-guarded) and resolves before the fresh snapshot, so it means
// the timeline the user just looked at — same rule as back.
func Since(args []string) int {
	patch := false
	at := ""
	var rest []string
	forcedPaths := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case forcedPaths:
			rest = append(rest, a)
		case a == "-p" || a == "--patch":
			patch = true
		case a == "--at":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "jog: --at requires a value (snap id, or a time like 1h or 20.minutes.ago)")
				return 2
			}
			at = args[i]
		case strings.HasPrefix(a, "--at="):
			at = strings.TrimPrefix(a, "--at=")
		case a == "--":
			forcedPaths = true
		default:
			rest = append(rest, a)
		}
	}

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
	ref := chainRef(repo)

	// First positional: a path if it exists on disk, otherwise a target —
	// `jog since 3h` and `jog since src/` share a slot (DESIGN §5). `--`
	// forces path interpretation for the rare collision.
	if at == "" && !forcedPaths && len(rest) > 0 {
		if _, statErr := os.Stat(rest[0]); statErr != nil {
			at = rest[0]
			rest = rest[1:]
		}
	}

	var target string
	if at != "" {
		if target, err = resolveTarget(repo, ref, at); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n  (an existing path would be diffed instead; use -- to force paths)\n", err)
			return 1
		}
	} else {
		// The pre-invocation tip; missing means the chain starts just below.
		target, _ = repo.RunRead("rev-parse", "-q", "--verify", ref)
	}

	// Snapshot first: the fresh snapshot is the worktree side of the diff —
	// untracked files are real tree entries in it, so they can never be
	// misreported. Best-effort like snaps: contention falls back to the tip
	// (the concurrent winner captured a near-identical tree).
	res, terr := snap.Take(repo, provenance.Pre(strings.TrimSpace("jog since "+strings.Join(args, " "))))
	if terr != nil {
		fmt.Fprintf(os.Stderr, "jog: snapshot failed (%v); comparing against the last one\n", terr)
	}
	fresh := ""
	if res != nil {
		fresh = res.Commit
	}
	if fresh == "" {
		if fresh, err = repo.RunRead("rev-parse", "-q", "--verify", ref); err != nil {
			fmt.Printf("no snapshots on %s yet — run `jog`, or any git command via the alias\n",
				strings.TrimPrefix(ref, "refs/jog/"))
			return 0
		}
	}

	if target == "" {
		fmt.Printf("first snapshot on %s just minted (%s) — nothing earlier to compare\n",
			strings.TrimPrefix(ref, "refs/jog/"), describe(repo, fresh))
		return 0
	}
	if target == fresh {
		fmt.Printf("no changes since %s\n", styleSnapID(describe(repo, target)))
		return 0
	}

	fmt.Printf("since %s\n", styleSnapID(describe(repo, target)))
	gitArgs := []string{"diff"}
	if patch {
		gitArgs = append(gitArgs, "-p")
	} else {
		gitArgs = append(gitArgs, "--compact-summary")
	}
	gitArgs = append(gitArgs, target, fresh)
	if len(rest) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, rest...)
	}
	return execGit(gitArgs)
}
