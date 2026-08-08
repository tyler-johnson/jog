package snap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

// Test numbering follows the matrix in docs/PLAN-V0.md §5.

func setup(t *testing.T) (*testrepo.Repo, *gitx.Repo) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	tr := testrepo.New(t)
	r, err := gitx.Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return tr, r
}

func take(t *testing.T, r *gitx.Repo, msg string) *Result {
	t.Helper()
	res, err := Take(r, msg)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return res
}

func snapFiles(tr *testrepo.Repo, ref string) string {
	return tr.Git("ls-tree", "-r", "--name-only", ref)
}

// 1 — the core invariant: real index byte-identical across a snapshot; and
// the chain topology: first snap's parent is HEAD, second's are (prev, HEAD).
func TestSnapshotLeavesIndexAlone(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "committed\n")
	head := tr.Commit("first")
	tr.Write("a.txt", "modified\n")
	tr.Write("untracked.txt", "new\n")

	before := tr.IndexBytes()
	res := take(t, r, "manual: test one")
	if !bytes.Equal(before, tr.IndexBytes()) {
		t.Fatal("real index bytes changed across snapshot")
	}
	if res.Commit == "" || res.NoOp {
		t.Fatalf("expected a real snapshot, got %+v", res)
	}
	if !res.FirstSnapshot {
		t.Error("first snapshot not flagged")
	}

	files := snapFiles(tr, res.Ref)
	if !strings.Contains(files, "a.txt") || !strings.Contains(files, "untracked.txt") {
		t.Errorf("snapshot tree = %q, want a.txt and untracked.txt", files)
	}
	if got := tr.Git("show", res.Ref+":a.txt"); got != "modified" {
		t.Errorf("snapshot content = %q, want modified worktree state", got)
	}

	// First snapshot: single parent = HEAD (the base edge).
	if got := tr.Git("log", "-1", "--format=%P", res.Ref); got != head {
		t.Errorf("first snapshot parents = %q, want %q", got, head)
	}
	// Snapshot identity is the fixed jog identity (D1).
	if got := tr.Git("log", "-1", "--format=%cn <%ce>", res.Ref); got != "jog <jog@local>" {
		t.Errorf("committer = %q", got)
	}

	// Second snapshot: parent 1 = previous snap, parent 2 = HEAD.
	tr.Write("a.txt", "modified again\n")
	res2 := take(t, r, "manual: test two")
	if got := tr.Git("log", "-1", "--format=%P", res2.Ref); got != res.Commit+" "+head {
		t.Errorf("second snapshot parents = %q, want %q", got, res.Commit+" "+head)
	}
}

// D1 must survive hostile environments: GIT_COMMITTER_*/GIT_AUTHOR_* env
// overrides git config, so `-c user.*` identity would silently break the
// snaps walk terminator. The engine sets identity via env-on-env instead.
func TestSnapshotIdentityBeatsEnv(t *testing.T) {
	tr, r := setup(t)
	t.Setenv("GIT_AUTHOR_NAME", "evil")
	t.Setenv("GIT_AUTHOR_EMAIL", "evil@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "evil")
	t.Setenv("GIT_COMMITTER_EMAIL", "evil@example.com")
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	res := take(t, r, "manual: identity")
	if got := tr.Git("log", "-1", "--format=%cn <%ce> %an <%ae>", res.Ref); got != "jog <jog@local> jog <jog@local>" {
		t.Errorf("identity = %q, want jog <jog@local> for both roles", got)
	}
}

// 2 — a held .git/index.lock (concurrent git activity) doesn't block
// snapshots: shadow ops never touch the real index lock.
func TestSnapshotWithRealIndexLockHeld(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	lock := filepath.Join(tr.GitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lock)

	res := take(t, r, "manual: under lock")
	if res.Commit == "" {
		t.Fatalf("snapshot failed under real index.lock: %+v", res)
	}
}

// 3 — mid-merge: conflict markers captured; MERGE_HEAD and the real index's
// stage 1/2/3 entries untouched.
func TestSnapshotMidMerge(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "base\n")
	tr.Commit("base")
	tr.Git("checkout", "-q", "-b", "side")
	tr.Write("a.txt", "side change\n")
	tr.Commit("side")
	tr.Git("checkout", "-q", "main")
	tr.Write("a.txt", "main change\n")
	tr.Commit("main change")
	if _, err := tr.TryGit("merge", "side"); err == nil {
		t.Fatal("merge unexpectedly succeeded")
	}
	if tr.Git("ls-files", "-u") == "" {
		t.Fatal("no conflicted stages before snapshot")
	}

	res := take(t, r, "manual: mid-merge")
	if res.Commit == "" {
		t.Fatalf("no snapshot mid-merge: %+v", res)
	}
	if !strings.Contains(tr.Git("show", res.Ref+":a.txt"), "<<<<<<<") {
		t.Error("conflict markers not captured")
	}
	if _, err := os.Stat(filepath.Join(tr.GitDir, "MERGE_HEAD")); err != nil {
		t.Error("MERGE_HEAD disturbed")
	}
	if tr.Git("ls-files", "-u") == "" {
		t.Error("conflicted stages lost from real index")
	}
}

