// Package gitx is jog's git subprocess layer.
//
// jog shells out to the system git binary, never a reimplementation — the
// design's lab verification applies to real git (docs/DESIGN.md §2). This
// package owns repo discovery, command execution, and the two environment
// rules everything above it relies on: reads carry --no-optional-locks
// (safety invariant 5), and shadow-index work happens via GIT_INDEX_FILE
// without ever touching the real index.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotARepo reports that a directory is not inside a git repository.
// Hook entry points check for it with errors.Is and exit 0 silently; user
// commands surface it.
var ErrNotARepo = errors.New("not a git repository")

// Repo is a discovered repository context. Commands run with the working
// directory jog was invoked from (or, for hooks, the cwd from the payload).
type Repo struct {
	// GitDir is the absolute git directory. In a linked worktree this is
	// .git/worktrees/<name> — per-worktree by construction, which is what
	// keeps the shadow index from being shared across worktrees.
	GitDir string
	// WorkDir is the directory commands run in.
	WorkDir string
	// Bare reports a bare repository (no worktree; nothing to snapshot).
	Bare bool

	extraEnv []string
}

// Discover locates the repository containing dir. Returns ErrNotARepo
// (wrapped) when dir is not inside one.
func Discover(dir string) (*Repo, error) {
	r := &Repo{WorkDir: dir}
	out, err := r.Run("rev-parse", "--absolute-git-dir", "--is-bare-repository")
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "not a git repository") {
			return nil, fmt.Errorf("%w: %s", ErrNotARepo, dir)
		}
		return nil, err
	}
	gitDir, bare, ok := strings.Cut(out, "\n")
	if !ok {
		return nil, fmt.Errorf("unexpected rev-parse output: %q", out)
	}
	r.GitDir = gitDir
	r.Bare = strings.TrimSpace(bare) == "true"
	return r, nil
}

// Run executes git with args and returns trimmed stdout. Non-zero exits
// return a *GitError carrying the exit code and stderr.
func (r *Repo) Run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.WorkDir
	cmd.Env = append(os.Environ(), r.extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		code := -1 // start failure (e.g. git not on PATH); no exit code exists
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return "", &GitError{
			Args:     args,
			ExitCode: code,
			Stderr:   strings.TrimSpace(stderr.String()),
			cause:    err,
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunRead is Run for read-only commands: it prepends --no-optional-locks so
// git never opportunistically rewrites the user's index (plain `git status`
// does — lab-verified). Every jog read goes through here, no exceptions.
func (r *Repo) RunRead(args ...string) (string, error) {
	return r.Run(append([]string{"--no-optional-locks"}, args...)...)
}

// WithIndex returns a copy of the repo whose commands run against the given
// index file via GIT_INDEX_FILE — the shadow-index mechanism. The path must
// be absolute: a relative GIT_INDEX_FILE resolves inside the worktree
// (lab-verified gotcha), which would scatter index files into user projects.
func (r *Repo) WithIndex(indexPath string) *Repo {
	if !filepath.IsAbs(indexPath) {
		panic("gitx: GIT_INDEX_FILE must be absolute; relative resolves inside the worktree")
	}
	c := *r
	c.extraEnv = append(append([]string{}, r.extraEnv...), "GIT_INDEX_FILE="+indexPath)
	return &c
}

// GitError is a non-zero git exit.
type GitError struct {
	Args     []string
	ExitCode int
	Stderr   string
	cause    error
}

func (e *GitError) Error() string {
	msg := fmt.Sprintf("git %s: exit %d", strings.Join(e.Args, " "), e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *GitError) Unwrap() error { return e.cause }
