package gitx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/testrepo"
)

// Mask host git config so gitx-spawned commands behave identically on every
// machine (testrepo does the same for its own commands).
func maskHostConfig(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func TestDiscover(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)

	r, err := Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.GitDir != tr.GitDir {
		t.Errorf("GitDir = %q, want %q", r.GitDir, tr.GitDir)
	}
	if r.Bare {
		t.Error("repo reported bare")
	}
	top := tr.Git("rev-parse", "--show-toplevel")
	if r.Top != top {
		t.Errorf("Top = %q, want %q", r.Top, top)
	}

	// From a subdirectory: same repo, WorkDir preserved, Top still the root.
	tr.Write("sub/f.txt", "x\n")
	sub := filepath.Join(tr.Dir, "sub")
	r2, err := Discover(sub)
	if err != nil {
		t.Fatal(err)
	}
	if r2.GitDir != tr.GitDir || r2.WorkDir != sub {
		t.Errorf("subdir discovery: GitDir=%q WorkDir=%q", r2.GitDir, r2.WorkDir)
	}
	if r2.Top != top {
		t.Errorf("subdir Top = %q, want %q", r2.Top, top)
	}
}

func TestDiscoverNotARepo(t *testing.T) {
	maskHostConfig(t)
	_, err := Discover(t.TempDir())
	if !errors.Is(err, ErrNotARepo) {
		t.Fatalf("err = %v, want ErrNotARepo", err)
	}
}

func TestDiscoverLinkedWorktree(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)
	tr.Write("a.txt", "hello\n")
	tr.Commit("first")

	wt := filepath.Join(t.TempDir(), "wt")
	tr.Git("worktree", "add", "-q", "-b", "side", wt)

	r, err := Discover(wt)
	if err != nil {
		t.Fatal(err)
	}
	// Linked worktrees get a per-worktree git dir — this is what keeps the
	// shadow index (and refs/worktree/* if ever needed) from being shared.
	// git prints forward-slash paths on every OS — compare in that form.
	if !strings.Contains(filepath.ToSlash(r.GitDir), "/worktrees/") {
		t.Errorf("linked worktree GitDir = %q, want a .git/worktrees/<name> path", r.GitDir)
	}
	if r.GitDir == tr.GitDir {
		t.Error("linked worktree shares GitDir with primary worktree")
	}
}

func TestDiscoverBare(t *testing.T) {
	maskHostConfig(t)
	dir := t.TempDir()
	r0 := &Repo{WorkDir: dir}
	if _, err := r0.Run("init", "-q", "--bare", "."); err != nil {
		t.Fatal(err)
	}
	r, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Bare {
		t.Error("bare repo not reported bare")
	}
	// --show-toplevel is fatal in a bare repo; Discover recovers the
	// partial answer and leaves Top empty.
	if r.Top != "" {
		t.Errorf("bare Top = %q, want empty", r.Top)
	}
}

// ReadRef must agree with git across the files backend's layouts: loose,
// packed, loose-shadows-stale-pack, and definitive absence.
func TestReadRef(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	sha1 := tr.Commit("first")
	r, err := Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	check := func(scenario, name, want string) {
		t.Helper()
		got, ok := r.ReadRef(name)
		if !ok || got != want {
			t.Errorf("%s: ReadRef(%s) = %q, %v; want %q, true", scenario, name, got, ok, want)
		}
	}

	tr.Git("update-ref", "refs/jog/main", sha1)
	check("loose", "refs/jog/main", sha1)
	check("absent, no packed-refs", "refs/jog/nope", "")

	tr.Git("pack-refs", "--all")
	check("packed", "refs/jog/main", sha1)
	check("absent, packed-refs present", "refs/jog/nope", "")

	// update-ref writes a loose file and leaves the packed entry stale —
	// the loose value must win.
	tr.Write("a.txt", "y\n")
	sha2 := tr.Commit("second")
	tr.Git("update-ref", "refs/jog/main", sha2)
	check("loose shadows stale pack", "refs/jog/main", sha2)

	// A symref file is not a sha — git's to interpret, never guessed.
	tr.Git("symbolic-ref", "refs/heads/alias", "refs/heads/main")
	if _, ok := r.ReadRef("refs/heads/alias"); ok {
		t.Error("ReadRef answered for a symref")
	}
}