// 4 — unborn HEAD: read-tree HEAD is fatal there; engine starts empty.
// The first snapshot has no parents at all.
func TestSnapshotUnbornHead(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "before first commit\n")

	res := take(t, r, "manual: unborn")
	if res.Commit == "" {
		t.Fatalf("no snapshot on unborn HEAD: %+v", res)
	}
	if got := tr.Git("log", "-1", "--format=%P", res.Ref); got != "" {
		t.Errorf("unborn-HEAD snapshot parents = %q, want none", got)
	}
	if !strings.Contains(snapFiles(tr, res.Ref), "a.txt") {
		t.Error("file missing from unborn-HEAD snapshot")
	}
}

// 5 — detached HEAD lands on the @detached chain.
func TestSnapshotDetachedHead(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Git("checkout", "-q", "--detach")
	tr.Write("b.txt", "detached work\n")

	res := take(t, r, "manual: detached")
	if res.Ref != "refs/jog/@detached" {
		t.Errorf("ref = %q, want refs/jog/@detached", res.Ref)
	}
	tr.Git("rev-parse", "--verify", "refs/jog/@detached")
}

// 6 — sparse checkout: sparse-excluded files must not be silently dropped
// (the unseeded-shadow gotcha); the re-seed path preserves them from HEAD.
func TestSnapshotSparseCheckout(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "root file\n")
	tr.Write("sub/b.txt", "in cone\n")
	tr.Commit("first")
	tr.Git("sparse-checkout", "set", "--no-cone", "sub")
	if _, err := os.Stat(filepath.Join(tr.Dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("sparse checkout did not drop a.txt from the worktree")
	}
	tr.Write("sub/b.txt", "changed\n")

	res := take(t, r, "manual: sparse")
	files := snapFiles(tr, res.Ref)
	if !strings.Contains(files, "a.txt") {
		t.Errorf("sparse-excluded a.txt dropped from snapshot; tree = %q", files)
	}
	if got := tr.Git("show", res.Ref+":sub/b.txt"); got != "changed" {
		t.Errorf("in-cone change not captured: %q", got)
	}
}

// 7 — .gitignore and info/exclude are respected; ignored files never
// snapshotted (by design).
func TestSnapshotRespectsIgnores(t *testing.T) {
	tr, r := setup(t)
	tr.Write(".gitignore", "ignored.txt\n")
	tr.Write("keep.txt", "x\n")
	tr.Commit("first")
	tr.Write("ignored.txt", "secret\n")
	tr.Write("excluded.txt", "also out\n")
	tr.Write(filepath.Join(".git", "info", "exclude"), "excluded.txt\n")

	res := take(t, r, "manual: ignores")
	files := snapFiles(tr, res.Ref)
	if strings.Contains(files, "ignored.txt") || strings.Contains(files, "excluded.txt") {
		t.Errorf("ignored files captured: %q", files)
	}
	if !strings.Contains(files, "keep.txt") {
		t.Errorf("tracked file missing: %q", files)
	}
}

