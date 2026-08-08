package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/testrepo"
)

// End-to-end tests run the compiled binary: passthrough replaces the process
// via exec, so it can only be observed from outside.

var jogBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jogbin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	jogBin = filepath.Join(dir, "jog")
	out, err := exec.Command("go", "build", "-o", jogBin, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building jog: %v\n%s", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func runJog(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runJogStdin(t, dir, "", args...)
}

func runJogStdin(t *testing.T, dir, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(jogBin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code = 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running jog %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return so.String(), se.String(), code
}

func TestBareJogSnapshots(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("untracked.txt", "new\n")

	stdout, _, code := runJog(t, tr.Dir)
	if code != 0 {
		t.Fatalf("bare jog exited %d", code)
	}
	if !strings.Contains(stdout, "snapshot ") || !strings.Contains(stdout, " on main") {
		t.Errorf("stdout = %q", stdout)
	}
	// D6: after snapshotting, bare jog shows the top of the timeline.
	if !strings.Contains(stdout, "ago  manual") {
		t.Errorf("bare jog missing recent-timeline readout: %q", stdout)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "manual" {
		t.Errorf("provenance = %q, want manual", got)
	}

	// -m attaches detail; unchanged tree reports no-op.
	stdout, _, code = runJog(t, tr.Dir, "-m", "before surgery")
	if code != 0 || !strings.Contains(stdout, "no changes") {
		t.Errorf("no-op run: code=%d stdout=%q", code, stdout)
	}
	tr.Write("untracked.txt", "changed\n")
	_, _, _ = runJog(t, tr.Dir, "-m", "before surgery")
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "manual: before surgery" {
		t.Errorf("provenance = %q", got)
	}
}

// Matrix row 17a — passthrough propagates git's own exit code.
func TestPassthroughExitCode(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")

	_, stderr, code := runJog(t, tr.Dir, "rev-parse", "--verify", "definitely-missing")
	if code != 128 {
		t.Errorf("exit = %d, want git's 128; stderr = %q", code, stderr)
	}
	tr.Write("b.txt", "change so the next snapshot isn't a no-op\n")
	_, _, code = runJog(t, tr.Dir, "status", "--porcelain")
	if code != 0 {
		t.Errorf("git status via passthrough exited %d", code)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: git status --porcelain" {
		t.Errorf("passthrough provenance = %q", got)
	}
}

// Matrix row 17b — outside a repo, passthrough is exactly git: no snapshot,
// no jog noise, git's own behavior and exit codes.
func TestPassthroughOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runJog(t, dir, "version")
	if code != 0 || !strings.Contains(stdout, "git version") {
		t.Errorf("git version: code=%d stdout=%q", code, stdout)
	}
	if strings.Contains(stderr, "jog") {
		t.Errorf("jog noise outside a repo: %q", stderr)
	}

	_, stderr, code = runJog(t, dir, "status")
	if code != 128 || !strings.Contains(stderr, "not a git repository") {
		t.Errorf("git status outside repo: code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "jog") {
		t.Errorf("jog noise on failing git outside a repo: %q", stderr)
	}
}

// The reason jog exists: the snapshot lands causally BEFORE a destructive
// command, so the destroyed state is recoverable.
func TestPassthroughSnapshotsBeforeDestruction(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "committed\n")
	tr.Commit("first")
	tr.Write("a.txt", "precious uncommitted work\n")

	_, _, code := runJog(t, tr.Dir, "checkout", "--", ".")
	if code != 0 {
		t.Fatalf("checkout exited %d", code)
	}
	// git clobbered the worktree...
	if got := tr.Git("show", "HEAD:a.txt"); got != "committed" {
		t.Fatalf("unexpected HEAD content %q", got)
	}
	data, err := os.ReadFile(filepath.Join(tr.Dir, "a.txt"))
	if err != nil || string(data) != "committed\n" {
		t.Fatalf("worktree not clobbered as expected: %q (%v)", data, err)
	}
	// ...but the snapshot holds the pre-destruction state.
	if got := tr.Git("show", "refs/jog/main:a.txt"); got != "precious uncommitted work" {
		t.Errorf("snapshot content = %q; pre-destruction state lost", got)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: git checkout -- ." {
		t.Errorf("provenance = %q", got)
	}
}

// Under the alias (JOG_AS_GIT=1), help must reach real git; typed directly,
// `jog -h` shows jog's own usage.
func TestHelp(t *testing.T) {
	dir := t.TempDir()
	for _, arg := range []string{"-h", "--help", "help"} {
		stdout, _, code := runJog(t, dir, arg)
		if code != 0 || !strings.Contains(stdout, "jog — a memory for your working tree") {
			t.Errorf("jog %s: code=%d stdout=%q...", arg, code, stdout[:min(80, len(stdout))])
		}
	}

	cmd := exec.Command(jogBin, "help")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JOG_AS_GIT=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "usage: git") {
		t.Errorf("aliased git help: err=%v stdout=%q...", err, string(out[:min(80, len(out))]))
	}
}

