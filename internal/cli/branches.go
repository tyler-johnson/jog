package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Branches is `jog branches [--json]`: one row per snapshot chain — the
// branch, its snapshot count, and its newest snapshot — with the current
// branch starred and deleted branches' chains flagged. The first-class
// answer to "which branches does jog have work for?", and the place to
// see gone chains before `trim --gone` drops them. `branch` is an alias
// (verb records what was typed).
func Branches(verb string, args []string) int {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		default:
			fmt.Fprintln(os.Stderr, "jog: usage: jog branches [--json]")
			return 2
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

	// Snapshot first, jj-style: listing the chains is a command boundary
	// too, and the current branch's row then reflects it. Best-effort; a
	// failure must not block the read.
	snap.Take(repo, provenance.Pre("jog "+verb))

	var chains []trimChain
	if out, err := repo.RunRead("for-each-ref", "--format=%(refname)", "refs/jog/"); err == nil && out != "" {
		for _, ref := range strings.Split(out, "\n") {
			if strings.HasPrefix(ref, "refs/jog/@trash/") {
				continue // trim's insurance refs are not chains
			}
			entries, err := listChain(repo, ref)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s: %v\n", ref, err)
				continue
			}
			if len(entries) == 0 {
				continue // a fully foreign ref under refs/jog/ is not a chain
			}
			chains = append(chains, trimChain{ref, entries, true})
		}
	}
	if len(chains) == 0 {
		if jsonOut {
			fmt.Println("[]")
			return 0
		}
		fmt.Println("no snapshot chains in this repository — run `jog`, or any git command via the alias")
		return 0
	}
	markLive(repo, chains)

	// Current branch first, then newest tip first: the row you came for is
	// on top, and the rest read in the same order `jog log --all` shows.
	current := ""
	if b, detached := repo.HeadBranch(); !detached {
		current = b
	}
	sort.SliceStable(chains, func(i, j int) bool {
		ci, cj := chainName(chains[i].ref) == current, chainName(chains[j].ref) == current
		if ci != cj {
			return ci
		}
		return chains[i].entries[0].commitUnix > chains[j].entries[0].commitUnix
	})

	if jsonOut {
		return branchesJSON(chains)
	}

	nameW, countW := 0, 0
	for _, c := range chains {
		if n := len(chainName(c.ref)); n > nameW {
			nameW = n
		}
		if n := len(fmt.Sprint(len(c.entries))); n > countW {
			countW = n
		}
	}
	anyGone := false
	for _, c := range chains {
		name := chainName(c.ref)
		e := c.entries[0]
		// The gutter column: * current branch, - deleted branch (the
		// footer explains it). @detached is neither — it has no branch to
		// be current or deleted — so it keeps a blank gutter and a suffix.
		mark := " "
		switch {
		case current != "" && name == current:
			mark = "*"
		case name != "@detached" && !c.live:
			anyGone = true
			mark = "-"
		}
		line := fmt.Sprintf("%s %s  %*d %s, newest %s — %s",
			mark, styleID.Render(fmt.Sprintf("%-*s", nameW, name)),
			countW, len(c.entries), plural(len(c.entries), "snapshot"),
			humanAge(time.Since(time.Unix(e.commitUnix, 0))), e.subjectLine())
		if name == "@detached" {
			line += "  " + styleDim.Render("(detached HEAD)")
		}
		fmt.Println(line)
	}
	if anyGone {
		fmt.Println(styleDim.Render("- Deleted branch, clean up with jog trim --gone"))
	}
	return 0
}

// branchChain is one chain in `jog branches --json`; newest speaks the
// same id/sha/time/age/provenance vocabulary as `jog log --json`.
type branchChain struct {
	Branch    string       `json:"branch"`
	Live      bool         `json:"live"`
	Snapshots int          `json:"snapshots"`
	Newest    branchNewest `json:"newest"`
}

type branchNewest struct {
	ID         string `json:"id"`
	SHA        string `json:"sha"`
	Time       string `json:"time"`
	Age        string `json:"age"`
	Provenance string `json:"provenance"`
}

func branchesJSON(chains []trimChain) int {
	rows := make([]branchChain, 0, len(chains))
	for _, c := range chains {
		e := c.entries[0]
		when := time.Unix(e.commitUnix, 0)
		rows = append(rows, branchChain{
			Branch:    chainName(c.ref),
			Live:      c.live,
			Snapshots: len(c.entries),
			Newest: branchNewest{
				ID: e.sha[:7], SHA: e.sha,
				Time:       when.Format(time.RFC3339),
				Age:        humanAge(time.Since(when)),
				Provenance: e.subjectLine(),
			},
		})
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	fmt.Println(string(b))
	return 0
}
