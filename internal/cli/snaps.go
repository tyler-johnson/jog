package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// snapsFormat renders one timeline entry: short id, age, provenance, then
// the body only when present (%+b) — it holds the maxFileSize skipped-list;
// --name-status or -p supplies the files-changed detail.
const snapsFormat = "--format=%C(auto,yellow)%h%C(auto,reset)  %C(auto,green)%cr%C(auto,reset)  %s%+b"

// snapsAllFormat adds the chain each entry belongs to (%S — the ref as
// spelled on the command line).
const snapsAllFormat = "--format=%C(auto,yellow)%h%C(auto,reset)  %C(auto,cyan)%S%C(auto,reset)  %C(auto,green)%cr%C(auto,reset)  %s%+b"

// Snaps is `jog snaps [-p] [--all] [path…]`: the timeline of the current
// branch's chain (D5) — or with --all the whole forest, every chain
// interleaved by time (plan D13) — rendered by real `git log` over the exact
// snapshot ranges, exec'd so git's pager and coloring apply.
func Snaps(args []string) int {
	patch := false
	all := false
	var paths []string
	for _, a := range args {
		switch a {
		case "-p", "--patch":
			patch = true
		case "--all":
			all = true
		default:
			paths = append(paths, a)
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

	// Snapshot first, jj-style: browsing the timeline is a command boundary
	// too, so the tree you're looking from is itself on the timeline.
	// Best-effort; a failure must not block the read.
	snap.Take(repo, provenance.Pre(strings.TrimSpace("jog snaps "+strings.Join(args, " "))))

	format := snapsFormat
	var ranges []string
	if all {
		var err error
		if ranges, err = forestRanges(repo); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 1
		}
		if len(ranges) == 0 {
			fmt.Println("no snapshots anywhere yet — run `jog`, or any git command via the alias")
			return 0
		}
		// %S attributes each entry to the chain it came from, spelled as
		// passed — forestRanges passes `jog/<branch>` so the label is short.
		format = snapsAllFormat
	} else {
		ref, rng, exists, err := chainRange(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 1
		}
		if !exists {
			// Loud empty state: until `doctor` exists, snaps doubles as the
			// liveness check — silence here could mask a dead engine.
			fmt.Printf("no snapshots on %s yet — run `jog`, or any git command via the alias\n",
				strings.TrimPrefix(ref, "refs/jog/"))
			return 0
		}
		ranges = []string{rng}
	}

	gitArgs := []string{"log", "--first-parent", format}
	if patch {
		gitArgs = append(gitArgs, "-p")
	} else {
		gitArgs = append(gitArgs, "--name-status")
	}
	gitArgs = append(gitArgs, ranges...)
	if len(paths) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	return execGit(gitArgs)
}

// forestRanges builds the git log arguments covering every chain: each tip
// (spelled `jog/<branch>` so %S renders short), plus a `^boundary` per chain
// so no walk runs off its oldest snapshot into real history. Multi-tip
// --first-parent follows each chain's own first-parent line and interleaves
// by commit date (lab-verified, D13).
func forestRanges(repo *gitx.Repo) ([]string, error) {
	out, err := repo.RunRead("for-each-ref", "--format=%(refname)", "refs/jog/")
	if err != nil || out == "" {
		return nil, err
	}
	var args []string
	var bounds []string
	for _, ref := range strings.Split(out, "\n") {
		boundary, err := chainBoundary(repo, ref)
		if err != nil {
			return nil, err
		}
		args = append(args, "jog/"+strings.TrimPrefix(ref, "refs/jog/"))
		if boundary != "" {
			bounds = append(bounds, "^"+boundary)
		}
	}
	return append(args, bounds...), nil
}

// chainRange resolves the current chain ref and the git range covering
// exactly its snapshots — `boundary..ref` (see chainBoundary).
func chainRange(repo *gitx.Repo) (ref, rng string, exists bool, err error) {
	ref = chainRef(repo)
	if _, verr := repo.RunRead("rev-parse", "-q", "--verify", ref); verr != nil {
		return ref, "", false, nil
	}
	boundary, err := chainBoundary(repo, ref)
	if err != nil {
		return "", "", false, err
	}
	rng = ref
	if boundary != "" {
		rng = boundary + ".." + ref
	}
	return ref, rng, true, nil
}

// chainBoundary finds where a chain's snapshots end and real history begins.
// The oldest snapshot's parent 1 is a real HEAD commit, so an unbounded
// --first-parent walk would run off the chain; the boundary is the first
// commit not committed by the fixed jog identity (D1). Empty when the chain
// bottoms out without one (unborn-HEAD root). Streamed and killed early —
// the walk below the boundary is the repo's whole history.
func chainBoundary(repo *gitx.Repo, ref string) (string, error) {
	cmd, out, err := repo.StartRead("log", "--first-parent", "--format=%H %ce", ref)
	if err != nil {
		return "", err
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		hash, email, _ := strings.Cut(scanner.Text(), " ")
		if email != snap.IdentityEmail {
			return hash, nil
		}
	}
	return "", nil
}

// chainRef resolves the current branch's chain ref (refs/jog/<branch>, or
// refs/jog/@detached on a detached HEAD).
func chainRef(repo *gitx.Repo) string {
	branch, detached := repo.HeadBranch()
	if detached {
		return "refs/jog/@detached"
	}
	return "refs/jog/" + branch
}

// recentEntries returns the newest n timeline lines, for the bare-`jog`
// readout (D6).
func recentEntries(repo *gitx.Repo, n int) []string {
	_, rng, exists, err := chainRange(repo)
	if err != nil || !exists {
		return nil
	}
	out, err := repo.RunRead("log", "--first-parent", "-n", fmt.Sprint(n), "--format=%h  %cr  %s", rng)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