// 8 — embedded repos become gitlinks (160000); inner files not captured.
func TestSnapshotEmbeddedRepo(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("inner/f.txt", "inner content\n")
	tr.Git("-C", "inner", "init", "-q")
	tr.Git("-C", "inner", "add", "-A")
	tr.Git("-C", "inner", "commit", "-q", "-m", "inner commit")

	res := take(t, r, "manual: embedded")
	entry := tr.Git("ls-tree", res.Ref, "inner")
	if !strings.HasPrefix(entry, "160000 commit") {
		t.Errorf("inner repo entry = %q, want gitlink (160000 commit)", entry)
	}
	if strings.Contains(snapFiles(tr, res.Ref), "inner/f.txt") {
		t.Error("embedded repo's inner files captured; should be a bare gitlink")
	}
}

// 9 — exec bit and symlinks preserved (100755 / 120000).
func TestSnapshotModes(t *testing.T) {
	tr, r := setup(t)
	tr.Write("script.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(tr.Dir, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("script.sh", filepath.Join(tr.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	tr.Commit("first") // snapshot a born repo; modes come through either way
	tr.Write("note.txt", "trigger a change\n")

	res := take(t, r, "manual: modes")
	tree := tr.Git("ls-tree", res.Ref)
	if !strings.Contains(tree, "100755 blob") {
		t.Errorf("exec bit lost: %q", tree)
	}
	if !strings.Contains(tree, "120000 blob") {
		t.Errorf("symlink lost: %q", tree)
	}
}

// 10 — no-op fast path: identical tree mints nothing and leaves the ref and
// reflog alone.
func TestSnapshotNoOp(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	res1 := take(t, r, "manual: real")
	res2 := take(t, r, "manual: nothing changed")
	if !res2.NoOp || res2.Commit != "" {
		t.Fatalf("second snapshot = %+v, want NoOp", res2)
	}
	if got := tr.Git("rev-parse", res1.Ref); got != res1.Commit {
		t.Error("ref moved on a no-op")
	}
	if _, err := tr.TryGit("rev-parse", "-q", "--verify", res1.Ref+"@{1}"); err == nil {
		t.Error("no-op added a reflog entry")
	}
}

// 11a — shadow-index contention: a held shadow lock means skip (Contended),
// never a failure or a hang.
func TestSnapshotShadowLockContention(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	if err := os.MkdirAll(filepath.Join(tr.GitDir, "jog"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(tr.GitDir, "jog", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	res := take(t, r, "manual: contended")
	if !res.Contended {
		t.Fatalf("result = %+v, want Contended", res)
	}

	// Lock released → next snapshot proceeds normally.
	os.Remove(lock)
	if res := take(t, r, "manual: after lock"); res.Commit == "" {
		t.Fatalf("post-contention snapshot failed: %+v", res)
	}
}

// 11b — the CAS mechanic itself: a stale old value must fail, not clobber.
func TestUpdateRefCAS(t *testing.T) {
	tr, _ := setup(t)
	tr.Write("a.txt", "1\n")
	shaA := tr.Commit("a")
	tr.Write("a.txt", "2\n")
	shaB := tr.Commit("b")

	tr.Git("update-ref", "refs/jog/castest", shaA, "")
	if _, err := tr.TryGit("update-ref", "refs/jog/castest", shaB, shaB); err == nil {
		t.Fatal("update-ref with stale old value succeeded; CAS is broken")
	}
	if got := tr.Git("rev-parse", "refs/jog/castest"); got != shaA {
		t.Errorf("ref clobbered to %s", got)
	}
}

// 12 — reflog: created via --create-reflog (refs outside refs/heads get none
// by default, verified), entries carry provenance, @{N} resolves.
func TestSnapshotReflog(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "1\n")
	res1 := take(t, r, "manual: snap one")
	tr.Write("b.txt", "2\n")
	take(t, r, "pre: git rebase main")

	if got := tr.Git("rev-parse", res1.Ref+"@{1}"); got != res1.Commit {
		t.Errorf("@{1} = %s, want first snapshot %s", got, res1.Commit)
	}
	subjects := tr.Git("log", "-g", "--format=%gs", res1.Ref)
	if !strings.Contains(subjects, "manual: snap one") || !strings.Contains(subjects, "pre: git rebase main") {
		t.Errorf("reflog subjects = %q, want provenance messages", subjects)
	}
}

// 13 — gc survival: snapshots reachable only via refs/jog/* survive
// `git gc --prune=now`, and the per-repo reflog-expire keys are set on first
// snapshot (D3).
func TestSnapshotSurvivesGC(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("precious.txt", "deleted later\n")

	res := take(t, r, "manual: before delete")
	if !res.GCConfigured {
		t.Error("gc reflog-expire config not written on first snapshot")
	}
	if got := tr.Git("config", "--local", "--get", "gc.refs/jog/*.reflogExpire"); got != "never" {
		t.Errorf("reflogExpire = %q, want never", got)
	}

	tr.Remove("precious.txt")
	tr.Git("gc", "--prune=now", "--quiet")
	if got := tr.Git("show", res.Ref+":precious.txt"); got != "deleted later" {
		t.Errorf("content lost after gc: %q", got)
	}
}

// 14 — jog.maxFileSize: oversized new files are skipped, warned about in the
// commit body, and reported in the Result; everything else is captured.
func TestSnapshotMaxFileSize(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Git("config", "jog.maxFileSize", "1024")
	tr.Write("big.bin", strings.Repeat("B", 2048))
	tr.Write("small.txt", "fits\n")

	res := take(t, r, "manual: big file")
	if len(res.SkippedFiles) != 1 || res.SkippedFiles[0] != "big.bin" {
		t.Fatalf("SkippedFiles = %v, want [big.bin]", res.SkippedFiles)
	}
	files := snapFiles(tr, res.Ref)
	if strings.Contains(files, "big.bin") {
		t.Error("oversized file captured despite limit")
	}
	if !strings.Contains(files, "small.txt") {
		t.Error("small file missing")
	}
	body := tr.Git("log", "-1", "--format=%b", res.Ref)
	if !strings.Contains(body, SkippedHeader) || !strings.Contains(body, "big.bin") {
		t.Errorf("commit body = %q, want skipped list", body)
	}
}

// 19 — branch names with slashes make valid chain refs.
func TestSnapshotSlashBranch(t *testing.T) {
	tr, r := setup(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Git("checkout", "-q", "-b", "feature/x")
	tr.Write("b.txt", "y\n")

	res := take(t, r, "manual: slash branch")
	if res.Ref != "refs/jog/feature/x" {
		t.Errorf("ref = %q, want refs/jog/feature/x", res.Ref)
	}
	tr.Git("rev-parse", "--verify", "refs/jog/feature/x")
}

// Bare repos: nothing to snapshot, distinct error.
func TestSnapshotBareRepo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	r0 := &gitx.Repo{WorkDir: dir}
	if _, err := r0.Run("init", "-q", "--bare", "."); err != nil {
		t.Fatal(err)
	}
	r, err := gitx.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Take(r, "manual: bare"); !errors.Is(err, ErrBareRepo) {
		t.Fatalf("err = %v, want ErrBareRepo", err)
	}
}

// 20 — perf smoke (informational, non-gating): warm no-op timing on a
// many-file repo. Budget from DESIGN §4: no-op ≤ 30 ms.
func TestNoOpPerf(t *testing.T) {
	if testing.Short() {
		t.Skip("perf smoke skipped in -short")
	}
	tr, r := setup(t)
	for i := 0; i < 2000; i++ {
		tr.Write(filepath.Join("src", string(rune('a'+i%26)), fmt.Sprintf("f%04d.txt", i)), "content\n")
	}
	tr.Commit("many files")
	take(t, r, "manual: warm the shadow index")

	const rounds = 3
	start := time.Now()
	for i := 0; i < rounds; i++ {
		if res := take(t, r, "manual: noop"); !res.NoOp {
			t.Fatalf("expected NoOp, got %+v", res)
		}
	}
	noop := time.Since(start) / rounds

	// The design's budget (no-op ≤ 30 ms) is calibrated against warm
	// `git status` ≈ 20–25 ms on a 5k-file repo — log both so the numbers
	// are comparable across hardware.
	start = time.Now()
	for i := 0; i < rounds; i++ {
		tr.Git("--no-optional-locks", "status", "--porcelain")
	}
	status := time.Since(start) / rounds
	t.Logf("warm no-op snapshot: %v avg; git status baseline: %v avg (%d rounds; budget: no-op ≤ 30ms)",
		noop, status, rounds)
}
