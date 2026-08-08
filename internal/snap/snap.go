// Package snap is jog's snapshot engine — the lab-verified recipe from
// docs/DESIGN.md §4 encoded in Go, with each gotcha preserved as a comment
// and a test.
//
// A snapshot is an ordinary git commit: its tree is the working tree's state
// (tracked + untracked, .gitignore respected), parent 1 is the previous
// snapshot on the branch's chain (the timeline), parent 2 is the HEAD commit
// at snapshot time (the base edge). The chain head lives at
// refs/jog/<branch>, invisible to branches, index, and remotes.
//
// The hot path is spawn-frugal (PLAN-V1 M8): repository state comes from one
// batched rev-parse, config from one --get-regexp, and the D2 status
// pre-scan doubles as a clean-tree detector that skips the shadow index
// entirely — five spawns for a dirty-but-unchanged no-op, three for a clean
// tree.
package snap

import (
	"errors"
	"fmt"
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

// state is everything Take needs from the object database, read in one
// batched rev-parse (see readState).
type state struct {
	top      string
	headSHA  string // empty on unborn HEAD
	headTree string // empty on unborn HEAD
	prev     string // previous snapshot; empty when the chain doesn't exist
	prevTree string
}

type repoConfig struct {
	maxFileSize int64
	sparse      bool
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

	branch, detached := repo.HeadBranch()
	ref := "refs/jog/@detached"
	if !detached {
		ref = "refs/jog/" + branch // slashes in branch names are fine in refs
	}

	st, err := readState(repo, ref)
	if err != nil {
		return nil, err
	}
	born := st.headSHA != ""

	// Pin commands to the toplevel: `add -A` pathspecs and status paths are
	// then repo-root-relative regardless of the directory jog ran from.
	tr := *repo
	tr.WorkDir = st.top

	jogDir := filepath.Join(tr.GitDir, "jog")
	if err := os.MkdirAll(jogDir, 0o755); err != nil {
		return nil, err
	}
	// Absolute by construction (GitDir is absolute) — a relative
	// GIT_INDEX_FILE resolves inside the worktree (verified gotcha).
	shadowPath := filepath.Join(jogDir, "index")
	e := &engine{repo: &tr, shadow: tr.WithIndex(shadowPath), top: st.top}

	cfg := e.readConfig()

	// One status read serves double duty: the D2 oversize pre-scan and the
	// clean-tree fast path below.
	statusOut, err := e.repo.RunRead("status", "--porcelain", "-z", "-uall")
	if err != nil {
		return nil, err
	}
	excludes, skipped := scanOversize(statusOut, cfg.maxFileSize, st.top)

	var tree string
	if born && statusOut == "" {
		// Clean tree: empty porcelain status (with -uall) means worktree ==
		// index == HEAD and nothing untracked, so the tree a shadow
		// `add -A` would produce is exactly HEAD's tree — skip the shadow
		// entirely. (Under sparse checkout the re-seeded shadow would also
		// resolve to HEAD's tree; mid-merge conflicts always show in
		// status, so they never take this path.)
		tree = st.headTree
	} else {
		if born {
			_, statErr := os.Stat(shadowPath)
			// Seed ONCE — re-seeding kills the stat cache (~30× slower,
			// verified). Exception: under sparse checkout an unseeded
			// shadow index silently drops sparse-excluded files (verified),
			// so re-seed from HEAD every snapshot there and accept the cost.
			if os.IsNotExist(statErr) || cfg.sparse {
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
		tree, err = e.shadowRun("write-tree")
		if err != nil {
			return busyOr(ref, err)
		}
	}

	if st.prev != "" && tree == st.prevTree {
		return &Result{Ref: ref, NoOp: true, SkippedFiles: skipped}, nil
	}

	// Parent order matters: parent 1 = previous snapshot (clean
	// `log --first-parent` timeline, verified), parent 2 = HEAD (base edge).
	// Identity via ENV, not `-c user.*`: GIT_COMMITTER_*/GIT_AUTHOR_* in the
	// user's environment would override config and silently break the D1
	// walk terminator; env-on-env wins (exec.Cmd keeps the last duplicate).
	ident := e.repo.WithEnv(
		"GIT_AUTHOR_NAME="+IdentityName, "GIT_AUTHOR_EMAIL="+IdentityEmail,
		"GIT_COMMITTER_NAME="+IdentityName, "GIT_COMMITTER_EMAIL="+IdentityEmail,
	)
	ct := []string{"commit-tree", tree}
	if st.prev != "" {
		ct = append(ct, "-p", st.prev)
	}
	if born {
		ct = append(ct, "-p", st.headSHA)
	}
	ct = append(ct, "-m", message)
	if len(skipped) > 0 {
		ct = append(ct, "-m", SkippedHeader+"\n"+strings.Join(skipped, "\n"))
	}
	snapSHA, err := ident.Run(ct...)
	if err != nil {
		return nil, err
	}

	// Update the ref immediately — dangling commits are gc-pruned
	// (verified). CAS form: stale old value fails instead of clobbering a
	// concurrent winner; empty old value = "must not exist" (creation CAS).
	if _, err := e.repo.Run("update-ref", "--create-reflog", "-m", message, ref, snapSHA, st.prev); err != nil {
		var ge *gitx.GitError
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "cannot lock ref") {
			return &Result{Ref: ref, Contended: true}, nil
		}
		return nil, err
	}

	res := &Result{Ref: ref, Commit: snapSHA, SkippedFiles: skipped, FirstSnapshot: st.prev == ""}
	if res.FirstSnapshot {
		res.GCConfigured = e.ensureGCConfig()
	}
	return res, nil
}

// readState fetches toplevel, HEAD, and chain-ref state in one batched
// rev-parse. rev-parse answers queries in order and, when one fails, prints
// the successful answers first, then the failing arg echoed literally, then
// exits 128 (lab-verified) — so the count of surviving lines identifies
// which optional pieces (HEAD on unborn repos, the chain ref before the
// first snapshot) are absent. The `^{commit}`/`^{tree}` suffixes force
// revision interpretation, so a worktree file named like an arg can never
// alias a query.
func readState(repo *gitx.Repo, ref string) (*state, error) {
	lines, err := tolerantRevParse(repo,
		"--show-toplevel", "HEAD^{commit}", "HEAD^{tree}", ref+"^{commit}", ref+"^{tree}")
	if err != nil {
		return nil, err
	}
	st := &state{}
	switch len(lines) {
	case 5: // the hot path: born HEAD, chain exists
		st.top, st.headSHA, st.headTree, st.prev, st.prevTree = lines[0], lines[1], lines[2], lines[3], lines[4]
	case 3: // born HEAD, no chain yet (first snapshot on this branch)
		st.top, st.headSHA, st.headTree = lines[0], lines[1], lines[2]
	case 1: // unborn HEAD killed the batch early; ask about the chain alone
		st.top = lines[0]
		rl, err := tolerantRevParse(repo, ref+"^{commit}", ref+"^{tree}")
		if err != nil {
			return nil, err
		}
		if len(rl) == 2 {
			st.prev, st.prevTree = rl[0], rl[1]
		}
	default:
		return nil, fmt.Errorf("unexpected rev-parse state (%d lines)", len(lines))
	}
	return st, nil
}

// tolerantRevParse runs a multi-query rev-parse and returns the lines that
// answered successfully. On exit 128 the last stdout line is the failing arg
// echoed back (lab-verified) — dropped after checking it really is one of
// ours, so a genuine failure can't be mistaken for a partial success.
func tolerantRevParse(repo *gitx.Repo, args ...string) ([]string, error) {
	out, err := repo.RunRead(append([]string{"rev-parse"}, args...)...)
	if err != nil {
		var ge *gitx.GitError
		if !errors.As(err, &ge) || ge.ExitCode != 128 || ge.Stdout == "" {
			return nil, err
		}
		out = ge.Stdout
		lines := strings.Split(out, "\n")
		last := lines[len(lines)-1]
		for _, a := range args {
			if a == last {
				return lines[:len(lines)-1], nil
			}
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// readConfig reads every jog-relevant config key in one spawn
// (`--get-regexp`; keys arrive lowercased, `key\nvalue\0` framed, valueless
// booleans as a bare key — lab-verified). Value canonicalization
// (`50m` → bytes, `yes` → true) is done here since --type doesn't combine
// with --get-regexp; exit 1 (no matches) means defaults.
func (e *engine) readConfig() repoConfig {
	cfg := repoConfig{maxFileSize: DefaultMaxFileSize}
	out, err := e.repo.RunRead("config", "-z", "--get-regexp", `^(jog\.|core\.sparsecheckout)`)
	if err != nil {
		return cfg
	}
	for _, ent := range strings.Split(out, "\x00") {
		if ent == "" {
			continue
		}
		key, val, hasVal := strings.Cut(ent, "\n")
		switch key {
		case "jog.maxfilesize":
			if n, ok := gitInt(val); hasVal && ok {
				cfg.maxFileSize = n
			}
		case "core.sparsecheckout":
			cfg.sparse = !hasVal || gitBool(val) // `[core]\n\tsparseCheckout` alone means true
		}
	}
	return cfg
}

// gitInt parses git's integer config syntax: decimal with an optional
// k/m/g suffix (×1024ⁿ, case-insensitive) — matching `--type=int`
// canonicalization ("50m" → 52428800).
func gitInt(s string) (int64, bool) {
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k', 'K':
			mult, s = 1<<10, s[:n-1]
		case 'm', 'M':
			mult, s = 1<<20, s[:n-1]
		case 'g', 'G':
			mult, s = 1<<30, s[:n-1]
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v * mult, true
}

// gitBool parses git's boolean config syntax (true/yes/on, false/no/off,
// empty-string false, nonzero numbers true).
func gitBool(s string) bool {
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off", "":
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n != 0
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

// scanOversize finds changed/untracked files over the size limit from
// already-fetched porcelain status output (plan D2) — pre-scan, because by
// `add` time the blob is already minted into the odb. Returns
// `:(exclude,literal)` pathspecs for the engine's add and the repo-relative
// skip list for the commit body.
func scanOversize(statusOut string, limit int64, top string) (excludes, skipped []string) {
	if limit <= 0 || statusOut == "" {
		return nil, nil
	}
	// -uall (in the caller's status) enumerates files inside untracked
	// directories (a bare `?? dir/` entry would hide oversized files).
	// Porcelain paths are root-relative; top is the toplevel, so Lstat can
	// join directly.
	entries := strings.Split(statusOut, "\x00")
	for i := 0; i < len(entries); i++ {
		ent := entries[i]
		if len(ent) < 4 {
			continue
		}
		x, path := ent[0], ent[3:]
		if x == 'R' || x == 'C' {
			i++ // rename/copy entries carry a second NUL-terminated origin path
		}
		fi, statErr := os.Lstat(filepath.Join(top, path))
		if statErr != nil || !fi.Mode().IsRegular() {
			continue // deleted, symlink, etc. — nothing oversized to stage
		}
		if fi.Size() > limit {
			excludes = append(excludes, ":(exclude,literal)"+path)
			skipped = append(skipped, path)
		}
	}
	return excludes, skipped
}

// ensureGCConfig writes the per-repo keys that stop git's own gc from
// expiring jog reflog entries (per-ref-pattern config wins over globals,
// verified). Written lazily on first refs/jog/* creation (plan D3 — the
// doctor --fix flow, D15, is the explicit front door; this stays as the
// safety net). Best-effort: a failure never fails the snapshot; callers may
// warn via GCConfigured.
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
