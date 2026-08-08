package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

// runJogAsGit invokes jog the way the alias does (`jog git …`).
func runJogAsGit(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runJogStdin(t, dir, "", append([]string{"git"}, args...)...)
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

	_, stderr, code := runJogAsGit(t, tr.Dir, "rev-parse", "--verify", "definitely-missing")
	if code != 128 {
		t.Errorf("exit = %d, want git's 128; stderr = %q", code, stderr)
	}
	tr.Write("b.txt", "change so the next snapshot isn't a no-op\n")
	_, _, code = runJogAsGit(t, tr.Dir, "status", "--porcelain")
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
	stdout, stderr, code := runJogAsGit(t, dir, "version")
	if code != 0 || !strings.Contains(stdout, "git version") {
		t.Errorf("git version: code=%d stdout=%q", code, stdout)
	}
	if strings.Contains(stderr, "jog") {
		t.Errorf("jog noise outside a repo: %q", stderr)
	}

	_, stderr, code = runJogAsGit(t, dir, "status")
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

	_, _, code := runJogAsGit(t, tr.Dir, "checkout", "--", ".")
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

// Matrix row 15 — jog back: worktree-only restores, index byte-identical,
// --all deletes files added since the target, restores are undoable.
func TestBack(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "committed\n")
	tr.Commit("first")
	tr.Write("a.txt", "version one\n")
	tr.Write("u.txt", "untracked treasure\n")
	runJog(t, tr.Dir, "-m", "target state")
	tr.Write("a.txt", "version two\n")
	tr.Remove("u.txt")
	tr.Write("new.txt", "added after target\n")

	idx := tr.IndexBytes()

	// Single file, default target (newest snapshot at command start).
	stdout, stderr, code := runJog(t, tr.Dir, "back", "a.txt")
	if code != 0 {
		t.Fatalf("back a.txt: %d %s", code, stderr)
	}
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "a.txt")); string(got) != "version one\n" {
		t.Errorf("a.txt = %q", got)
	}
	if !bytes.Equal(idx, tr.IndexBytes()) {
		t.Fatal("index bytes changed across single-file back")
	}
	// The restore snapshotted first; its snapshot holds "version two".
	if got := tr.Git("show", "refs/jog/main:a.txt"); got != "version two" {
		t.Errorf("pre-restore snapshot content = %q", got)
	}

	// Undo the undo: default target is now the pre-restore snapshot.
	runJog(t, tr.Dir, "back", "a.txt")
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "a.txt")); string(got) != "version two\n" {
		t.Errorf("undo-of-undo: a.txt = %q", got)
	}

	// Deleted untracked file, restored by name.
	if _, _, code := runJog(t, tr.Dir, "back", "u.txt", "--at", "@{2}"); code != 0 {
		t.Fatal("back u.txt failed")
	}
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "u.txt")); string(got) != "untracked treasure\n" {
		t.Errorf("u.txt = %q", got)
	}

	// --all to an explicit snap id: worktree becomes exactly the target
	// tree — including deleting new.txt, which no git command alone does.
	targetSha := strings.Fields(tr.Git("log", "--format=%h %s", "refs/jog/main"))
	var target string
	for i := 0; i < len(targetSha)-1; i++ {
		if targetSha[i+1] == "manual:" { // "manual: target state"
			target = targetSha[i]
			break
		}
	}
	if target == "" {
		t.Fatal("could not find target snapshot id")
	}
	idx = tr.IndexBytes()
	stdout, stderr, code = runJog(t, tr.Dir, "back", "--all", "--at", target)
	if code != 0 {
		t.Fatalf("back --all: %d %s", code, stderr)
	}
	if !bytes.Equal(idx, tr.IndexBytes()) {
		t.Fatal("index bytes changed across back --all")
	}
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "a.txt")); string(got) != "version one\n" {
		t.Errorf("--all: a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "u.txt")); string(got) != "untracked treasure\n" {
		t.Errorf("--all: u.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, "new.txt")); !os.IsNotExist(err) {
		t.Error("--all did not delete new.txt (added after target)")
	}
	if !strings.Contains(stdout, "deleted") {
		t.Errorf("summary missing: %q", stdout)
	}

	// Undo of --all brings new.txt back.
	runJog(t, tr.Dir, "back", "--all")
	if _, err := os.Stat(filepath.Join(tr.Dir, "new.txt")); err != nil {
		t.Error("undo of --all did not restore new.txt")
	}
}

// back refuses non-snapshot targets and bad grammar; reflog time syntax
// falls back to oldest past the horizon (verified git behavior).
func TestBackGuards(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "oldest\n")
	tr.Commit("first")
	tr.Write("a.txt", "snapshotted oldest\n")
	runJog(t, tr.Dir, "-m", "one")
	tr.Write("a.txt", "snapshotted newest\n")
	runJog(t, tr.Dir, "-m", "two")
	tr.Write("a.txt", "dirty\n")

	// HEAD is a real commit, not a snapshot.
	_, stderr, code := runJog(t, tr.Dir, "back", "a.txt", "--at", "HEAD")
	if code != 1 || !strings.Contains(stderr, "not a jog snapshot") {
		t.Errorf("--at HEAD: code=%d stderr=%q", code, stderr)
	}
	// --all plus paths is a grammar error.
	if _, _, code := runJog(t, tr.Dir, "back", "--all", "a.txt"); code != 2 {
		t.Errorf("--all with paths: code=%d", code)
	}
	// A time past the oldest entry falls back to oldest, exit 0.
	_, _, code = runJog(t, tr.Dir, "back", "a.txt", "--at", "30.minutes.ago")
	if code != 0 {
		t.Fatalf("past-oldest time query exited %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "a.txt")); string(got) != "snapshotted oldest\n" {
		t.Errorf("past-oldest fallback restored %q, want oldest snapshot", got)
	}
}

// D11: `jog <unknown>` is an error with a `jog git` hint — never an
// implicit passthrough, and never a snapshot.
func TestUnknownCommandErrors(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	stdout, stderr, code := runJog(t, tr.Dir, "status")
	if code != 1 || !strings.Contains(stderr, "jog git status") {
		t.Errorf("jog status: code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "On branch") {
		t.Errorf("jog status ran git: %q", stdout)
	}
	if _, err := tr.TryGit("rev-parse", "--verify", "refs/jog/main"); err == nil {
		t.Error("unknown command minted a snapshot")
	}
}

// jog's own version, in all three spellings; aliased `git version` still
// reaches real git.
func TestVersion(t *testing.T) {
	dir := t.TempDir()
	for _, arg := range []string{"-v", "--version", "version"} {
		stdout, _, code := runJog(t, dir, arg)
		if code != 0 || !strings.HasPrefix(stdout, "jog version ") {
			t.Errorf("jog %s: code=%d stdout=%q", arg, code, stdout)
		}
	}
	stdout, _, code := runJogAsGit(t, dir, "version")
	if code != 0 || !strings.Contains(stdout, "git version") {
		t.Errorf("aliased git version: code=%d stdout=%q", code, stdout)
	}
}

// Through the alias (`jog git help`), help must reach real git; typed
// directly, `jog -h` shows jog's own usage.
func TestHelp(t *testing.T) {
	dir := t.TempDir()
	for _, arg := range []string{"-h", "--help", "help"} {
		stdout, _, code := runJog(t, dir, arg)
		if code != 0 || !strings.Contains(stdout, "jog — a memory for your working tree") {
			t.Errorf("jog %s: code=%d stdout=%q...", arg, code, stdout[:min(80, len(stdout))])
		}
	}

	stdout, _, code := runJogAsGit(t, dir, "help")
	if code != 0 || !strings.Contains(stdout, "usage: git") {
		t.Errorf("aliased git help: code=%d stdout=%q...", code, stdout[:min(80, len(stdout))])
	}
}

// D10: through the alias (`jog git …`), jog verbs don't exist — everything
// passes through to real git, still snapshotting causally first.
func TestAsGitIsPurePassthrough(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	_, stderr, code := runJogAsGit(t, tr.Dir, "snaps")
	if code == 0 || !strings.Contains(stderr, "'snaps' is not a git command") {
		t.Errorf("aliased `git snaps` should reach real git: code=%d stderr=%q", code, stderr)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: git snaps" {
		t.Errorf("provenance = %q — the failed passthrough should still have snapshotted first", got)
	}

	// Bare `git` is git's usage screen, not a jog snapshot report.
	stdout, _, code := runJogAsGit(t, tr.Dir)
	if code != 1 || !strings.Contains(stdout, "usage: git") {
		t.Errorf("bare aliased git: code=%d stdout=%q...", code, stdout[:min(80, len(stdout))])
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
	_, stderr, code := runJog(t, dir, "mcp")
	if code != 1 || !strings.Contains(stderr, "reserved") {
		t.Errorf("mcp stub: code=%d stderr=%q", code, stderr)
	}
}

// TestSince covers matrix row 21: since diffs snapshot ↔ snapshot, so an
// untracked file is reported as added — never as deleted, which the
// one-commit diff form would claim (verified trap, DESIGN §5). Also covers
// the D12 default (last command boundary) and the no-change fast exit.
func TestSince(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("base")
	runJog(t, tr.Dir, "-m", "boundary one")

	tr.Write("a.txt", "two\n")
	tr.Write("untracked.txt", "new\n")
	stdout, stderr, code := runJog(t, tr.Dir, "since")
	if code != 0 {
		t.Fatalf("since exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "a.txt") {
		t.Errorf("modified file missing from since output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "untracked.txt (new") {
		t.Errorf("untracked file not reported as added:\n%s", stdout)
	}
	if strings.Contains(stdout, "untracked.txt (gone") {
		t.Errorf("untracked file misreported as deleted (one-commit diff trap):\n%s", stdout)
	}

	// Unchanged tree: the fresh snapshot no-ops, the pre-invocation tip is
	// the fresh tip, and since says so instead of printing an empty diff.
	stdout, stderr, code = runJog(t, tr.Dir, "since")
	if code != 0 {
		t.Fatalf("second since exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no changes since") {
		t.Errorf("expected no-changes message, got:\n%s", stdout)
	}
}

// TestSinceGrammar covers matrix row 22: the target slot shares back --at's
// grammar (snap id, reflog syntax, D1-identity-guarded) and the first
// positional falls back to a path when it exists on disk.
func TestSinceGrammar(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Write("notes.txt", "keep\n")
	tr.Commit("base")
	runJog(t, tr.Dir, "-m", "first")
	tr.Write("a.txt", "two\n")
	runJog(t, tr.Dir, "-m", "second")

	// HEAD is a real commit, not a snapshot — the identity guard rejects it.
	_, stderr, code := runJog(t, tr.Dir, "since", "--at", "HEAD")
	if code != 1 || !strings.Contains(stderr, "not a jog snapshot") {
		t.Errorf("--at HEAD: code=%d stderr=%q", code, stderr)
	}

	// Chain reflog syntax resolves against the chain, not the branch's own
	// reflog (regression: bare @{N} used to hit the branch reflog and fail
	// the identity guard).
	tr.Write("a.txt", "three\n")
	stdout, stderr, code := runJog(t, tr.Dir, "since", "--at", "@{1}")
	if code != 0 {
		t.Fatalf("since --at @{1} exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "a.txt") {
		t.Errorf("expected a.txt in since --at @{1} output:\n%s", stdout)
	}

	// Positional target: a snap id in the T slot.
	sha := tr.Git("rev-parse", "--short", "refs/jog/main")
	tr.Write("a.txt", "four\n")
	stdout, stderr, code = runJog(t, tr.Dir, "since", sha)
	if code != 0 {
		t.Fatalf("since <id> exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "since "+sha) || !strings.Contains(stdout, "a.txt") {
		t.Errorf("positional snap id not honored:\n%s", stdout)
	}

	// Positional path: exists on disk, so it filters instead of targeting —
	// the unrelated changed file must not appear.
	tr.Write("notes.txt", "changed\n")
	tr.Write("other.txt", "noise\n")
	stdout, stderr, code = runJog(t, tr.Dir, "since", "notes.txt")
	if code != 0 {
		t.Fatalf("since <path> exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "notes.txt") || strings.Contains(stdout, "other.txt") {
		t.Errorf("path filter not honored:\n%s", stdout)
	}

	// Neither a path nor resolvable: a real error, with the dual-slot hint.
	_, stderr, code = runJog(t, tr.Dir, "since", "no-such-thing")
	if code != 1 || !strings.Contains(stderr, "cannot resolve") {
		t.Errorf("unresolvable positional: code=%d stderr=%q", code, stderr)
	}
}

// TestSnapsAll covers matrix row 23: the forest view interleaves every
// chain with per-chain attribution, and per-chain boundaries keep real
// history out.
func TestSnapsAll(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("real history commit")
	runJog(t, tr.Dir, "-m", "on main")
	tr.Git("checkout", "-q", "-b", "feat")
	tr.Write("f.txt", "feat\n")
	runJog(t, tr.Dir, "-m", "on feat")

	stdout, stderr, code := runJog(t, tr.Dir, "snaps", "--all")
	if code != 0 {
		t.Fatalf("snaps --all exited %d: %s", code, stderr)
	}
	for _, want := range []string{"jog/main", "jog/feat", "manual: on main", "manual: on feat"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("forest view missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "real history commit") {
		t.Errorf("forest view leaked real history past a chain boundary:\n%s", stdout)
	}
}

// runJogEnv is runJogStdin with extra environment entries (e.g. a fake HOME
// for doctor's wiring checks).
func runJogEnv(t *testing.T, dir string, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(jogBin, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	), extraEnv...)
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

// TestDoctor covers matrix row 30: healthy exits 0; a dead chain, missing
// gc keys, and a foreign chain tip are each findings (exit 1); --fix writes
// only the two gc keys.
func TestDoctor(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"jog hook claude"}]}]}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home}

	// Engine never run: the loudest finding doctor exists for.
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("base")
	stdout, _, code := runJogEnv(t, tr.Dir, env, "doctor")
	if code != 1 || !strings.Contains(stdout, "never run") {
		t.Errorf("dead engine: code=%d\n%s", code, stdout)
	}

	// Healthy: snapshot (mints chain + reflog + gc keys), hooks wired.
	runJog(t, tr.Dir, "-m", "first")
	stdout, _, code = runJogEnv(t, tr.Dir, env, "doctor")
	if code != 0 {
		t.Errorf("healthy repo: code=%d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "no findings") || !strings.Contains(stdout, "claude hooks") ||
		!strings.Contains(stdout, "claude skill") {
		t.Errorf("healthy output:\n%s", stdout)
	}

	// gc keys stripped: a finding, and --fix restores exactly those two.
	tr.Git("config", "--unset", "gc.refs/jog/*.reflogExpire")
	tr.Git("config", "--unset", "gc.refs/jog/*.reflogExpireUnreachable")
	before := tr.Git("config", "--local", "-l")
	stdout, _, code = runJogEnv(t, tr.Dir, env, "doctor")
	if code != 1 || !strings.Contains(stdout, "gc config") || !strings.Contains(stdout, "--fix") {
		t.Errorf("missing gc keys: code=%d\n%s", code, stdout)
	}
	stdout, _, code = runJogEnv(t, tr.Dir, env, "doctor", "--fix")
	if code != 0 || !strings.Contains(stdout, "fixed") {
		t.Errorf("doctor --fix: code=%d\n%s", code, stdout)
	}
	after := tr.Git("config", "--local", "-l")
	wantAdded := before + "\ngc.refs/jog/*.reflogexpire=never\ngc.refs/jog/*.reflogexpireunreachable=never"
	if sortLines(after) != sortLines(wantAdded) {
		t.Errorf("--fix wrote more than the two gc keys:\nbefore: %q\nafter: %q", before, after)
	}

	// Foreign tip: something other than jog moved the chain ref.
	tr.Git("update-ref", "refs/jog/main", "HEAD")
	stdout, _, code = runJogEnv(t, tr.Dir, env, "doctor")
	if code != 1 || !strings.Contains(stdout, "identity") {
		t.Errorf("foreign tip: code=%d\n%s", code, stdout)
	}

	// No triggers wired at all: the silent-engine finding.
	bare := t.TempDir()
	tr2 := testrepo.New(t)
	tr2.Write("a.txt", "one\n")
	tr2.Commit("base")
	runJog(t, tr2.Dir, "-m", "first")
	stdout, _, code = runJogEnv(t, tr2.Dir, []string{"HOME=" + bare}, "doctor")
	if code != 1 || !strings.Contains(stdout, "neither the alias nor Claude hooks") {
		t.Errorf("no triggers: code=%d\n%s", code, stdout)
	}
}

func sortLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// mintSnap builds one real snapshot commit on ref with a controlled date,
// mirroring the engine's shape exactly (D1 identity, parent 1 = previous
// snapshot, parent 2 = base edge, dated reflog entry).
func mintSnap(t *testing.T, tr *testrepo.Repo, ref, content string, date time.Time, base string) string {
	t.Helper()
	tr.Write("w.txt", content)
	idx := filepath.Join(tr.GitDir, "test-trim-index")
	env := []string{"GIT_INDEX_FILE=" + idx}
	tr.GitEnv(env, "add", "-A")
	tree := tr.GitEnv(env, "write-tree")

	d := date.Format(time.RFC3339)
	ident := []string{
		"GIT_AUTHOR_NAME=jog", "GIT_AUTHOR_EMAIL=jog@local",
		"GIT_COMMITTER_NAME=jog", "GIT_COMMITTER_EMAIL=jog@local",
		"GIT_AUTHOR_DATE=" + d, "GIT_COMMITTER_DATE=" + d,
	}
	prev, perr := tr.TryGit("rev-parse", "-q", "--verify", ref)
	args := []string{"commit-tree", tree}
	if perr == nil {
		args = append(args, "-p", prev)
	}
	if base != "" {
		args = append(args, "-p", base)
	}
	args = append(args, "-m", "manual: "+content)
	sha := tr.GitEnv(ident, args...)
	if perr != nil {
		prev = ""
	}
	tr.GitEnv(ident, "update-ref", "--create-reflog", "-m", "manual: "+content, ref, sha, prev)
	return sha
}

// TestTrim covers matrix rows 25–27 and 29: taper applied through a real
// chain rewrite — survivors byte-preserved (tree, dates, message, base
// edge), dropped snapshots off the timeline but held by the insurance ref,
// reflog replayed with true timestamps, dry-run inert, keep-all fenced,
// user index untouched.
func TestTrim(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("real history commit")
	head := tr.Git("rev-parse", "HEAD")

	now := time.Now()
	utcDay := func(age time.Duration, offset time.Duration) time.Time {
		return now.Add(-age).UTC().Truncate(24 * time.Hour).Add(offset)
	}
	utcHour := func(age time.Duration, offset time.Duration) time.Time {
		return now.Add(-age).UTC().Truncate(time.Hour).Add(offset)
	}

	ref := "refs/jog/main"
	ancient := mintSnap(t, tr, ref, "ancient", now.Add(-95*24*time.Hour), head)
	dailyA1 := mintSnap(t, tr, ref, "dailyA1", utcDay(10*24*time.Hour, 2*time.Hour), head)
	dailyA2 := mintSnap(t, tr, ref, "dailyA2", utcDay(10*24*time.Hour, 3*time.Hour), head)
	hourlyB1 := mintSnap(t, tr, ref, "hourlyB1", utcHour(72*time.Hour, 10*time.Minute), head)
	hourlyB2 := mintSnap(t, tr, ref, "hourlyB2", utcHour(72*time.Hour, 25*time.Minute), head)
	recent := mintSnap(t, tr, ref, "recent", now.Add(-2*time.Hour), head)
	tip := mintSnap(t, tr, ref, "tip", now.Add(-10*time.Minute), head)

	survivorMeta := map[string]string{}
	for _, sha := range []string{dailyA2, hourlyB2, recent, tip} {
		survivorMeta[sha] = tr.Git("log", "-1", "--date=raw", "--format=%T|%ad|%cd|%B|%P", sha)
	}
	idxBefore := tr.IndexBytes()
	reflogBefore := tr.Git("reflog", "show", ref)

	// Dry run: the plan, and nothing else.
	stdout, stderr, code := runJog(t, tr.Dir, "trim", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "would drop 3 of 7") {
		t.Fatalf("dry-run: code=%d\n%s%s", code, stdout, stderr)
	}
	if got := tr.Git("rev-parse", ref); got != tip {
		t.Fatalf("dry-run moved the ref: %s", got)
	}
	if got := tr.Git("reflog", "show", ref); got != reflogBefore {
		t.Fatalf("dry-run touched the reflog")
	}
	if _, err := tr.TryGit("rev-parse", "-q", "--verify", "refs/jog/@trash/main"); err == nil {
		t.Fatalf("dry-run wrote the insurance ref")
	}

	// Apply. The pre-trim snapshot no-ops (worktree == tip content).
	stdout, stderr, code = runJog(t, tr.Dir, "trim")
	if code != 0 || !strings.Contains(stdout, "dropped 3 of 7") {
		t.Fatalf("trim: code=%d\n%s%s", code, stdout, stderr)
	}

	// Row 29: the user's index is untouched by the one command that deletes.
	if !bytes.Equal(idxBefore, tr.IndexBytes()) {
		t.Error("trim modified the user's index")
	}

	// The surviving timeline: 4 jog snapshots, then the boundary.
	walk := tr.Git("log", "--first-parent", "--format=%ce|%T|%s", ref)
	var jogLines []string
	for _, l := range strings.Split(walk, "\n") {
		if !strings.HasPrefix(l, "jog@local|") {
			break
		}
		jogLines = append(jogLines, l)
	}
	if len(jogLines) != 4 {
		t.Fatalf("survivor walk: want 4 jog snapshots, got %d:\n%s", len(jogLines), walk)
	}
	for i, want := range []string{"tip", "recent", "hourlyB2", "dailyA2"} {
		if !strings.HasSuffix(jogLines[i], "manual: "+want) {
			t.Errorf("survivor %d: want %q, got %q", i, want, jogLines[i])
		}
	}

	// Row 25: survivors preserved verbatim — tree, author/committer dates,
	// message, base edge; only parent 1 relinked.
	newShas := strings.Split(tr.Git("rev-list", "--first-parent", "-4", ref), "\n")
	for i, orig := range []string{tip, recent, hourlyB2, dailyA2} {
		got := tr.Git("log", "-1", "--date=raw", "--format=%T|%ad|%cd|%B|%P", newShas[i])
		want := survivorMeta[orig]
		gw, ww := strings.Split(got, "|"), strings.Split(want, "|")
		for f, name := range []string{"tree", "authordate", "commitdate", "message"} {
			if gw[f] != ww[f] {
				t.Errorf("survivor %d %s changed: %q → %q", i, name, ww[f], gw[f])
			}
		}
		if !strings.HasSuffix(gw[4], head) {
			t.Errorf("survivor %d lost its base edge: parents %q", i, gw[4])
		}
	}

	// Dropped snapshots: off the timeline, held by the insurance ref.
	revList := tr.Git("rev-list", "--first-parent", ref)
	for _, dropped := range []string{ancient, dailyA1, hourlyB1} {
		if strings.Contains(revList, dropped) {
			t.Errorf("dropped snapshot %s still on the timeline", dropped[:7])
		}
		if _, err := tr.TryGit("cat-file", "-e", dropped); err != nil {
			t.Errorf("dropped snapshot %s not held by the insurance ref", dropped[:7])
		}
	}
	if got := tr.Git("rev-parse", "refs/jog/@trash/main"); got != tip {
		t.Errorf("insurance ref: want pre-trim tip %s, got %s", tip[:7], got[:7])
	}

	// Row 26: reflog replayed with original timestamps — entry per
	// survivor, and @{time} resolves truthfully.
	reflog := tr.Git("reflog", "show", ref)
	if got := len(strings.Split(reflog, "\n")); got != 4 {
		t.Errorf("reflog: want 4 entries, got %d:\n%s", got, reflog)
	}
	atDaily := utcDay(10*24*time.Hour, 3*time.Hour).Add(30 * time.Minute).Format(time.RFC3339)
	sha := tr.Git("rev-parse", ref+"@{"+atDaily+"}")
	if wantTree := strings.Split(survivorMeta[dailyA2], "|")[0]; tr.Git("log", "-1", "--format=%T", sha) != wantTree {
		t.Errorf("@{time} query resolved wrong survivor")
	}

	// Idempotence: a second trim finds nothing (its own boundary snapshot
	// no-ops, and the survivors all satisfy the policy).
	stdout, _, _ = runJog(t, tr.Dir, "trim")
	if !strings.Contains(stdout, "nothing to trim") {
		t.Errorf("second trim not idempotent:\n%s", stdout)
	}
}

// TestPick covers matrix row 31's e2e face: without a TTY the version list
// prints plainly — the same rows the TUI shows — restricted to snapshots
// that actually changed the file, and it must agree with `jog snaps <path>`.
func TestPick(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Write("other.txt", "x\n")
	tr.Commit("base")
	runJog(t, tr.Dir, "-m", "touches a")
	tr.Write("other.txt", "y\n")
	runJog(t, tr.Dir, "-m", "touches other only")
	tr.Write("a.txt", "two\n")
	runJog(t, tr.Dir, "-m", "touches a again")

	stdout, stderr, code := runJog(t, tr.Dir, "pick", "a.txt")
	if code != 0 {
		t.Fatalf("pick exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "touches a again") || !strings.Contains(stdout, "manual: touches a") {
		t.Errorf("pick list missing a.txt versions:\n%s", stdout)
	}
	if strings.Contains(stdout, "touches other only") {
		t.Errorf("pick listed a snapshot that did not change the file:\n%s", stdout)
	}

	stdout, _, _ = runJog(t, tr.Dir, "pick", "missing.txt")
	if !strings.Contains(stdout, "no snapshots touch") {
		t.Errorf("pick on untouched path:\n%s", stdout)
	}

	_, stderr, code = runJog(t, tr.Dir, "pick")
	if code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("pick without path: code=%d stderr=%q", code, stderr)
	}
}

// TestSkill: `jog skill claude` installs the Claude Code skill under HOME,
// is idempotent, prints with --print, and rejects unknown adapters.
func TestSkill(t *testing.T) {
	home := t.TempDir()
	env := []string{"HOME=" + home}
	dir := t.TempDir()
	path := filepath.Join(home, ".claude", "skills", "jog", "SKILL.md")

	stdout, _, code := runJogEnv(t, dir, env, "skill", "claude")
	if code != 0 || !strings.Contains(stdout, "installed") {
		t.Fatalf("install: code=%d\n%s", code, stdout)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	if !strings.HasPrefix(content, "---\n") || !strings.Contains(content, "name: jog") ||
		!strings.Contains(content, "description:") {
		t.Errorf("skill missing frontmatter:\n%.200s", content)
	}

	stdout, _, code = runJogEnv(t, dir, env, "skill", "claude")
	if code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("reinstall: code=%d\n%s", code, stdout)
	}

	printed, _, code := runJogEnv(t, dir, env, "skill", "--print", "claude")
	if code != 0 || printed != content {
		t.Errorf("--print: code=%d, output differs from installed file", code)
	}

	_, stderr, code := runJogEnv(t, dir, env, "skill", "codex")
	if code != 2 || !strings.Contains(stderr, "adapter") {
		t.Errorf("unknown adapter: code=%d stderr=%q", code, stderr)
	}
}
