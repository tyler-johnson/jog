// Package snap is jog's snapshot engine — the lab-verified recipe from
// docs/DESIGN.md §4 encoded in Go, with each gotcha preserved as a comment
// and a test.
//
// A snapshot is an ordinary git commit: its tree is the working tree's state
// (tracked + untracked, .gitignore respected), parent 1 is the previous
// snapshot on the branch's chain (the timeline), parent 2 is the HEAD commit
// at snapshot time (the base edge). The chain head lives at
// refs/jog/<branch>, invisible to branches, index, and remotes.
package snap

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-johnson/jog/internal/gitx"
)

const (
	// Fixed snapshot identity (plan D1). Load-bearing: `jog snaps` stops its
	// first-parent walk at the first commit not committed by this identity —
	// the oldest snapshot's parent 1 is a real HEAD commit, so without the
	// marker the walk would run off the chain into real history.
	IdentityName  = "jog"
	IdentityEmail = "jog@local"

	// DefaultMaxFileSize guards against snapshotting huge new blobs
	// (jog.maxFileSize config; 0 disables).
	DefaultMaxFileSize = 50 << 20 // 50 MiB

	// SkippedHeader introduces the commit-body paragraph listing files
	// skipped for size (plan D2) — `snaps` surfaces it, no side channel.
	SkippedHeader = "Skipped (jog.maxFileSize):"

	gcKeyExpire      = "gc.refs/jog/*.reflogExpire"
	gcKeyUnreachable = "gc.refs/jog/*.reflogExpireUnreachable"
)

// ErrBareRepo reports a bare repository — there is no working tree to
// snapshot.
var ErrBareRepo = errors.New("bare repository: nothing to snapshot")

// errBusy is returned internally when the shadow index stays locked after a
// retry: a concurrent jog holds it and captured a near-identical tree, so we
// skip rather than block (never delay the user's command).
var errBusy = errors.New("shadow index locked by a concurrent snapshot")

// Result describes one engine run.
type Result struct {
	Ref           string   // refs/jog/<branch> (or refs/jog/@detached)
	Commit        string   // new snapshot sha; empty when NoOp or Contended
	NoOp          bool     // tree identical to the last snapshot; nothing written
	Contended     bool     // lost to a concurrent snapshot (lock or CAS); benign
	FirstSnapshot bool     // this run created the ref
	GCConfigured  bool     // per-repo gc reflog-expire keys verified/written
	SkippedFiles  []string // paths excluded by jog.maxFileSize
}

type engine struct {
	repo   *gitx.Repo // WorkDir pinned to the worktree toplevel
	shadow *gitx.Repo // same, with GIT_INDEX_FILE → $GIT_DIR/jog/index
	top    string
}

// Take snapshots the working tree with the given provenance message
// (first line: `<source>: <detail>`, see DESIGN §3).
//
// It never writes the user's index, worktree, HEAD, or branches; on
// contention it skips rather than blocks. Callers on hook/passthrough paths
// must treat any error as non-fatal (plan D4).
func Take(repo *gitx.Repo, message string) (*Result, error) {
	if repo.Bare {
		return nil, ErrBareRepo
	}
	// Pin commands to the toplevel: `add -A` pathspecs and status paths are
	// then repo-root-relative regardless of the directory jog ran from.
	top, err := repo.RunRead("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	tr := *repo
	tr.WorkDir = top

	jogDir := filepath.Join(tr.GitDir, "jog")
	if err := os.MkdirAll(jogDir, 0o755); err != nil {
		return nil, err
	}
	// Absolute by construction (GitDir is absolute) — a relative
	// GIT_INDEX_FILE resolves inside the worktree (verified gotcha).
	shadowPath := filepath.Join(jogDir, "index")
	e := &engine{repo: &tr, shadow: tr.WithIndex(shadowPath), top: top}

	branch, berr := e.repo.RunRead("symbolic-ref", "--short", "HEAD")
	ref := "refs/jog/@detached"
	if berr == nil {
		ref = "refs/jog/" + branch // slashes in branch names are fine in refs
	}

	head, herr := e.repo.RunRead("rev-parse", "-q", "--verify", "HEAD")
	born := herr == nil

	excludes, skipped, err := e.scanOversize()
	if err != nil {
		return nil, err
	}

	if born {
		_, statErr := os.Stat(shadowPath)
		sparse, _ := e.repo.RunRead("config", "--type=bool", "core.sparseCheckout")
		// Seed ONCE — re-seeding kills the stat cache (~30× slower,
		// verified). Exception: under sparse checkout an unseeded shadow
		// index silently drops sparse-excluded files (verified), so re-seed
		// from HEAD every snapshot there and accept the cost.
		if os.IsNotExist(statErr) || sparse == "true" {
			if _, err := e.shadowRun("read-tree", "HEAD"); err != nil {
				return busyOr(ref, err)
			}
		}
	} else {
		// Unborn HEAD: `read-tree HEAD` is fatal (verified); start empty.
		if _, err := e.shadowRun("read-tree", "--empty"); err != nil {
			return busyOr(ref, err)
		}
	}

	// `add -A` respects .gitignore + info/exclude; embedded repos become
	// gitlinks (advice suppressed, verified). With size excludes, the `.`
	// base pathspec keeps -A tree-wide (WorkDir is the toplevel).
	addArgs := []string{"-c", "advice.addEmbeddedRepo=false", "add", "-A"}
	if len(excludes) > 0 {
		addArgs = append(addArgs, "--", ".")
		addArgs = append(addArgs, excludes...)
	}
	if _, err := e.shadowRun(addArgs...); err != nil {
		return busyOr(ref, err)
	}
	tree, err := e.shadowRun("write-tree")
	if err != nil {
		return busyOr(ref, err)
	}

	prevTree, _ := e.repo.RunRead("rev-parse", "-q", "--verify", ref+"^{tree}")
	if tree == prevTree {
		return &Result{Ref: ref, NoOp: true, SkippedFiles: skipped}, nil
	}
	prev, _ := e.repo.RunRead("rev-parse", "-q", "--verify", ref)

	// Parent order matters: parent 1 = previous snapshot (clean
	// `log --first-parent` timeline, verified), parent 2 = HEAD (base edge).
	ct := []string{"-c", "user.name=" + IdentityName, "-c", "user.email=" + IdentityEmail, "commit-tree", tree}
	if prev != "" {
		ct = append(ct, "-p", prev)
	}
	if born {
		ct = append(ct, "-p", head)
	}
	ct = append(ct, "-m", message)
	if len(skipped) > 0 {
		ct = append(ct, "-m", SkippedHeader+"\n"+strings.Join(skipped, "\n"))
	}
	snapSHA, err := e.repo.Run(ct...)
	if err != nil {
		return nil, err
	}

	// Update the ref immediately — dangling commits are gc-pruned
	// (verified). CAS form: stale old value fails instead of clobbering a
	// concurrent winner; empty old value = "must not exist" (creation CAS).
	if _, err := e.repo.Run("update-ref", "--create-reflog", "-m", message, ref, snapSHA, prev); err != nil {
		var ge *gitx.GitError
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "cannot lock ref") {
			return &Result{Ref: ref, Contended: true}, nil
		}
		return nil, err
	}

	res := &Result{Ref: ref, Commit: snapSHA, SkippedFiles: skipped, FirstSnapshot: prev == ""}
	if res.FirstSnapshot {
		res.GCConfigured = e.ensureGCConfig()
	}
	return res, nil
}

