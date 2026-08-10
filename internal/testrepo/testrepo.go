// Package testrepo provides throwaway git repositories for integration tests.
//
// Tests drive the real git binary, never a reimplementation — jog's design
// verification applies to real git (docs/DESIGN.md §2), so its tests must too.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a temporary git repository rooted at Dir. It is created with a
// clean environment: the host's global and system git config are masked so
// tests behave identically on every machine.
type Repo struct {
	t      *testing.T
	Dir    string // worktree root
	GitDir string // absolute git dir (Dir/.git for a primary worktree)
	env    []string
}

// New creates an empty repository on branch main. HEAD is unborn until the
// first Commit.
func New(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	r := &Repo{
		t:   t,
		Dir: dir,
		env: append(os.Environ(),
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_SYSTEM=" + os.DevNull,
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		),
	}
	r.Git("init", "-q", "-b", "main")
	r.GitDir = r.Git("rev-parse", "--absolute-git-dir")
	return r
}

// Git runs git in the worktree and returns trimmed stdout, failing the test
// on a non-zero exit.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	out, err := r.TryGit(args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// TryGit runs git in the worktree and returns trimmed combined output and the
// error, for tests that expect failure.
func (r *Repo) TryGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// GitEnv runs git with extra environment entries appended after the
// fixture's own (last wins), for tests needing controlled identities,
// dates, or index files. Fails the test on non-zero exit.
func (r *Repo) GitEnv(env []string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(append([]string{}, r.env...), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write creates or overwrites a file (path relative to the worktree),
// creating parent directories as needed.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	abs := filepath.Join(r.Dir, path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Remove deletes a file from the worktree.
func (r *Repo) Remove(path string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.Dir, path)); err != nil {
		r.t.Fatal(err)
	}
}

// Commit stages everything and commits, returning the new HEAD sha.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}

// IndexBytes returns the raw contents of the real index file, for
// byte-identity assertions (safety invariant 1). Returns nil if no index
// exists yet.
func (r *Repo) IndexBytes() []byte {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.GitDir, "index"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}