// ReadRef from a linked worktree must find shared refs in the common dir,
// not the per-worktree git dir.
func TestReadRefLinkedWorktree(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	sha := tr.Commit("first")

	wt := filepath.Join(t.TempDir(), "wt")
	tr.Git("worktree", "add", "-q", "-b", "side", wt)
	r, err := Discover(wt)
	if err != nil {
		t.Fatal(err)
	}

	if got := r.CommonDir(); got != tr.GitDir {
		t.Errorf("CommonDir = %q, want %q", got, tr.GitDir)
	}
	if got, ok := r.ReadRef("refs/heads/main"); !ok || got != sha {
		t.Errorf("ReadRef(refs/heads/main) = %q, %v; want %q, true", got, ok, sha)
	}
}

func TestHeadSHA(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)
	r, err := Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	// Unborn: definitively no commit.
	if got, ok := r.HeadSHA(); !ok || got != "" {
		t.Errorf("unborn HeadSHA = %q, %v; want \"\", true", got, ok)
	}

	tr.Write("a.txt", "x\n")
	sha := tr.Commit("first")
	if got, ok := r.HeadSHA(); !ok || got != sha {
		t.Errorf("born HeadSHA = %q, %v; want %q, true", got, ok, sha)
	}

	tr.Git("checkout", "-q", "--detach")
	if got, ok := r.HeadSHA(); !ok || got != sha {
		t.Errorf("detached HeadSHA = %q, %v; want %q, true", got, ok, sha)
	}
}

// In a reftable repo every native read must decline — refs live in
// reftable/, and the HEAD file is a `refs/heads/.invalid` stub
// (lab-verified) — while the spawn fallbacks answer correctly.
func TestNativeReadsDeclineReftable(t *testing.T) {
	maskHostConfig(t)
	dir := t.TempDir()
	r0 := &Repo{WorkDir: dir}
	if _, err := r0.Run("init", "-q", "-b", "main", "--ref-format=reftable", "."); err != nil {
		t.Skip("git without reftable support")
	}
	r, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	ident := r.WithEnv(
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if _, err := ident.Run("commit", "-q", "--allow-empty", "-m", "first"); err != nil {
		t.Fatal(err)
	}

	if _, ok := r.ReadRef("refs/heads/main"); ok {
		t.Error("ReadRef answered natively in a reftable repo")
	}
	if _, ok := r.HeadSHA(); ok {
		t.Error("HeadSHA answered natively in a reftable repo")
	}
	// The stub must not leak as a branch name; the spawn resolves it.
	if branch, detached := r.HeadBranch(); detached || branch != "main" {
		t.Errorf("HeadBranch = %q, %v; want main, false", branch, detached)
	}
}

func TestRunError(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)
	r, err := Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.Run("rev-parse", "--verify", "definitely-not-a-ref")
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %T, want *GitError", err)
	}
	if ge.ExitCode == 0 || ge.Stderr == "" {
		t.Errorf("GitError = %+v, want non-zero exit and stderr", ge)
	}
}

func TestWithIndexShadowsRealIndex(t *testing.T) {
	maskHostConfig(t)
	tr := testrepo.New(t)
	tr.Write("a.txt", "hello\n")
	tr.Commit("first")
	tr.Write("b.txt", "new file\n")

	r, err := Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	before := tr.IndexBytes()
	shadowPath := filepath.Join(t.TempDir(), "shadow-index")
	shadow := r.WithIndex(shadowPath)

	// The M2 engine's write sequence, in miniature: stage everything into
	// the shadow index and write a tree. The real index must not move.
	if _, err := shadow.Run("read-tree", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := shadow.Run("add", "-A"); err != nil {
		t.Fatal(err)
	}
	tree, err := shadow.Run("write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if tree == "" {
		t.Fatal("write-tree produced no tree")
	}
	if _, err := os.Stat(shadowPath); err != nil {
		t.Fatalf("shadow index file: %v", err)
	}
	if !bytes.Equal(before, tr.IndexBytes()) {
		t.Fatal("real index bytes changed under shadow-index operations")
	}

	// The staged file must be in the shadow tree.
	if out, err := shadow.RunRead("ls-tree", "--name-only", tree); err != nil || !strings.Contains(out, "b.txt") {
		t.Fatalf("ls-tree = %q (%v), want to include b.txt", out, err)
	}

	// The original repo handle must be unaffected by the derived one.
	if len(r.extraEnv) != 0 {
		t.Error("WithIndex mutated the parent repo's env")
	}
}

func TestWithIndexRejectsRelativePath(t *testing.T) {
	r := &Repo{WorkDir: "."}
	defer func() {
		if recover() == nil {
			t.Fatal("WithIndex accepted a relative path; relative GIT_INDEX_FILE resolves inside the worktree")
		}
	}()
	r.WithIndex("jog/index")
}
