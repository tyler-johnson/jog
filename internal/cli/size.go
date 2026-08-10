package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
)

// Disk accounting for the timeline, built on rev-list --disk-usage
// (git ≥ 2.31 — callers degrade when it is missing rather than fail).
// Both measurements subtract everything reachable from the repo's real
// refs, so the numbers are what jog alone costs — objects a snapshot
// shares with committed history are free and stay uncounted. Best-effort
// by nature: loose objects report their loose size and shrink when git
// repacks, so every number a user sees carries a "~".

// jogDiskUsage is the bytes on disk attributable to refs/jog/*
// (including the @trash insurance refs) and nothing else.
func jogDiskUsage(repo *gitx.Repo) (int64, error) {
	out, err := repo.RunRead("rev-list", "--objects", "--disk-usage",
		"--glob=refs/jog/*", "--not", "--exclude=refs/jog/*", "--all")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(out, 10, 64)
}

// treesDiskUsage is the bytes those snapshot trees hold beyond real
// history — the projection primitive for planning: a survivor set's
// ancestry includes the very commits being dropped (they are chain
// parents), so commit reachability cannot express "survivors only", but
// their tree shas can. rev-list always emits an explicitly-listed
// positive, so each root tree counts its own object even when history
// shares it — tens of bytes of overcount per tree, noise for an estimate.
func treesDiskUsage(repo *gitx.Repo, trees []string) (int64, error) {
	seen := make(map[string]bool, len(trees))
	var in strings.Builder
	for _, t := range trees {
		if t != "" && !seen[t] {
			seen[t] = true
			in.WriteString(t)
			in.WriteByte('\n')
		}
	}
	if in.Len() == 0 {
		return 0, nil
	}
	out, err := repo.RunReadStdin(in.String(), "rev-list", "--objects",
		"--disk-usage", "--stdin", "--not", "--exclude=refs/jog/*", "--all")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(out, 10, 64)
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
}
