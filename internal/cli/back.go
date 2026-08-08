package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Back is `jog back <path>… [--at T]` / `jog back --all [--at T]`: restore
// from a snapshot, worktree-only — the single command that writes the
// user's files, and it never touches the index (matrix row 15).
//
// Ordering is load-bearing:
//  1. resolve the target — BEFORE the pre-restore snapshot, so the default
//     ("newest snapshot") and @{N} mean the timeline the user just looked at;
//  2. snapshot (mandatory, not best-effort) — the undo point, and for --all
//     the diff base that makes deletions computable;
//  3. write the worktree.
func Back(args []string) int {
	all := false
	at := ""
	var paths []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--all":
			all = true
		case a == "--at":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "jog: --at requires a value (snap id or reflog time like 20.minutes.ago)")
				return 2
			}
			at = args[i]
		case strings.HasPrefix(a, "--at="):
			at = strings.TrimPrefix(a, "--at=")
		default:
			paths = append(paths, a)
		}
	}
	if all == (len(paths) > 0) {
		fmt.Fprintln(os.Stderr, `jog: usage: jog back <path>… [--at T]  |  jog back --all [--at T]`)
		return 2
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
	target, err := resolveTarget(repo, ref, at)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}

	// The pre-restore snapshot makes the restore itself undoable (jj-style)
	// and is required, not best-effort: without it, --all can't compute
	// deletions and a bad restore would be unrecoverable.
	res, err := snap.Take(repo, provenance.Pre(strings.TrimSpace("jog back "+strings.Join(args, " "))))
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: pre-restore snapshot failed, aborting: %v\n", err)
		return 1
	}
	if res.Contended {
		fmt.Fprintln(os.Stderr, "jog: pre-restore snapshot lost to a concurrent jog run; retry")
		return 1
	}
	fresh := res.Commit
	if fresh == "" { // no-op: the tree is already captured at the ref tip
		if fresh, err = repo.RunRead("rev-parse", ref); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 1
		}
	}

	if all {
		return backAll(repo, target, fresh)
	}
	return backPaths(repo, target, paths)
}

// resolveTarget turns --at into a snapshot commit: try a commit-ish (snap
// id) first, then git's own reflog syntax against the chain ref — where a
// time past the oldest entry falls back to oldest with git's warning on
// stderr, exit 0 (verified). Whatever resolves must be a jog snapshot: the
// fixed identity (D1) is the guard against restoring an arbitrary tree.
func resolveTarget(repo *gitx.Repo, ref, at string) (string, error) {
	var sha string
	var err error
	switch {
	case at == "":
		if sha, err = repo.RunRead("rev-parse", "-q", "--verify", ref); err != nil {
			return "", fmt.Errorf("no snapshots on %s yet — nothing to restore from", strings.TrimPrefix(ref, "refs/jog/"))
		}
	default:
		if sha, err = repo.RunRead("rev-parse", "-q", "--verify", at+"^{commit}"); err != nil {
			spec := ref + "@{" + strings.TrimSuffix(strings.TrimPrefix(at, "@{"), "}") + "}"
			if sha, err = repo.RunReadLoud("rev-parse", "-q", "--verify", spec+"^{commit}"); err != nil {
				return "", fmt.Errorf("cannot resolve --at %q as a snap id or reflog time on %s", at, ref)
			}
		}
	}
	committer, err := repo.RunRead("log", "-1", "--format=%ce", sha)
	if err != nil {
		return "", err
	}
	if committer != snap.IdentityEmail {
		return "", fmt.Errorf("%s is not a jog snapshot — jog back only restores from the snapshot timeline", at)
	}
	return sha, nil
}

// backPaths restores the given paths (cwd-relative, git pathspec semantics)
// from the target. `git restore --source … --worktree` only — checkout
// would silently stage into the real index (verified trap).
func backPaths(repo *gitx.Repo, target string, paths []string) int {
	restoreArgs := append([]string{"restore", "--source=" + target, "--worktree", "--"}, paths...)
	if _, err := repo.Run(restoreArgs...); err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	fmt.Printf("restored %s from %s\n", strings.Join(paths, " "), describe(repo, target))
	fmt.Println("(undo: jog back " + strings.Join(paths, " ") + ")")
	return 0
}

// backAll makes the worktree exactly the target tree: diff target→fresh
// (the just-taken snapshot), restore modified/deleted paths, delete paths
// added since the target. Both trees respect ignore rules, so ignored files
// are structurally untouchable. Runs from the toplevel — diff paths are
// root-relative.
func backAll(repo *gitx.Repo, target, fresh string) int {
	top, err := repo.RunRead("rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	tr := *repo
	tr.WorkDir = top

	out, err := tr.RunRead("diff", "--name-status", "--no-renames", "-z", target, fresh)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if out == "" {
		fmt.Printf("already at %s — nothing to restore\n", describe(repo, target))
		return 0
	}

	var toRestore, toDelete []string
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		if status == "A" { // added since target → delete to match target
			toDelete = append(toDelete, path)
		} else { // M/T/D → target has the content; restore it
			toRestore = append(toRestore, path)
		}
	}

	for _, p := range toDelete {
		removeWithEmptyParents(top, p)
	}
	if len(toRestore) > 0 {
		restoreArgs := append([]string{"restore", "--source=" + target, "--worktree", "--"}, toRestore...)
		if _, err := tr.Run(restoreArgs...); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v (pre-restore state is snapshotted at %s)\n", err, fresh[:7])
			return 1
		}
	}
	fmt.Printf("restored to %s: %d restored, %d deleted\n", describe(repo, target), len(toRestore), len(toDelete))
	fmt.Println("(undo: jog back --all)")
	return 0
}

// removeWithEmptyParents deletes a worktree file and any directories the
// deletion empties (git tracks no directories, so empties are litter).
func removeWithEmptyParents(top, rel string) {
	abs := filepath.Join(top, rel)
	if os.Remove(abs) != nil {
		return
	}
	for d := filepath.Dir(abs); d != top; d = filepath.Dir(d) {
		if os.Remove(d) != nil { // non-empty or gone: stop
			return
		}
	}
}

func describe(repo *gitx.Repo, sha string) string {
	out, err := repo.RunRead("log", "-1", "--format=%h (%cr — %s)", sha)
	if err != nil {
		return sha[:7]
	}
	return out
}