// Matrix row 16 + M5: the timeline shows all snapshots, newest first, stops
// at the real-history boundary, filters by path, and snapshots before
// reading (jj-style).
func TestSnaps(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("real history commit")
	tr.Write("a.txt", "two\n")
	runJog(t, tr.Dir, "-m", "checkpoint one")
	tr.Write("b.txt", "new\n")
	runJog(t, tr.Dir, "-m", "checkpoint two")

	stdout, stderr, code := runJog(t, tr.Dir, "snaps")
	if code != 0 {
		t.Fatalf("snaps exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "manual: checkpoint one") || !strings.Contains(stdout, "manual: checkpoint two") {
		t.Errorf("timeline missing snapshots:\n%s", stdout)
	}
	if !strings.Contains(stdout, "b.txt") {
		t.Errorf("files-changed detail missing:\n%s", stdout)
	}
	// Row 16: the first-parent walk must stop at the chain boundary, not
	// run into real history.
	if strings.Contains(stdout, "real history commit") {
		t.Errorf("timeline walked into real history:\n%s", stdout)
	}
	if strings.Index(stdout, "checkpoint two") > strings.Index(stdout, "checkpoint one") {
		t.Errorf("timeline not newest-first:\n%s", stdout)
	}

	// Path filter: only entries touching b.txt.
	stdout, _, _ = runJog(t, tr.Dir, "snaps", "b.txt")
	if !strings.Contains(stdout, "checkpoint two") || strings.Contains(stdout, "checkpoint one") {
		t.Errorf("path filter wrong:\n%s", stdout)
	}

	// -p appends patches.
	stdout, _, _ = runJog(t, tr.Dir, "snaps", "-p")
	if !strings.Contains(stdout, "diff --git") || !strings.Contains(stdout, "+new") {
		t.Errorf("-p missing patches:\n%s", stdout)
	}

	// Reading snapshots first: a dirty tree lands on the timeline before
	// it is displayed.
	tr.Write("a.txt", "three\n")
	stdout, _, _ = runJog(t, tr.Dir, "snaps")
	if !strings.Contains(stdout, "pre: jog snaps") {
		t.Errorf("snaps did not snapshot before reading:\n%s", stdout)
	}
}

// Hook entry points always exit 0 (row 18) — misconfigured invocations
// included, since a non-zero exit would block the user's tool call.
func TestHookAlwaysExitsZero(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	json := `{"hook_event_name":"PreToolUse","session_id":"e2e-sess-id","cwd":` +
		strconvQuote(tr.Dir) + `,"tool_name":"Bash","tool_input":{"command":"make build"}}`
	stdout, _, code := runJogStdin(t, tr.Dir, json, "hook", "claude")
	if code != 0 || stdout != "" {
		t.Errorf("hook claude: code=%d stdout=%q (stdout must stay empty)", code, stdout)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "claude[e2e-sess]: Bash(make build)" {
		t.Errorf("provenance = %q", got)
	}

	if _, _, code := runJogStdin(t, tr.Dir, "garbage", "hook", "claude"); code != 0 {
		t.Errorf("hook claude with garbage stdin exited %d", code)
	}
	if _, _, code := runJog(t, tr.Dir, "hook"); code != 0 {
		t.Errorf("bare `jog hook` exited %d", code)
	}
	if _, _, code := runJog(t, tr.Dir, "hook", "unknown-adapter"); code != 0 {
		t.Errorf("`jog hook unknown-adapter` exited %d", code)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestReservedVerbStub(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runJog(t, dir, "trim")
	if code != 1 || !strings.Contains(stderr, "reserved") {
		t.Errorf("trim stub: code=%d stderr=%q", code, stderr)
	}
}
