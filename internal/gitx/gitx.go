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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotARepo reports that a directory is not inside a git repository.
// Hook entry points check for it with errors.Is and exit 0 silently; user
// commands surface it.
var ErrNotARepo = errors.New("not a git repository")

// Bin is the git executable jog runs: $JOG_GIT when set (a name looked
// up on PATH, or a path to the binary), plain "git" from PATH otherwise.
// An env var rather than git config on purpose — jog's settings live in
// git config, which takes a working git to read.
func Bin() string {
	if g := os.Getenv("JOG_GIT"); g != "" {
		return g
	}
	return "git"
}

// Look resolves Bin to an executable path, with an error message that
// says where the bad value came from.
func Look() (string, error) {
	p, err := exec.LookPath(Bin())
	if err == nil {
		return p, nil
	}
	if g := os.Getenv("JOG_GIT"); g != "" {
		return "", fmt.Errorf("git not found at %q (set by $JOG_GIT)", g)
	}
	return "", errors.New("git not found on PATH")
}

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
	cmd := exec.Command(Bin(), args...)
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
			Stdout:   strings.TrimSpace(stdout.String()),
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

// RunReadStdin is RunRead with input fed to git's stdin — for plumbing
// that takes object lists via --stdin, where a long chain's worth of shas
// would overflow argv.
func (r *Repo) RunReadStdin(stdin string, args ...string) (string, error) {
	args = append([]string{"--no-optional-locks"}, args...)
	cmd := exec.Command(Bin(), args...)
	cmd.Dir = r.WorkDir
	cmd.Env = append(os.Environ(), r.extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return "", &GitError{
			Args:     args,
			ExitCode: code,
			Stdout:   strings.TrimSpace(stdout.String()),
			Stderr:   strings.TrimSpace(stderr.String()),
			cause:    err,
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunReadLoud is RunRead with git's stderr passed through to the user — for
// commands whose warnings matter even on success (e.g. the reflog time-query
// falling back to the oldest entry, which git reports on stderr with exit 0).
func (r *Repo) RunReadLoud(args ...string) (string, error) {
	cmd := exec.Command(Bin(), append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = r.WorkDir
	cmd.Env = append(os.Environ(), r.extraEnv...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return "", &GitError{Args: args, ExitCode: code, cause: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// StartRead starts a read command and returns its stdout for streaming, for
// walks that terminate early (e.g. the snaps chain-boundary scan) — the
// caller reads what it needs, then kills and waits on the returned cmd.
func (r *Repo) StartRead(args ...string) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.Command(Bin(), append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = r.WorkDir
	cmd.Env = append(os.Environ(), r.extraEnv...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, out, nil
}

// HeadBranch returns the current branch's short name, or detached=true on a
// detached HEAD. It reads $GIT_DIR/HEAD directly — the format is documented
// and stable (gitrepository-layout(5): a `ref:` symref line or a raw object
// id), and this sits on the snapshot hot path where a spawn costs more than
// the whole file read. Anything unexpected falls back to a symbolic-ref
// spawn, keeping the fast read an optimization, never a semantic.
func (r *Repo) HeadBranch() (branch string, detached bool) {
	if b, err := os.ReadFile(filepath.Join(r.GitDir, "HEAD")); err == nil {
		s := strings.TrimSpace(string(b))
		if rest, ok := strings.CutPrefix(s, "ref: "); ok {
			if br, ok := strings.CutPrefix(rest, "refs/heads/"); ok && br != "" {
				return br, false
			}
			// Symref outside refs/heads — rare; let git interpret it.
		} else if isHex(s) {
			return "", true
		}
	}
	out, err := r.RunRead("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", true
	}
	return out, false
}

func isHex(s string) bool {
	if len(s) < 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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

// WithEnv returns a copy of the repo whose commands run with the given
// KEY=VALUE pairs appended. exec.Cmd keeps the last value for duplicate
// keys, so these override anything inherited from the process environment.
func (r *Repo) WithEnv(kv ...string) *Repo {
	c := *r
	c.extraEnv = append(append([]string{}, r.extraEnv...), kv...)
	return &c
}

// GitError is a non-zero git exit. Stdout carries whatever the command
// printed before failing — some commands (rev-parse with several queries)
// emit useful partial results ahead of a fatal, and the engine's batched
// state read depends on recovering them.
type GitError struct {
	Args     []string
	ExitCode int
	Stdout   string
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
