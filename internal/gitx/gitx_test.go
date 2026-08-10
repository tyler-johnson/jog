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

	// From a subdirectory: same repo, WorkDir preserved.
	tr.Write("sub/f.txt", "x\n")
	sub := filepath.Join(tr.Dir, "sub")
	r2, err := Discover(sub)
	if err != nil {
		t.Fatal(err)
	}
	if r2.GitDir != tr.GitDir || r2.WorkDir != sub {
		t.Errorf("subdir discovery: GitDir=%q WorkDir=%q", r2.GitDir, r2.WorkDir)
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
	if !strings.Contains(r.GitDir, string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
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