// shadowRun runs a shadow-index command, retrying once (~50 ms) if a
// concurrent jog holds the shadow lock, then giving up with errBusy. git
// locks only `<shadow>.lock` for these — never the user's .git/index.lock
// (verified).
func (e *engine) shadowRun(args ...string) (string, error) {
	out, err := e.shadow.Run(args...)
	if !isLockErr(err) {
		return out, err
	}
	time.Sleep(50 * time.Millisecond)
	out, err = e.shadow.Run(args...)
	if isLockErr(err) {
		return "", errBusy
	}
	return out, err
}

func isLockErr(err error) bool {
	var ge *gitx.GitError
	return errors.As(err, &ge) && strings.Contains(ge.Stderr, ".lock") &&
		(strings.Contains(ge.Stderr, "File exists") || strings.Contains(ge.Stderr, "Unable to create"))
}

func busyOr(ref string, err error) (*Result, error) {
	if errors.Is(err, errBusy) {
		return &Result{Ref: ref, Contended: true}, nil
	}
	return nil, err
}

// scanOversize finds changed/untracked files over jog.maxFileSize before
// they are staged (plan D2) — pre-scan, because by `add` time the blob is
// already minted into the odb. Returns `:(exclude,literal)` pathspecs for
// the engine's add and the repo-relative skip list for the commit body.
func (e *engine) scanOversize() (excludes, skipped []string, err error) {
	limit := e.maxFileSize()
	if limit <= 0 {
		return nil, nil, nil
	}
	// -uall enumerates files inside untracked directories (a bare `?? dir/`
	// entry would hide oversized files). Porcelain paths are root-relative;
	// WorkDir is the toplevel, so Lstat can join directly.
	out, err := e.repo.RunRead("status", "--porcelain", "-z", "-uall")
	if err != nil {
		return nil, nil, err
	}
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		ent := entries[i]
		if len(ent) < 4 {
			continue
		}
		x, path := ent[0], ent[3:]
		if x == 'R' || x == 'C' {
			i++ // rename/copy entries carry a second NUL-terminated origin path
		}
		fi, statErr := os.Lstat(filepath.Join(e.top, path))
		if statErr != nil || !fi.Mode().IsRegular() {
			continue // deleted, symlink, etc. — nothing oversized to stage
		}
		if fi.Size() > limit {
			excludes = append(excludes, ":(exclude,literal)"+path)
			skipped = append(skipped, path)
		}
	}
	return excludes, skipped, nil
}

func (e *engine) maxFileSize() int64 {
	// --type=int canonicalizes k/m/g suffixes ("50m" → 52428800).
	out, err := e.repo.RunRead("config", "--type=int", "--get", "jog.maxFileSize")
	if err != nil {
		return DefaultMaxFileSize
	}
	n, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		return DefaultMaxFileSize
	}
	return n
}

// ensureGCConfig writes the per-repo keys that stop git's own gc from
// expiring jog reflog entries (per-ref-pattern config wins over globals,
// verified). Written lazily on first refs/jog/* creation (plan D3 — v1's
// init/doctor flow makes this consensual and explicit). Best-effort: a
// failure never fails the snapshot; callers may warn via GCConfigured.
func (e *engine) ensureGCConfig() bool {
	if _, err := e.repo.RunRead("config", "--local", "--get", gcKeyExpire); err == nil {
		return true // already configured; never rewrite user config
	}
	if _, err := e.repo.Run("config", gcKeyExpire, "never"); err != nil {
		return false
	}
	if _, err := e.repo.Run("config", gcKeyUnreachable, "never"); err != nil {
		return false
	}
	return true
}
