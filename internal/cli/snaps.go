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

// Snaps is `jog snaps [-p] [path…]`: the timeline of the current branch's
// chain (D5), rendered by real `git log` over the exact snapshot range —
// exec'd, so git's pager and coloring apply.
func Snaps(args []string) int {
	patch := false
	var paths []string
	for _, a := range args {
		if a == "-p" || a == "--patch" {
			patch = true
		} else {
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

	gitArgs := []string{"log", "--first-parent", snapsFormat}
	if patch {
		gitArgs = append(gitArgs, "-p")
	} else {
		gitArgs = append(gitArgs, "--name-status")
	}
	gitArgs = append(gitArgs, rng)
	if len(paths) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	return execGit(gitArgs)
}

// chainRange resolves the current chain ref and the git range covering
// exactly its snapshots. The oldest snapshot's parent 1 is a real HEAD
// commit, so an unbounded --first-parent walk would run off the chain into
// real history; the boundary is the first commit not committed by the fixed
// jog identity (D1), and `boundary..ref` excludes it and everything below.
func chainRange(repo *gitx.Repo) (ref, rng string, exists bool, err error) {
	ref = chainRef(repo)
	if _, verr := repo.RunRead("rev-parse", "-q", "--verify", ref); verr != nil {
		return ref, "", false, nil
	}

	cmd, out, err := repo.StartRead("log", "--first-parent", "--format=%H %ce", ref)
	if err != nil {
		return "", "", false, err
	}
	defer func() {
		cmd.Process.Kill() // walk terminated early; the rest is real history
		cmd.Wait()
	}()
	boundary := ""
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		hash, email, _ := strings.Cut(scanner.Text(), " ")
		if email != snap.IdentityEmail {
			boundary = hash
			break
		}
	}
	rng = ref
	if boundary != "" {
		rng = boundary + ".." + ref
	}
	return ref, rng, true, nil
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
