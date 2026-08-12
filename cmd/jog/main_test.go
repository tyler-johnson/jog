package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tyler-johnson/jog/internal/testrepo"
)

// End-to-end tests run the compiled binary: passthrough replaces the process
// (unix) or proxies the child's exit code (windows), so it can only be
// observed from outside.

var (
	jogBin string
	// gitOnlyPath is a PATH holding only git's own directory — fake-home
	// tests use it so no real jog/claude/editor binary can be found while
	// git still resolves, on any OS.
	gitOnlyPath string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jogbin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	jogBin = filepath.Join(dir, "jog")
	if runtime.GOOS == "windows" {
		jogBin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", jogBin, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building jog: %v\n%s", err, out)
		os.Exit(1)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests need git on PATH")
		os.Exit(1)
	}
	gitOnlyPath = filepath.Dir(gitPath)
	os.Exit(m.Run())
}

// hookNeedle is the invariant tail of a wired jog hook command: the
// binary is bare `jog` on PATH or an absolute path ending jog / jog.exe,
// so the tail is what every spelling shares.
func hookNeedle(client string) string {
	if runtime.GOOS == "windows" {
		return "jog.exe hook " + client
	}
	return "jog hook " + client
}

// vimPluginPath mirrors the per-OS vim runtime root the editors package
// uses: ~/.vim everywhere except Windows's ~/vimfiles.
func vimPluginPath(home string) string {
	root := ".vim"
	if runtime.GOOS == "windows" {
		root = "vimfiles"
	}
	return filepath.Join(home, root, "plugin", "jog.vim")
}

// fakeHome returns env entries that relocate the home directory and hide
// any real jog/agent/editor installs, portably: HOME for unix tools and
// git, USERPROFILE for os.UserHomeDir on windows, the AppData roots kept
// inside the sandbox, and a PATH holding only git's directory.
func fakeHome(home string) []string {
	return []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=",
		"XDG_CACHE_HOME=",
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"PATH=" + gitOnlyPath,
	}
}

// fakeCacheDir is where os.UserCacheDir lands inside a fakeHome, per-OS.
func fakeCacheDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local")
	case "darwin":
		return filepath.Join(home, "Library", "Caches")
	default:
		return filepath.Join(home, ".cache")
	}
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
	// CI=1 keeps background maintenance (auto-trim, update checks) from
	// spawning detached children under every test; TestAutoTrim opts back
	// in with CI= via runJogEnv.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"CI=1",
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
	// D6: after snapshotting, bare jog shows the top of the timeline —
	// including the base column naming the commit the snapshot hangs off.
	if !strings.Contains(stdout, "ago  manual") {
		t.Errorf("bare jog missing recent-timeline readout: %q", stdout)
	}
	if base := tr.Git("rev-parse", "--short", "HEAD"); !strings.Contains(stdout, "  "+base+"  ") {
		t.Errorf("bare jog readout missing base column %s: %q", base, stdout)
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

// Even with a cached newer release, piped passthrough output stays
// byte-clean: the update notice is TTY-gated (and the test binary is a
// source build, which alone keeps the notice machinery inert).
func TestPassthroughNoNoticeWhenPiped(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")

	home := t.TempDir()
	cache := filepath.Join(fakeCacheDir(home), "jog")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"checked_at":"2099-01-01T00:00:00Z","latest":"v99.0.0"}`
	if err := os.WriteFile(filepath.Join(cache, "update.json"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	tr.Write("b.txt", "so the snapshot isn't a no-op\n")
	_, stderr, code := runJogEnv(t, tr.Dir, fakeHome(home), "git", "status", "--porcelain")
	if code != 0 || stderr != "" {
		t.Errorf("piped passthrough with a seeded cache: code=%d stderr=%q", code, stderr)
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

// Matrix row 15 — jog restore: worktree-only restores, index byte-identical,
// --all deletes files added since the target, restores are undoable.
func TestRestore(t *testing.T) {
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
	stdout, stderr, code := runJog(t, tr.Dir, "restore", "a.txt")
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
	runJog(t, tr.Dir, "restore", "a.txt")
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, "a.txt")); string(got) != "version two\n" {
		t.Errorf("undo-of-undo: a.txt = %q", got)
	}

	// Deleted untracked file, restored by name.
	if _, _, code := runJog(t, tr.Dir, "restore", "u.txt", "--at", "@{2}"); code != 0 {
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
	stdout, stderr, code = runJog(t, tr.Dir, "restore", "--all", "--at", target)
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
	runJog(t, tr.Dir, "restore", "--all")
	if _, err := os.Stat(filepath.Join(tr.Dir, "new.txt")); err != nil {
		t.Error("undo of --all did not restore new.txt")
	}
}

// restore refuses non-snapshot targets and bad grammar; reflog time syntax
// falls back to oldest past the horizon (verified git behavior).
func TestRestoreGuards(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "oldest\n")
	tr.Commit("first")
	tr.Write("a.txt", "snapshotted oldest\n")
	runJog(t, tr.Dir, "-m", "one")
	tr.Write("a.txt", "snapshotted newest\n")
	runJog(t, tr.Dir, "-m", "two")
	tr.Write("a.txt", "dirty\n")

	// HEAD is a real commit, not a snapshot.
	_, stderr, code := runJog(t, tr.Dir, "restore", "a.txt", "--at", "HEAD")
	if code != 1 || !strings.Contains(stderr, "not a jog snapshot") {
		t.Errorf("--at HEAD: code=%d stderr=%q", code, stderr)
	}
	// --all plus paths is a grammar error.
	if _, _, code := runJog(t, tr.Dir, "restore", "--all", "a.txt"); code != 2 {
		t.Errorf("--all with paths: code=%d", code)
	}
	// A time past the oldest entry falls back to oldest, exit 0.
	_, _, code = runJog(t, tr.Dir, "restore", "a.txt", "--at", "30.minutes.ago")
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

// TestUpdateSourceBuild: the test binary is a source build, so update
// must refuse with the go-install pointer — before any network touch,
// which is why this test can run offline.
func TestUpdateSourceBuild(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runJog(t, dir, "update")
	if code != 1 || !strings.Contains(stderr, "go install github.com/tyler-johnson/jog/cmd/jog@latest") {
		t.Errorf("update on a source build: code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runJog(t, dir, "update", "--bogus")
	if code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("update with args: code=%d stderr=%q", code, stderr)
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
func TestLog(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("real history commit")
	tr.Write("a.txt", "two\n")
	runJog(t, tr.Dir, "-m", "checkpoint one")
	tr.Write("b.txt", "new\n")
	runJog(t, tr.Dir, "-m", "checkpoint two")

	stdout, stderr, code := runJog(t, tr.Dir, "log")
	if code != 0 {
		t.Fatalf("log exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "manual: checkpoint one") || !strings.Contains(stdout, "manual: checkpoint two") {
		t.Errorf("timeline missing snapshots:\n%s", stdout)
	}
	if !strings.Contains(stdout, "b.txt") {
		t.Errorf("files-changed detail missing:\n%s", stdout)
	}
	// Row 16: the first-parent walk must stop at the chain boundary, not
	// run into real history — the boundary commit itself appears exactly
	// once, as the ● anchor row the chain grew from.
	if n := strings.Count(stdout, "real history commit"); n != 1 {
		t.Errorf("boundary commit should appear once (anchor row), got %d:\n%s", n, stdout)
	}
	if !strings.Contains(stdout, "● "+tr.Git("rev-parse", "--short", "HEAD")+"  real history commit") {
		t.Errorf("anchor row missing or misshapen:\n%s", stdout)
	}
	if strings.Index(stdout, "checkpoint two") > strings.Index(stdout, "checkpoint one") {
		t.Errorf("timeline not newest-first:\n%s", stdout)
	}
	// Every snapshot row carries the commit it was based on.
	base := tr.Git("rev-parse", "--short", "HEAD")
	for _, want := range []string{"manual: checkpoint one", "manual: checkpoint two"} {
		for _, line := range strings.Split(stdout, "\n") {
			if strings.Contains(line, want) && !strings.Contains(line, base) {
				t.Errorf("snapshot row missing base %s: %q", base, line)
			}
		}
	}
	// Piped output is plain rows — ids and provenance with no ANSI
	// styling; the interactive browser only appears on a TTY.
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("piped log output carries ANSI escapes:\n%s", stdout)
	}
	if id := tr.Git("rev-parse", "--short", "refs/jog/main"); !strings.Contains(stdout, id) {
		t.Errorf("piped log output missing snapshot id %s:\n%s", id, stdout)
	}

	// A commit between snapshots surfaces as an event row between their
	// timeline entries, labeled by the reflog.
	tr.Commit("second real commit")
	tr.Write("c.txt", "more\n")
	runJog(t, tr.Dir, "-m", "checkpoint three")
	stdout, _, _ = runJog(t, tr.Dir, "log")
	iThree := strings.Index(stdout, "checkpoint three")
	iEvent := strings.Index(stdout, "commit: second real commit")
	iTwo := strings.Index(stdout, "checkpoint two")
	if iThree == -1 || iEvent == -1 || iTwo == -1 || !(iThree < iEvent && iEvent < iTwo) {
		t.Errorf("event row misplaced (three=%d event=%d two=%d):\n%s", iThree, iEvent, iTwo, stdout)
	}

	// Path filter: only entries touching b.txt.
	stdout, _, _ = runJog(t, tr.Dir, "log", "b.txt")
	if !strings.Contains(stdout, "checkpoint two") || strings.Contains(stdout, "checkpoint one") {
		t.Errorf("path filter wrong:\n%s", stdout)
	}

	// -p appends patches.
	stdout, _, _ = runJog(t, tr.Dir, "log", "-p")
	if !strings.Contains(stdout, "diff --git") || !strings.Contains(stdout, "+new") {
		t.Errorf("-p missing patches:\n%s", stdout)
	}

	// Reading snapshots first: a dirty tree lands on the timeline before
	// it is displayed.
	tr.Write("a.txt", "three\n")
	stdout, _, _ = runJog(t, tr.Dir, "log")
	if !strings.Contains(stdout, "pre: jog log") {
		t.Errorf("log did not snapshot before reading:\n%s", stdout)
	}
}

// The machine outputs: --json is a parseable array carrying everything an
// agent needs (ids, times, provenance, chain, files) without touching
// refs/jog/* itself; -n limits; --format hands the rendering to git with
// nothing appended; incompatible combinations fail loudly.
func TestLogMachineOutput(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("real history commit")
	tr.Write("a.txt", "two\n")
	runJog(t, tr.Dir, "-m", "checkpoint one")
	tr.Write("b.txt", "new\n")
	runJog(t, tr.Dir, "-m", "checkpoint two")

	stdout, stderr, code := runJog(t, tr.Dir, "log", "--json")
	if code != 0 {
		t.Fatalf("log --json exited %d: %s", code, stderr)
	}
	var entries []struct {
		ID, SHA, Base, Time, Age, Chain, Provenance string
		Files                                       []struct{ Status, Path string }
	}
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("log --json is not valid JSON: %v\n%s", err, stdout)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d:\n%s", len(entries), stdout)
	}
	e := entries[0] // newest first
	if e.Provenance != "manual: checkpoint two" || e.Chain != "main" ||
		len(e.SHA) != 40 || !strings.HasPrefix(e.SHA, e.ID) || e.Time == "" {
		t.Errorf("newest entry wrong: %+v", e)
	}
	// a.txt was untouched between the checkpoints, so the newest snapshot's
	// parent-1 diff is exactly the b.txt addition.
	if len(e.Files) != 1 || e.Files[0].Status != "A" || e.Files[0].Path != "b.txt" {
		t.Errorf("newest entry files = %+v, want [{A b.txt}]", e.Files)
	}
	if f := entries[1].Files; len(f) != 1 || f[0].Status != "M" || f[0].Path != "a.txt" {
		t.Errorf("oldest entry files = %+v, want [{M a.txt}]", f)
	}
	// Every entry names the commit it was based on: the newest via its base
	// edge (parent 2), the chain root via its single parent — same sha here.
	if baseSha := tr.Git("rev-parse", "HEAD"); entries[0].Base != baseSha || entries[1].Base != baseSha {
		t.Errorf("bases = %.7s %.7s, want both %.7s", entries[0].Base, entries[1].Base, baseSha)
	}

	// Unborn-era snapshots (no commit yet) have no base — empty, not absent.
	tr2 := testrepo.New(t)
	tr2.Write("x.txt", "one\n")
	runJog(t, tr2.Dir, "-m", "before first commit")
	stdout, _, _ = runJog(t, tr2.Dir, "log", "--json")
	var unborn []struct{ Base *string }
	if err := json.Unmarshal([]byte(stdout), &unborn); err != nil {
		t.Fatalf("unborn log --json: %v\n%s", err, stdout)
	}
	if len(unborn) != 1 || unborn[0].Base == nil || *unborn[0].Base != "" {
		t.Errorf("unborn entry base wrong: %+v\n%s", unborn, stdout)
	}

	// -n limits, and an empty result is an empty array, not prose.
	stdout, _, _ = runJog(t, tr.Dir, "log", "--json", "-n", "1")
	if strings.Count(stdout, `"sha"`) != 1 {
		t.Errorf("-n 1 did not limit:\n%s", stdout)
	}
	stdout, _, _ = runJog(t, tr.Dir, "log", "--json", "nonexistent.txt")
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("empty JSON result = %q, want []", stdout)
	}

	// --format owns the output: exactly one line per snapshot, no
	// name-status appended, no ANSI.
	stdout, _, code = runJog(t, tr.Dir, "log", "--format=%h %s")
	if code != 0 {
		t.Fatalf("log --format exited %d", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "manual: checkpoint two") ||
		strings.Contains(stdout, "\x1b[") || strings.Contains(stdout, "b.txt") {
		t.Errorf("--format output wrong:\n%s", stdout)
	}

	// Loud grammar failures, exit 2.
	for _, bad := range [][]string{
		{"log", "--json", "-p"},
		{"log", "--json", "--format=%h"},
		{"log", "-n", "potato"},
		{"log", "-n"},
	} {
		if _, stderr, code := runJog(t, tr.Dir, bad...); code != 2 || stderr == "" {
			t.Errorf("%v: code=%d stderr=%q, want loud exit 2", bad, code, stderr)
		}
	}
}

// Hook entry points always exit 0 (row 18) — misconfigured invocations
// included, since a non-zero exit would block the user's tool call.
func TestHookAlwaysExitsZero(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	tr.Write("b.txt", "y\n")

	claudeJSON := `{"hook_event_name":"PreToolUse","session_id":"e2e-sess-id","cwd":` +
		strconvQuote(tr.Dir) + `,"tool_name":"Bash","tool_input":{"command":"make build"}}`
	stdout, _, code := runJogStdin(t, tr.Dir, claudeJSON, "hook", "claude")
	if code != 0 || stdout != "" {
		t.Errorf("hook claude: code=%d stdout=%q (stdout must stay empty)", code, stdout)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "claude[e2e-sess]: Bash(make build)" {
		t.Errorf("provenance = %q", got)
	}

	tr.Write("b.txt", "codex change\n")
	codexJSON := `{"hook_event_name":"PreToolUse","session_id":"codex-session","cwd":` +
		strconvQuote(tr.Dir) + `,"tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch"}}`
	stdout, _, code = runJogStdin(t, tr.Dir, codexJSON, "hook", "codex")
	if code != 0 || stdout != "" {
		t.Errorf("hook codex: code=%d stdout=%q (stdout must stay empty)", code, stdout)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "codex[codex-se]: apply_patch(*** Begin Patch)" {
		t.Errorf("codex provenance = %q", got)
	}

	codexPromptJSON := `{"hook_event_name":"UserPromptSubmit","session_id":"codex-session","cwd":` +
		strconvQuote(tr.Dir) + `,"prompt":"test the prompt hook"}`
	stdout, _, code = runJogStdin(t, tr.Dir, codexPromptJSON, "hook", "codex")
	if code != 0 {
		t.Fatalf("hook codex UserPromptSubmit: code=%d stdout=%q", code, stdout)
	}
	var notice struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &notice); err != nil {
		t.Fatalf("hook codex UserPromptSubmit emitted invalid JSON: %v\n%s", err, stdout)
	}
	if notice.HookSpecificOutput.HookEventName != "UserPromptSubmit" ||
		!strings.Contains(notice.HookSpecificOutput.AdditionalContext, "[jog]") {
		t.Errorf("hook codex UserPromptSubmit response = %q", stdout)
	}
	stdout, _, code = runJogStdin(t, tr.Dir, codexPromptJSON, "hook", "codex")
	if code != 0 || stdout != "" {
		t.Errorf("repeated codex prompt: code=%d stdout=%q, want silent success", code, stdout)
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
func TestLogAll(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("real history commit")
	runJog(t, tr.Dir, "-m", "on main")
	tr.Git("checkout", "-q", "-b", "feat")
	tr.Write("f.txt", "feat\n")
	runJog(t, tr.Dir, "-m", "on feat")

	stdout, stderr, code := runJog(t, tr.Dir, "log", "--all")
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
	// Base columns and commit rows are single-chain views for now — the
	// forest passthrough stays event-free.
	if strings.Contains(stdout, "●") {
		t.Errorf("forest view grew event rows (deferred feature):\n%s", stdout)
	}
}

// runJogEnv is runJogStdin with extra environment entries (e.g. a fake HOME
// for doctor's wiring checks).
func runJogEnv(t *testing.T, dir string, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runJogEnvStdin(t, dir, extraEnv, "", args...)
}

// runJogEnvStdin is runJogEnv with input on stdin — how the interactive
// `jog install` / `jog uninstall` answers are piped in.
func runJogEnvStdin(t *testing.T, dir string, extraEnv []string, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(jogBin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	// extraEnv comes last so a test can override the hermetic defaults
	// (exec.Cmd keeps the last value for duplicate keys) — CI=1 mirrors
	// runJogStdin, see there.
	cmd.Env = append(append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"CI=1",
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

// TestBranches covers the per-chain summary: one row per chain with the
// current branch starred, counts and newest provenance, deleted branches
// marked - in the gutter with the trim --gone footer, `branch` as a
// byte-identical alias, the command's own boundary snapshot, and the
// --json shape.
func TestBranches(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("base")
	runJog(t, tr.Dir, "-m", "wip on main")
	tr.Git("checkout", "-q", "-b", "feature")
	tr.Write("b.txt", "feat\n")
	runJog(t, tr.Dir, "-m", "wip on feature")
	tr.Git("checkout", "-q", "main")
	tr.Git("branch", "-D", "feature")

	// The run's own boundary snapshot lands first (b.txt is untracked
	// relative to main's last snapshot), so main reads 2 snapshots with
	// the pre: provenance on top.
	stdout, stderr, code := runJog(t, tr.Dir, "branches")
	if code != 0 {
		t.Fatalf("branches exited %d: %s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 rows + footer, got:\n%s", stdout)
	}
	if !strings.HasPrefix(lines[0], "* main") ||
		!strings.Contains(lines[0], "2 snapshots") ||
		!strings.Contains(lines[0], "pre: jog branches") {
		t.Errorf("main row: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "- feature") ||
		!strings.Contains(lines[1], "1 snapshot,") ||
		!strings.Contains(lines[1], "manual: wip on feature") {
		t.Errorf("feature row: %q", lines[1])
	}
	if lines[2] != "- Deleted branch, clean up with jog trim --gone" {
		t.Errorf("footer: %q", lines[2])
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Errorf("piped output carries ANSI escapes:\n%s", stdout)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: jog branches" {
		t.Errorf("self-snapshot provenance = %q", got)
	}

	// The alias no-ops its own snapshot (the tree is now clean), so the
	// listing is byte-identical.
	alias, _, code := runJog(t, tr.Dir, "branch")
	if code != 0 || alias != stdout {
		t.Errorf("branch output differs from branches (code=%d):\n%s", code, alias)
	}

	stdout, stderr, code = runJog(t, tr.Dir, "branches", "--json")
	if code != 0 {
		t.Fatalf("branches --json exited %d: %s", code, stderr)
	}
	var rows []struct {
		Branch    string `json:"branch"`
		Live      bool   `json:"live"`
		Snapshots int    `json:"snapshots"`
		Newest    struct {
			ID         string `json:"id"`
			SHA        string `json:"sha"`
			Time       string `json:"time"`
			Age        string `json:"age"`
			Provenance string `json:"provenance"`
		} `json:"newest"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("--json not parseable: %v\n%s", err, stdout)
	}
	if len(rows) != 2 || rows[0].Branch != "main" || rows[1].Branch != "feature" {
		t.Fatalf("--json rows: %+v", rows)
	}
	if !rows[0].Live || rows[0].Snapshots != 2 ||
		rows[0].Newest.ID != rows[0].Newest.SHA[:7] ||
		rows[0].Newest.Provenance != "pre: jog branches" {
		t.Errorf("main json row: %+v", rows[0])
	}
	if _, err := time.Parse(time.RFC3339, rows[0].Newest.Time); err != nil {
		t.Errorf("newest.time not RFC3339: %v", err)
	}
	if rows[1].Live || rows[1].Snapshots != 1 ||
		rows[1].Newest.Provenance != "manual: wip on feature" {
		t.Errorf("feature json row: %+v", rows[1])
	}

	_, stderr, code = runJog(t, tr.Dir, "branches", "--bogus")
	if code != 2 || !strings.Contains(stderr, "usage: jog branches [--json]") {
		t.Errorf("unknown flag: code=%d stderr=%q", code, stderr)
	}

	// The no-chains state needs a repo whose boundary snapshot cannot mint
	// a chain — in any worktree the command's own snapshot seeds one — so
	// a bare repo stands in (same silent-snapshot-failure path as jog log).
	bare := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	stdout, _, code = runJog(t, bare, "branches")
	if code != 0 || !strings.Contains(stdout, "no snapshot chains") {
		t.Errorf("empty state: code=%d stdout=%q", code, stdout)
	}
	stdout, _, code = runJog(t, bare, "branches", "--json")
	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Errorf("empty state --json: code=%d stdout=%q", code, stdout)
	}
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
	// An editor hook is a trigger too — doctor should report it.
	vimHook := vimPluginPath(home)
	if err := os.MkdirAll(filepath.Dir(vimHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vimHook, []byte("\" jog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := fakeHome(home)

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
		!strings.Contains(stdout, "claude skill") || !strings.Contains(stdout, "vim editor") {
		t.Errorf("healthy output:\n%s", stdout)
	}
	// The update line: the test binary is a source build, so doctor says
	// so — the cache-driven wordings are covered by selfupdate's unit
	// tests, where the version is injectable.
	if !strings.Contains(stdout, "source build") {
		t.Errorf("update line missing:\n%s", stdout)
	}

	// Disk + trim visibility: the cost line is always there; a fresh chain
	// has nothing to drop. An over-age snapshot flips the trim line to say
	// so — info, not a finding, so the exit code stays 0.
	if !strings.Contains(stdout, "snapshot disk") || !strings.Contains(stdout, "nothing to drop") {
		t.Errorf("disk/trim lines missing:\n%s", stdout)
	}
	agedHead := tr.Git("rev-parse", "HEAD")
	mintSnap(t, tr, "refs/jog/aged", "old", time.Now().Add(-95*24*time.Hour), agedHead)
	mintSnap(t, tr, "refs/jog/aged", "new", time.Now().Add(-time.Hour), agedHead)
	stdout, _, code = runJogEnv(t, tr.Dir, env, "doctor")
	if code != 0 || !strings.Contains(stdout, "1 snapshot older than ~90 days — `jog trim` drops them") {
		t.Errorf("trim-needed line: code=%d\n%s", code, stdout)
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
	stdout, _, code = runJogEnv(t, tr2.Dir, fakeHome(bare), "doctor")
	if code != 1 || !strings.Contains(stdout, "neither the alias, the preexec hook, nor agent/editor hooks") {
		t.Errorf("no triggers: code=%d\n%s", code, stdout)
	}

	// A `jog shell install`ed alias is reported as managed, not
	// heuristic, and clears the silent-engine finding.
	marked := "alias git='jog git' # jog — added by `jog shell install`\n"
	if err := os.WriteFile(filepath.Join(bare, ".bashrc"), []byte(marked), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, tr2.Dir, fakeHome(bare), "doctor")
	if code != 0 || !strings.Contains(stdout, "`jog shell` manages it") {
		t.Errorf("managed alias: code=%d\n%s", code, stdout)
	}
	if strings.Contains(stdout, "neither the alias") {
		t.Errorf("managed alias should clear the triggers warning:\n%s", stdout)
	}

	// A managed preexec line is a trigger in its own right: ok row, and
	// preexec-only wiring must not raise the no-triggers warning.
	bare2 := t.TempDir()
	preexecMarked := `__jog_preexec() { command -v jog >/dev/null && jog shell-hook -- "$1"; }; preexec_functions+=(__jog_preexec)` +
		" # jog preexec — added by `jog shell install`\n"
	if err := os.WriteFile(filepath.Join(bare2, ".zshrc"), []byte(preexecMarked), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, tr2.Dir, fakeHome(bare2), "doctor")
	if code != 0 || !strings.Contains(stdout, "`jog shell-hook` wired in ~/.zshrc (`jog shell` manages it)") {
		t.Errorf("managed preexec: code=%d\n%s", code, stdout)
	}
	if strings.Contains(stdout, "neither the alias") {
		t.Errorf("preexec-only wiring should clear the triggers warning:\n%s", stdout)
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

// TestTrim covers matrix rows 25–27 and 29: the keep window applied
// through a real chain rewrite — survivors byte-preserved (tree, dates,
// message, base edge), dropped snapshots off the timeline but held by the
// insurance ref, reflog replayed with true timestamps, dry-run inert,
// user index untouched. The drops are the chain's oldest entries, so
// every survivor is re-committed — the preservation checks cover them all.
func TestTrim(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("real history commit")
	head := tr.Git("rev-parse", "HEAD")

	now := time.Now()
	ref := "refs/jog/main"
	ancient1 := mintSnap(t, tr, ref, "ancient1", now.Add(-120*24*time.Hour), head)
	ancient2 := mintSnap(t, tr, ref, "ancient2", now.Add(-95*24*time.Hour), head)
	month := mintSnap(t, tr, ref, "month", now.Add(-30*24*time.Hour), head)
	week := mintSnap(t, tr, ref, "week", now.Add(-7*24*time.Hour), head)
	recent := mintSnap(t, tr, ref, "recent", now.Add(-2*time.Hour), head)
	tip := mintSnap(t, tr, ref, "tip", now.Add(-10*time.Minute), head)

	survivorMeta := map[string]string{}
	for _, sha := range []string{month, week, recent, tip} {
		survivorMeta[sha] = tr.Git("log", "-1", "--date=raw", "--format=%T|%ad|%cd|%B|%P", sha)
	}
	idxBefore := tr.IndexBytes()
	reflogBefore := tr.Git("reflog", "show", ref)

	// Dry run: the plan, and nothing else.
	stdout, stderr, code := runJog(t, tr.Dir, "trim", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "would drop 2 of 6") {
		t.Fatalf("dry-run: code=%d\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "snapshots hold ~") || !strings.Contains(stdout, "settles to ~") {
		t.Errorf("dry-run size footer missing:\n%s", stdout)
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
	if code != 0 || !strings.Contains(stdout, "dropped 2 of 6") {
		t.Fatalf("trim: code=%d\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "frees after the next trim") {
		t.Errorf("apply size footer missing:\n%s", stdout)
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
	for i, want := range []string{"tip", "recent", "week", "month"} {
		if !strings.HasSuffix(jogLines[i], "manual: "+want) {
			t.Errorf("survivor %d: want %q, got %q", i, want, jogLines[i])
		}
	}

	// Row 25: survivors preserved verbatim — tree, author/committer dates,
	// message, base edge; only parent 1 relinked.
	newShas := strings.Split(tr.Git("rev-list", "--first-parent", "-4", ref), "\n")
	for i, orig := range []string{tip, recent, week, month} {
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
	for _, dropped := range []string{ancient1, ancient2} {
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
	atMonth := now.Add(-30 * 24 * time.Hour).Add(30 * time.Minute).Format(time.RFC3339)
	sha := tr.Git("rev-parse", ref+"@{"+atMonth+"}")
	if wantTree := strings.Split(survivorMeta[month], "|")[0]; tr.Git("log", "-1", "--format=%T", sha) != wantTree {
		t.Errorf("@{time} query resolved wrong survivor")
	}

	// Idempotence: a second trim finds nothing (its own boundary snapshot
	// no-ops, and the survivors all sit inside the keep window).
	stdout, _, _ = runJog(t, tr.Dir, "trim")
	if !strings.Contains(stdout, "nothing to trim") {
		t.Errorf("second trim not idempotent:\n%s", stdout)
	}
}

// TestTrimMaxSize: the size budget tightens the age cutoff, one snapshot
// leniently — the snapshot that crosses the budget survives, so a budget
// exceeded only by its crossing snapshot drops nothing, a 1-byte budget
// still leaves the newest snapshot, and no budget means no budget lines.
func TestTrimMaxSize(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("base")
	head := tr.Git("rev-parse", "HEAD")
	now := time.Now()

	// Four snapshots, each pinning a distinct ~256 KiB of content history
	// never saw — LCG bytes, so zlib can't fold them away.
	blob := func(seed uint32) string {
		b := make([]byte, 256<<10)
		x := seed*2654435761 | 1
		for i := range b {
			x = x*1664525 + 1013904223
			b[i] = byte(x >> 24)
		}
		return string(b)
	}
	ref := "refs/jog/main"
	tr.Write("big.bin", blob(1))
	older := mintSnap(t, tr, ref, "older", now.Add(-60*24*time.Hour), head)
	tr.Write("big.bin", blob(2))
	mintSnap(t, tr, ref, "old", now.Add(-40*24*time.Hour), head)
	tr.Write("big.bin", blob(3))
	mintSnap(t, tr, ref, "mid", now.Add(-20*24*time.Hour), head)
	tr.Write("big.bin", blob(4))
	mintSnap(t, tr, ref, "new", now.Add(-time.Hour), head)

	// No budget set: everything is inside the 90-day window, nothing to do,
	// and no budget wording anywhere.
	stdout, stderr, code := runJog(t, tr.Dir, "trim", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "nothing to trim") || strings.Contains(stdout, "size budget") {
		t.Fatalf("no-budget dry-run: code=%d\n%s%s", code, stdout, stderr)
	}

	// ~1 MiB held, 800k budget: over, but only by the crossing snapshot —
	// the one-snapshot leniency covers it and nothing drops.
	tr.Git("config", "jog.maxSize", "800k")
	stdout, stderr, code = runJog(t, tr.Dir, "trim", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "within one snapshot of the budget") ||
		!strings.Contains(stdout, "nothing to trim") || strings.Contains(stdout, "would drop") {
		t.Fatalf("within-one dry-run: code=%d\n%s%s", code, stdout, stderr)
	}

	// 600k fits two blobs and the third crosses: the cutoff lands on the
	// crossing snapshot's age (~40 days) and only what is older drops.
	tr.Git("config", "jog.maxSize", "600k")
	stdout, stderr, code = runJog(t, tr.Dir, "trim", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "size budget") || !strings.Contains(stdout, "would drop 1 of 4") {
		t.Fatalf("budget dry-run: code=%d\n%s%s", code, stdout, stderr)
	}
	stdout, stderr, code = runJog(t, tr.Dir, "trim")
	if code != 0 || !strings.Contains(stdout, "dropped 1 of 4") || !strings.Contains(stdout, "~40 days") {
		t.Fatalf("budget trim: code=%d\n%s%s", code, stdout, stderr)
	}
	walk := tr.Git("log", "--first-parent", "--format=%s", ref)
	if strings.Contains(walk, "manual: older") || !strings.Contains(walk, "manual: old") ||
		!strings.Contains(walk, "manual: mid") || !strings.Contains(walk, "manual: new") {
		t.Errorf("survivors after budget trim:\n%s", walk)
	}
	if _, err := tr.TryGit("cat-file", "-e", older); err != nil {
		t.Errorf("dropped snapshot not held by the insurance ref")
	}

	// A 1-byte budget: even the newest snapshot alone is over, and the
	// leniency keeps exactly it.
	tr.Git("config", "jog.maxSize", "1")
	stdout, stderr, code = runJog(t, tr.Dir, "trim")
	if code != 0 || !strings.Contains(stdout, "size budget 1 B: dropping snapshots older than") ||
		!strings.Contains(stdout, "dropped 2 of 3") {
		t.Fatalf("1-byte budget: code=%d\n%s%s", code, stdout, stderr)
	}
	walk = tr.Git("log", "--first-parent", "--format=%s", ref)
	if !strings.HasPrefix(walk, "manual: new") || strings.Contains(walk, "manual: mid") {
		t.Errorf("newest snapshot should be the sole survivor:\n%s", walk)
	}
}

// TestTrimGone: age spares nothing — a chain whose snapshots have all
// aged out is removed whole, branch or no branch; --gone removes dead
// chains immediately; and the stale @trash a removed chain leaves behind
// goes on the following run.
func TestTrimGone(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("base")
	head := tr.Git("rev-parse", "HEAD")
	now := time.Now()

	// feature: a branch that no longer exists, fully aged. main: alive
	// but fully aged too — age takes both whole.
	mintSnap(t, tr, "refs/jog/feature", "feat-old", now.Add(-100*24*time.Hour), head)
	featTip := mintSnap(t, tr, "refs/jog/feature", "feat-tip", now.Add(-95*24*time.Hour), head)
	mainTip := mintSnap(t, tr, "refs/jog/main", "main-tip", now.Add(-95*24*time.Hour), head)

	stdout, stderr, code := runJog(t, tr.Dir, "trim", "--dry-run")
	if code != 0 ||
		!strings.Contains(stdout, "feature: branch is gone — would remove the chain (2 snapshots)") ||
		!strings.Contains(stdout, "main: would drop all 1 snapshot and remove the chain") {
		t.Fatalf("dry-run: code=%d\n%s%s", code, stdout, stderr)
	}

	// Apply: both chains gone, both tips held by trash. (The run's own
	// boundary snapshot no-ops — the worktree matches main's tip.)
	stdout, stderr, code = runJog(t, tr.Dir, "trim")
	if code != 0 ||
		!strings.Contains(stdout, "feature: branch is gone — chain removed (2 snapshots saved at refs/jog/@trash/feature until the next trim)") ||
		!strings.Contains(stdout, "main: dropped all 1 snapshot — chain removed (saved at refs/jog/@trash/main until the next trim)") {
		t.Fatalf("apply: code=%d\n%s%s", code, stdout, stderr)
	}
	for _, ref := range []string{"refs/jog/feature", "refs/jog/main"} {
		if _, err := tr.TryGit("rev-parse", "-q", "--verify", ref); err == nil {
			t.Errorf("%s still exists after full age-out", ref)
		}
	}
	if got := tr.Git("rev-parse", "refs/jog/@trash/feature"); got != featTip {
		t.Errorf("feature trash: want %s, got %s", featTip[:7], got[:7])
	}
	if got := tr.Git("rev-parse", "refs/jog/@trash/main"); got != mainTip {
		t.Errorf("main trash: want %s, got %s", mainTip[:7], got[:7])
	}

	// Next trim: feature's stale trash goes. main's chain is revived by
	// this run's own boundary snapshot (dirty worktree), so its trash is
	// not stale and stays.
	stdout, stderr, code = runJog(t, tr.Dir, "trim")
	if code != 0 || !strings.Contains(stdout, "feature: stale trash removed (its chain was already gone)") {
		t.Fatalf("stale trash: code=%d\n%s%s", code, stdout, stderr)
	}
	if _, err := tr.TryGit("rev-parse", "-q", "--verify", "refs/jog/@trash/feature"); err == nil {
		t.Error("feature trash still exists")
	}
	tr.Git("rev-parse", "refs/jog/@trash/main")

	// --gone: a dead chain too young for age goes immediately.
	mintSnap(t, tr, "refs/jog/dead", "dead-tip", now.Add(-time.Hour), head)
	stdout, _, code = runJog(t, tr.Dir, "trim")
	if code != 0 || !strings.Contains(stdout, "dead: 1 snapshot, nothing to trim") {
		t.Fatalf("young dead chain should survive a plain trim: code=%d\n%s", code, stdout)
	}
	stdout, _, code = runJog(t, tr.Dir, "trim", "--gone")
	if code != 0 || !strings.Contains(stdout, "dead: branch is gone — chain removed (1 snapshot saved at refs/jog/@trash/dead until the next trim)") {
		t.Fatalf("--gone: code=%d\n%s", code, stdout)
	}
}

// readTrimStamp decodes <gitdir>/jog/autotrim.json — the per-repo
// auto-trim state file.
func readTrimStamp(t *testing.T, tr *testrepo.Repo) (trimmedAt time.Time, intervalSecs int64, exists bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tr.GitDir, "jog", "autotrim.json"))
	if err != nil {
		return time.Time{}, 0, false
	}
	var s struct {
		TrimmedAt    time.Time `json:"trimmed_at"`
		IntervalSecs int64     `json:"interval_secs"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("corrupt auto-trim stamp: %v\n%s", err, b)
	}
	return s.TrimmedAt, s.IntervalSecs, true
}

// TestAutoTrim covers the background trim: a git command through jog
// stamps the per-repo state file and spawns a detached `jog trim` on
// the jog.autoTrim cadence (daily by default), which enforces the keep
// window on its own; false disables it; `jog config` pushes a new
// cadence into the stamp immediately; a manual trim resets the clock.
func TestAutoTrim(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("real history commit")
	head := tr.Git("rev-parse", "HEAD")
	now := time.Now()
	ref := "refs/jog/main"
	mintSnap(t, tr, ref, "ancient", now.Add(-120*24*time.Hour), head)
	mintSnap(t, tr, ref, "fresh", now.Add(-time.Hour), head)

	// Under CI (the harness default) nothing spawns and nothing is
	// stamped — test suites and pipelines must not leak background work.
	runJogAsGit(t, tr.Dir, "status")
	if _, _, exists := readTrimStamp(t, tr); exists {
		t.Fatal("a CI run wrote the auto-trim stamp")
	}

	// CI cleared: the passthrough stamps the default daily cadence and
	// spawns a background trim, which drops the 120-day snapshot.
	if _, stderr, code := runJogEnv(t, tr.Dir, []string{"CI="}, "git", "status"); code != 0 {
		t.Fatalf("passthrough: code=%d\n%s", code, stderr)
	}
	stampedAt, secs, exists := readTrimStamp(t, tr)
	if !exists {
		t.Fatal("no auto-trim stamp after the passthrough")
	}
	if secs != 86400 {
		t.Errorf("stamp interval = %d, want 86400 (default daily)", secs)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if out, err := tr.TryGit("log", "--format=%s", ref); err == nil && !strings.Contains(out, "manual: ancient") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background trim never dropped the ancient snapshot:\n%s", tr.Git("log", "--format=%s", ref))
		}
		time.Sleep(50 * time.Millisecond)
	}
	tr.Git("rev-parse", "refs/jog/@trash/main") // the insurance ref proves it was a real trim

	// A manual trim resets the clock: the stamp advances past the spawn's.
	runJog(t, tr.Dir, "trim")
	if trimmedAt, _, _ := readTrimStamp(t, tr); !trimmedAt.After(stampedAt) {
		t.Errorf("manual trim did not advance the stamp: %v -> %v", stampedAt, trimmedAt)
	}

	// autoTrim false: the passthrough stamps the disabled marker and
	// spawns nothing.
	tr2 := testrepo.New(t)
	tr2.Write("b.txt", "x\n")
	tr2.Commit("base")
	tr2.Git("config", "jog.autoTrim", "false")
	runJogEnv(t, tr2.Dir, []string{"CI="}, "git", "status")
	if _, secs, exists := readTrimStamp(t, tr2); !exists || secs != -1 {
		t.Errorf("disabled stamp = (%d, %v), want (-1, true)", secs, exists)
	}

	// `jog config` pushes a cadence change into the stamp immediately.
	if _, stderr, code := runJog(t, tr2.Dir, "config", "autoTrim", "3600"); code != 0 {
		t.Fatalf("config autoTrim: code=%d\n%s", code, stderr)
	}
	if _, secs, _ := readTrimStamp(t, tr2); secs != 3600 {
		t.Errorf("synced stamp interval = %d, want 3600", secs)
	}
}

// TestVerbAliases: snaps and pick are log, back is restore — same code
// path, same output — and provenance records the verb the user typed.
func TestVerbAliases(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Write("other.txt", "x\n")
	tr.Commit("base")
	runJog(t, tr.Dir, "-m", "touches a")
	tr.Write("other.txt", "y\n")
	runJog(t, tr.Dir, "-m", "touches other only")
	tr.Write("a.txt", "two\n")
	runJog(t, tr.Dir, "-m", "touches a again")

	// The path-scoped timeline (pick's old job) via each verb, on a clean
	// tree so no run mints a snapshot: byte-identical output.
	want, stderr, code := runJog(t, tr.Dir, "log", "--format=%H %s", "a.txt")
	if code != 0 {
		t.Fatalf("log exited %d: %s", code, stderr)
	}
	if !strings.Contains(want, "touches a again") || !strings.Contains(want, "touches a") {
		t.Errorf("log list missing a.txt versions:\n%s", want)
	}
	if strings.Contains(want, "touches other only") {
		t.Errorf("log listed a snapshot that did not change the file:\n%s", want)
	}
	for _, alias := range []string{"snaps", "pick"} {
		got, _, code := runJog(t, tr.Dir, alias, "--format=%H %s", "a.txt")
		if code != 0 || got != want {
			t.Errorf("%s output differs from log (code=%d):\n%s", alias, code, got)
		}
	}

	// A dirty tree run records the typed verb in provenance.
	tr.Write("a.txt", "three\n")
	runJog(t, tr.Dir, "snaps", "-n", "1", "--format=%h")
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: jog snaps -n 1 --format=%h" {
		t.Errorf("alias provenance = %q, want the typed verb", got)
	}

	// back restores like restore, and the undo hint speaks restore. A dirty
	// tree, so the mandatory pre-restore snapshot records the typed verb.
	tr.Write("a.txt", "four\n")
	stdout, stderr, code := runJog(t, tr.Dir, "back", "a.txt")
	if code != 0 {
		t.Fatalf("back exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "restored a.txt") || !strings.Contains(stdout, "undo: jog restore a.txt") {
		t.Errorf("back alias output:\n%s", stdout)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: jog back a.txt" {
		t.Errorf("back provenance = %q, want the typed verb", got)
	}
}

// mustHelp runs `jog help <words…>` and returns stdout, failing the test
// on a non-zero exit.
func mustHelp(t *testing.T, dir string, words ...string) string {
	t.Helper()
	stdout, _, code := runJog(t, dir, append([]string{"help"}, words...)...)
	if code != 0 {
		t.Fatalf("help %v: code=%d", words, code)
	}
	return stdout
}

// TestPerCommandHelp: every jog verb answers --help and `jog help <verb>`
// with a usage block — except git, where every argument (including
// --help) belongs to real git. (TestHelp covers the global forms.)
func TestPerCommandHelp(t *testing.T) {
	dir := t.TempDir() // outside any repo: help must not need one
	verbs := []string{"log", "snaps", "pick", "since", "restore", "back", "branches", "branch", "trim", "config", "doctor", "hook", "agents", "agent", "editors", "editor", "editor-hook", "shell-hook", "update", "shell", "install", "uninstall"}
	for _, v := range verbs {
		stdout, _, code := runJog(t, dir, v, "--help")
		if code != 0 || !strings.Contains(stdout, "usage:") || !strings.Contains(stdout, v) {
			t.Errorf("%s --help: code=%d\n%s", v, code, stdout)
		}
		short, _, code := runJog(t, dir, v, "-h")
		if code != 0 || short != stdout {
			t.Errorf("%s -h differs from --help (code=%d)", v, code)
		}
		byName, _, code := runJog(t, dir, "help", v)
		if code != 0 || byName != stdout {
			t.Errorf("help %s differs from %s --help (code=%d)", v, v, code)
		}
	}

	// Command groups have per-subcommand pages, reachable as
	// `jog help <group> <sub>` and `jog <group> <sub> --help` alike.
	for _, group := range []string{"agents", "editors", "shell"} {
		for _, sub := range []string{"install", "uninstall", "list"} {
			stdout, _, code := runJog(t, dir, group, sub, "--help")
			if code != 0 || !strings.Contains(stdout, "usage:") || !strings.Contains(stdout, "jog "+group+" "+sub) {
				t.Errorf("%s %s --help: code=%d\n%s", group, sub, code, stdout)
			}
			byName, _, code := runJog(t, dir, "help", group, sub)
			if code != 0 || byName != stdout {
				t.Errorf("help %s %s differs from --help (code=%d)", group, sub, code)
			}
			if stdout == mustHelp(t, dir, group) {
				t.Errorf("%s %s shows the group page, not its own", group, sub)
			}
		}
	}
	// Group aliases resolve nested pages too.
	if a, b := mustHelp(t, dir, "agent", "list"), mustHelp(t, dir, "agents", "list"); a != b {
		t.Error("agent list help differs from agents list")
	}
	// An unknown subcommand falls back to the group page.
	if a, b := mustHelp(t, dir, "agents", "bogus"), mustHelp(t, dir, "agents"); a != b {
		t.Error("agents bogus should fall back to the agents page")
	}

	// The flag wins even mixed into other arguments.
	stdout, _, code := runJog(t, dir, "back", "some/path", "--help")
	if code != 0 || !strings.Contains(stdout, "usage:") {
		t.Errorf("back <path> --help: code=%d\n%s", code, stdout)
	}

	// git passthrough: --help must reach real git, not jog's help.
	stdout, _, code = runJog(t, dir, "git", "--help")
	if code != 0 || !strings.Contains(stdout, "usage: git") || strings.Contains(stdout, "jog git —") {
		t.Errorf("git --help intercepted: code=%d\n%.200s", code, stdout)
	}
	// …its jog-side story lives at `jog help git` instead.
	stdout, _, code = runJog(t, dir, "help", "git")
	if code != 0 || !strings.Contains(stdout, "jog git") || !strings.Contains(stdout, "passthrough") {
		t.Errorf("help git: code=%d\n%s", code, stdout)
	}

	_, stderr, code := runJog(t, dir, "help", "frobnicate")
	if code != 2 || !strings.Contains(stderr, "no help") {
		t.Errorf("help for unknown verb: code=%d stderr=%q", code, stderr)
	}
}

// The compact time shorthand (30m, 1h, 2d, 1w) is jog's primary documented
// syntax, and git itself cannot parse it: "@{1h}" reads as an ancient date
// and falls back to the oldest reflog entry — the "since 1h shows
// everything" bug. jog translates the shorthand before resolution; this
// pins that `since 1h` means the chain state an hour ago, and that
// `back --at` shares the fix.
func TestShortTimeTargets(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("base")
	head := tr.Git("rev-parse", "HEAD")
	ref := "refs/jog/main"
	now := time.Now()
	old := mintSnap(t, tr, ref, "three hours old\n", now.Add(-3*time.Hour), head)
	recent := mintSnap(t, tr, ref, "ninety minutes old\n", now.Add(-90*time.Minute), head)

	tr.Write("w.txt", "current\n")
	stdout, stderr, code := runJog(t, tr.Dir, "since", "1h")
	if code != 0 {
		t.Fatalf("since 1h: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, recent[:7]) {
		t.Errorf("since 1h: want target %s (the state an hour ago), got:\n%s", recent[:7], stdout)
	}
	if strings.Contains(stdout, old[:7]) {
		t.Errorf("since 1h fell back to the oldest snapshot:\n%s", stdout)
	}

	if _, stderr, code := runJog(t, tr.Dir, "restore", "w.txt", "--at", "2h"); code != 0 {
		t.Fatalf("back --at 2h: code=%d stderr=%s", code, stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(tr.Dir, "w.txt")); string(b) != "three hours old\n" {
		t.Errorf("back --at 2h restored %q, want the three-hour-old version", b)
	}
}

// TestAgents: the full lifecycle at user scope — one command installs
// both surfaces, idempotent reinstall, list reflects state, surface
// selection, the modified-skill refusal, unknown clients, and the
// singular alias.
func TestAgents(t *testing.T) {
	home := t.TempDir()
	// Client detection: ~/.claude existing is the signal (no claude binary
	// needed on PATH).
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := fakeHome(home)
	dir := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	skillPath := filepath.Join(home, ".claude", "skills", "jog", "SKILL.md")

	stdout, stderr, code := runJogEnv(t, dir, env, "agents", "install")
	if code != 0 {
		t.Fatalf("install: code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"hooks", "skill", "PreToolUse", "UserPromptSubmit", "uninstall"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install output missing %q:\n%s", want, stdout)
		}
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings not valid JSON: %v", err)
	}
	sb, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(sb), "---\n") || !strings.Contains(string(sb), "name: jog") {
		t.Errorf("skill missing frontmatter:\n%.120s", sb)
	}

	// Idempotent: both surfaces report already-done, nothing changes.
	stdout, _, code = runJogEnv(t, dir, env, "agents", "install")
	if code != 0 || !strings.Contains(stdout, "already wired") || !strings.Contains(stdout, "up to date") {
		t.Errorf("reinstall: code=%d\n%s", code, stdout)
	}

	stdout, _, code = runJogEnv(t, dir, env, "agents", "list")
	if code != 0 || !strings.Contains(stdout, "settings.json") || !strings.Contains(stdout, "SKILL.md") {
		t.Errorf("list after install:\n%s", stdout)
	}

	// Surface selection: uninstall hooks only; the skill must survive.
	stdout, _, code = runJogEnv(t, dir, env, "agents", "uninstall", "hooks")
	if code != 0 || !strings.Contains(stdout, "removed") {
		t.Errorf("uninstall hooks: code=%d\n%s", code, stdout)
	}
	if b, _ := os.ReadFile(settings); strings.Contains(string(b), "jog hook claude") {
		t.Error("jog hooks survive `agents uninstall hooks`")
	}
	if _, err := os.Stat(skillPath); err != nil {
		t.Error("skill removed by `agents uninstall hooks`")
	}

	// The modified-skill refusal, then a clean removal.
	if err := os.WriteFile(skillPath, append(sb, []byte("local edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runJogEnv(t, dir, env, "agents", "uninstall", "skill")
	if code != 1 || !strings.Contains(stderr, "differs") {
		t.Errorf("uninstall modified skill: code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(skillPath); err != nil {
		t.Error("modified skill was deleted")
	}
	if err := os.WriteFile(skillPath, sb, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, dir, env, "agents", "uninstall", "skill")
	if code != 0 || !strings.Contains(stdout, "removed") {
		t.Errorf("uninstall skill: code=%d\n%s", code, stdout)
	}
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Error("skill still present after uninstall")
	}

	stdout, _, code = runJogEnv(t, dir, env, "agents", "list")
	if code != 0 || !strings.Contains(stdout, "not installed") {
		t.Errorf("list after uninstall:\n%s", stdout)
	}

	_, stderr, code = runJogEnv(t, dir, env, "agents", "install", "clippy")
	if code != 2 || !strings.Contains(stderr, "supported") {
		t.Errorf("unknown client: code=%d stderr=%q", code, stderr)
	}

	// jog config: the self-documenting settings list, get/set/unset
	// through real git config, values vetted by git's own parsers.
	tr3 := testrepo.New(t)
	tr3.Write("a.txt", "x\n")
	tr3.Commit("base")
	stdout, _, code = runJog(t, tr3.Dir, "config")
	for _, want := range []string{"maxFileSize", "keep", "(default)"} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("config list missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "jog.maxFileSize") {
		t.Errorf("list shows the git-config prefix — jog's surface is prefix-less:\n%s", stdout)
	}
	if stdout, _, _ = runJog(t, tr3.Dir, "config", "maxFileSize"); strings.TrimSpace(stdout) != "52428800" {
		t.Errorf("unset get = %q, want the default", stdout)
	}
	if _, _, code = runJog(t, tr3.Dir, "config", "jog.maxFileSize", "100M"); code != 0 {
		t.Errorf("set exited %d", code)
	}
	if got := tr3.Git("config", "--get", "jog.maxFileSize"); got != "100M" {
		t.Errorf("git sees %q after set", got)
	}
	if stdout, _, _ = runJog(t, tr3.Dir, "config", "MAXFILESIZE"); strings.TrimSpace(stdout) != "100M" {
		t.Errorf("case-insensitive get = %q", stdout)
	}
	if stdout, _, code = runJog(t, tr3.Dir, "config", "--unset", "maxFileSize"); code != 0 || !strings.Contains(stdout, "default") {
		t.Errorf("unset: code=%d %q", code, stdout)
	}
	if _, err := tr3.TryGit("config", "--get", "jog.maxFileSize"); err == nil {
		t.Error("value survived --unset")
	}
	if _, stderr, code = runJog(t, tr3.Dir, "config", "keep", "banana"); code != 2 || !strings.Contains(stderr, "not a valid value") {
		t.Errorf("invalid value: code=%d stderr=%q", code, stderr)
	}
	if _, err := tr3.TryGit("config", "--get", "jog.keep"); err == nil {
		t.Error("invalid value reached the real config")
	}
	if _, stderr, code = runJog(t, tr3.Dir, "config", "jog.nonsense"); code != 2 || !strings.Contains(stderr, "unknown setting") {
		t.Errorf("unknown setting: code=%d stderr=%q", code, stderr)
	}

	stdout, _, code = runJogEnv(t, dir, env, "agent", "list")
	if code != 0 || !strings.Contains(stdout, "claude") || !strings.Contains(stdout, "codex") {
		t.Errorf("singular alias: code=%d\n%s", code, stdout)
	}

	// A bare command group prints its help — the command list — not an error.
	stdout, _, code = runJogEnv(t, dir, env, "agents")
	if code != 0 || !strings.Contains(stdout, "commands:") || !strings.Contains(stdout, "install") {
		t.Errorf("bare agents: code=%d\n%s", code, stdout)
	}
}

// TestAgentsCodex covers Codex detection, its official user-level hook and
// skill locations, idempotence, project-root anchoring, and clean removal.
func TestAgentsCodex(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := fakeHome(home)
	dir := t.TempDir()
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	skillPath := filepath.Join(home, ".agents", "skills", "jog", "SKILL.md")

	stdout, stderr, code := runJogEnv(t, dir, env, "agents", "install")
	if code != 0 {
		t.Fatalf("codex install: code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"codex", "PreToolUse", "UserPromptSubmit", "/hooks", ".agents"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("codex install output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "claude hooks") {
		t.Errorf("undetected Claude was installed:\n%s", stdout)
	}
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), hookNeedle("codex")) || !strings.Contains(string(b), "Bash|Edit|Write") {
		t.Errorf("unexpected Codex hooks:\n%s", b)
	}
	if sb, err := os.ReadFile(skillPath); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(sb), "name: jog") {
		t.Errorf("Codex skill missing frontmatter:\n%.120s", sb)
	}

	stdout, _, code = runJogEnv(t, dir, env, "agents", "install", "codex")
	if code != 0 || !strings.Contains(stdout, "already wired") || !strings.Contains(stdout, "up to date") {
		t.Errorf("codex reinstall: code=%d\n%s", code, stdout)
	}
	stdout, _, code = runJogEnv(t, dir, env, "agents", "list", "codex")
	if code != 0 || !strings.Contains(stdout, "~/.codex/hooks.json") || !strings.Contains(stdout, "~/.agents/skills") {
		t.Errorf("codex list after install: code=%d\n%s", code, stdout)
	}

	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("base")
	sub := filepath.Join(tr.Dir, "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, code = runJogEnv(t, sub, env, "agents", "install", "codex", "--project")
	if code != 0 {
		t.Fatalf("codex --project install: code=%d", code)
	}
	for _, path := range []string{
		filepath.Join(tr.Dir, ".codex", "hooks.json"),
		filepath.Join(tr.Dir, ".agents", "skills", "jog", "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("codex --project output missing at %s: %v", path, err)
		}
	}
	runJog(t, tr.Dir, "-m", "codex doctor")
	stdout, _, code = runJogEnv(t, tr.Dir, env, "doctor")
	if code != 0 || !strings.Contains(stdout, "codex hooks") || !strings.Contains(stdout, "codex skill") {
		t.Errorf("doctor did not recognize Codex integration: code=%d\n%s", code, stdout)
	}

	stdout, _, code = runJogEnv(t, dir, env, "agents", "uninstall", "codex")
	if code != 0 || !strings.Contains(stdout, "removed 2") || !strings.Contains(stdout, "SKILL.md") {
		t.Errorf("codex uninstall: code=%d\n%s", code, stdout)
	}
	if b, _ := os.ReadFile(hooksPath); strings.Contains(string(b), "jog hook codex") {
		t.Errorf("Codex hooks survive uninstall:\n%s", b)
	}
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Error("Codex skill survives uninstall")
	}

	if err := os.WriteFile(hooksPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runJogEnv(t, dir, env, "agents", "install", "hooks", "codex")
	if code != 1 || !strings.Contains(stderr, "not valid JSON") {
		t.Errorf("malformed Codex hooks: code=%d stderr=%q", code, stderr)
	}
	if b, _ := os.ReadFile(hooksPath); string(b) != "{not json" {
		t.Error("malformed Codex hooks file was rewritten")
	}
}

// TestAgentsMoreClients: the copilot, cursor, gemini, and opencode
// integrations — each lands in its own config in its own dialect, is
// idempotent, and uninstalls surgically.
func TestAgentsMoreClients(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".copilot", ".cursor", ".gemini", filepath.Join(".config", "opencode")} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := fakeHome(home)
	dir := t.TempDir()

	// A pre-existing foreign Cursor hook must survive everything below.
	cursorHooks := filepath.Join(home, ".cursor", "hooks.json")
	seed := `{"version":1,"hooks":{"beforeShellExecution":[{"command":"echo audit"}]}}`
	if err := os.WriteFile(cursorHooks, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runJogEnv(t, dir, env, "agents", "install")
	if code != 0 {
		t.Fatalf("install: code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"copilot", "cursor", "gemini", "opencode"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "claude hooks") {
		t.Errorf("undetected claude was installed:\n%s", stdout)
	}

	// Each client's hook config, in its own dialect.
	checks := []struct {
		path string
		want []string
	}{
		{filepath.Join(home, ".copilot", "settings.json"),
			[]string{"PreToolUse", "UserPromptSubmit", "Bash|Edit|Write", hookNeedle("copilot")}},
		{cursorHooks,
			[]string{"beforeShellExecution", "afterFileEdit", "beforeSubmitPrompt", hookNeedle("cursor"), "echo audit"}},
		{filepath.Join(home, ".gemini", "settings.json"),
			[]string{"BeforeAgent", "BeforeTool", "write_file|replace|run_shell_command", `"name": "jog"`, hookNeedle("gemini")}},
		// The opencode plugin is a static asset invoking bare `jog` from
		// PATH — its needle is literal on every OS.
		{filepath.Join(home, ".config", "opencode", "plugins", "jog.js"),
			[]string{"jog hook opencode", "tool.execute.before", "chat.message"}},
	}
	for _, c := range checks {
		b, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		for _, want := range c.want {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s missing %q:\n%s", c.path, want, b)
			}
		}
	}
	for _, p := range []string{
		filepath.Join(home, ".copilot", "skills", "jog", "SKILL.md"),
		filepath.Join(home, ".cursor", "skills", "jog", "SKILL.md"),
		filepath.Join(home, ".gemini", "skills", "jog", "SKILL.md"),
		filepath.Join(home, ".config", "opencode", "skills", "jog", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("skill missing at %s: %v", p, err)
		}
	}

	// Idempotent: everything reports already-done, and the changed-work
	// footer (which only prints when something was written) stays away.
	stdout, _, code = runJogEnv(t, dir, env, "agents", "install")
	if code != 0 || !strings.Contains(stdout, "already wired") ||
		!strings.Contains(stdout, "already up to date") || strings.Contains(stdout, "jog agents uninstall") {
		t.Errorf("reinstall not idempotent: code=%d\n%s", code, stdout)
	}

	stdout, _, code = runJogEnv(t, dir, env, "agents", "list")
	for _, want := range []string{"~/.copilot/settings.json", "~/.cursor/hooks.json",
		"~/.gemini/settings.json", "~/.config/opencode/plugins/jog.js"} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("list missing %q:\n%s", want, stdout)
		}
	}

	// Uninstall: jog's entries gone, the foreign Cursor hook untouched.
	stdout, stderr, code = runJogEnv(t, dir, env, "agents", "uninstall")
	if code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%s", code, stderr)
	}
	b, err := os.ReadFile(cursorHooks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "jog hook cursor") || !strings.Contains(string(b), "echo audit") {
		t.Errorf("cursor uninstall not surgical:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "plugins", "jog.js")); !os.IsNotExist(err) {
		t.Error("opencode plugin survives uninstall")
	}

	// An edited opencode plugin is refused, like an edited skill.
	plugin := filepath.Join(home, ".config", "opencode", "plugins", "jog.js")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("// my own plugin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runJogEnv(t, dir, env, "agents", "uninstall", "hooks", "opencode")
	if code != 1 || !strings.Contains(stderr, "differs") {
		t.Errorf("edited plugin: code=%d stderr=%q", code, stderr)
	}
}

// TestAgentsDetectionAndScope: undetected clients are skipped (and
// nothing is created), naming a client overrides detection, --project
// lands in the repo's personal settings + committable skills, and
// malformed settings are a hard error that never rewrites the file.
func TestAgentsDetectionAndScope(t *testing.T) {
	home := t.TempDir() // no ~/.claude, and PATH below carries no claude binary
	env := fakeHome(home)
	dir := t.TempDir()

	stdout, _, code := runJogEnv(t, dir, env, "agents", "install")
	if code != 0 || !strings.Contains(stdout, "skipped") {
		t.Fatalf("undetected install: code=%d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("skipped install still created ~/.claude")
	}

	// Naming the client overrides detection.
	stdout, stderr, code := runJogEnv(t, dir, env, "agents", "install", "claude")
	if code != 0 || !strings.Contains(stdout, "wired") {
		t.Fatalf("forced install: code=%d stderr=%s\n%s", code, stderr, stdout)
	}

	// --project: personal settings and committable skill at the repo
	// toplevel, found from a subdirectory.
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("base")
	sub := filepath.Join(tr.Dir, "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, code = runJogEnv(t, sub, env, "agents", "install", "claude", "--project")
	if code != 0 {
		t.Fatalf("--project install: code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, ".claude", "settings.local.json")); err != nil {
		t.Errorf("--project hooks not in settings.local.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, ".claude", "skills", "jog", "SKILL.md")); err != nil {
		t.Errorf("--project skill not at repo toplevel: %v", err)
	}

	// Malformed settings: hard error, file untouched.
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runJogEnv(t, dir, env, "agents", "install", "claude")
	if code != 1 || !strings.Contains(stderr, "not valid JSON") {
		t.Errorf("malformed settings: code=%d stderr=%q", code, stderr)
	}
	if b, _ := os.ReadFile(settings); string(b) != "{not json" {
		t.Error("malformed settings file was rewritten")
	}
}

// TestAgentsUninstallSurgical: uninstall removes exactly the entries that
// invoke jog — a foreign top-level key, a foreign hook sharing our event,
// and a foreign entry sharing a matcher group with ours all survive; the
// structures jog emptied are pruned.
func TestAgentsUninstallSurgical(t *testing.T) {
	home := t.TempDir()
	env := fakeHome(home)
	dir := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [
        {"type": "command", "command": "jog hook claude"},
        {"type": "command", "command": "echo other"}
      ]},
      {"matcher": "Write", "hooks": [{"type": "command", "command": "prettier"}]}
    ],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "jog hook claude"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "notify-send done"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := runJogEnv(t, dir, env, "agents", "uninstall", "hooks", "claude")
	if code != 0 || !strings.Contains(stdout, "removed 2") {
		t.Fatalf("uninstall: code=%d\n%s", code, stdout)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings not valid JSON after uninstall: %v", err)
	}
	if m["model"] != "opus" {
		t.Error("unrelated top-level key lost")
	}
	s := string(b)
	if strings.Contains(s, "jog hook claude") {
		t.Errorf("jog entries survive uninstall:\n%s", s)
	}
	for _, want := range []string{"echo other", "prettier", "notify-send done"} {
		if !strings.Contains(s, want) {
			t.Errorf("foreign hook %q lost:\n%s", want, s)
		}
	}
	hooks, _ := m["hooks"].(map[string]any)
	if hooks["UserPromptSubmit"] != nil {
		t.Error("emptied UserPromptSubmit event not pruned")
	}

	stdout, _, code = runJogEnv(t, dir, env, "agents", "uninstall", "hooks")
	if code != 0 || !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("uninstall twice: code=%d\n%s", code, stdout)
	}
}

// TestEditors: the vim lifecycle at user scope — install writes the
// plugin and teaches the caveats, reinstall is idempotent but still
// teaches, uninstall refuses an edited file and removes a pristine one.
func TestEditors(t *testing.T) {
	home := t.TempDir()
	env := fakeHome(home)
	dir := t.TempDir()
	hookFile := vimPluginPath(home)

	stdout, stderr, code := runJogEnv(t, dir, env, "editors", "install", "vim")
	if code != 0 {
		t.Fatalf("install: code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"installed", "every save inside a git repo", "jog editors uninstall vim"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install output missing %q:\n%s", want, stdout)
		}
	}
	b, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "editor-hook") {
		t.Errorf("plugin does not invoke the editor hook:\n%.200s", b)
	}

	// Idempotent — and the notes still print: re-running install is how
	// you re-read the caveats.
	stdout, _, code = runJogEnv(t, dir, env, "editors", "install", "vim")
	if code != 0 || !strings.Contains(stdout, "already up to date") || !strings.Contains(stdout, "every save inside a git repo") {
		t.Errorf("reinstall: code=%d\n%s", code, stdout)
	}

	// TildePath renders home-relative with forward slashes on every OS.
	tildeHook := "~/" + filepath.ToSlash(strings.TrimPrefix(hookFile, home+string(filepath.Separator)))
	stdout, _, code = runJogEnv(t, dir, env, "editors", "list")
	if code != 0 || !strings.Contains(stdout, tildeHook) || !strings.Contains(stdout, "✓ installed") {
		t.Errorf("list after install:\n%s", stdout)
	}

	// The singular alias speaks the same command.
	single, _, code := runJogEnv(t, dir, env, "editor", "list")
	if code != 0 || single != stdout {
		t.Errorf("`jog editor list` differs from `jog editors list` (code=%d)", code)
	}

	// An edited hook file is the user's now — uninstall refuses.
	if err := os.WriteFile(hookFile, append(b, []byte("\" my tweak\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runJogEnv(t, dir, env, "editors", "uninstall", "vim")
	if code != 1 || !strings.Contains(stderr, "differs") {
		t.Errorf("uninstall edited: code=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(hookFile); err != nil {
		t.Errorf("edited hook file was removed despite the refusal")
	}

	// Pristine again: uninstall removes it cleanly.
	if err := os.WriteFile(hookFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, dir, env, "editors", "uninstall", "vim")
	if code != 0 || !strings.Contains(stdout, "removed") {
		t.Errorf("uninstall: code=%d\n%s", code, stdout)
	}
	if _, err := os.Stat(hookFile); !os.IsNotExist(err) {
		t.Errorf("hook file survived uninstall")
	}
	stdout, _, code = runJogEnv(t, dir, env, "editors", "list", "vim")
	if code != 0 || !strings.Contains(stdout, "not installed") {
		t.Errorf("list after uninstall:\n%s", stdout)
	}

	// A bare command group prints its help — the command list — not an error.
	stdout, _, code = runJogEnv(t, dir, env, "editors")
	if code != 0 || !strings.Contains(stdout, "commands:") || !strings.Contains(stdout, "install") {
		t.Errorf("bare editors: code=%d\n%s", code, stdout)
	}
}

// TestEditorsExactlyOne: install/uninstall take exactly one editor —
// zero, several, and unknown names are each usage errors that say so.
func TestEditorsExactlyOne(t *testing.T) {
	home := t.TempDir()
	env := fakeHome(home)
	dir := t.TempDir()

	_, stderr, code := runJogEnv(t, dir, env, "editors", "install")
	if code != 2 || !strings.Contains(stderr, "exactly one") || !strings.Contains(stderr, "jog editors list") {
		t.Errorf("install no name: code=%d stderr=%s", code, stderr)
	}
	_, stderr, code = runJogEnv(t, dir, env, "editors", "install", "vim", "emacs")
	if code != 2 || !strings.Contains(stderr, "one editor at a time") {
		t.Errorf("install two names: code=%d stderr=%s", code, stderr)
	}
	_, stderr, code = runJogEnv(t, dir, env, "editors", "uninstall")
	if code != 2 || !strings.Contains(stderr, "exactly one") {
		t.Errorf("uninstall no name: code=%d stderr=%s", code, stderr)
	}
	_, stderr, code = runJogEnv(t, dir, env, "editors", "install", "nano")
	if code != 2 || !strings.Contains(stderr, `unknown editor "nano"`) || !strings.Contains(stderr, "supported:") {
		t.Errorf("unknown editor: code=%d stderr=%s", code, stderr)
	}
}

// TestEditorsJetBrains: the one per-project editor. Outside a repo or
// without .idea the install explains itself; inside, the XML merge adds
// exactly jog's watcher and uninstall removes exactly it.
func TestEditorsJetBrains(t *testing.T) {
	home := t.TempDir()
	env := fakeHome(home)

	// Not a repo: refused with the .idea story.
	_, stderr, code := runJogEnv(t, t.TempDir(), env, "editors", "install", "jetbrains")
	if code != 1 || !strings.Contains(stderr, "git repository") {
		t.Errorf("outside repo: code=%d stderr=%s", code, stderr)
	}

	// A repo without .idea: jog won't invent project structure.
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("base")
	_, stderr, code = runJogEnv(t, tr.Dir, env, "editors", "install", "jetbrains")
	if code != 1 || !strings.Contains(stderr, ".idea") {
		t.Errorf("no .idea: code=%d stderr=%s", code, stderr)
	}

	// Seed .idea with a foreign watcher: it must survive in value terms.
	if err := os.MkdirAll(filepath.Join(tr.Dir, ".idea"), 0o755); err != nil {
		t.Fatal(err)
	}
	watcher := filepath.Join(tr.Dir, ".idea", "watcherTasks.xml")
	foreign := `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="ProjectTasksOptions">
    <TaskOptions isEnabled="true">
      <option name="name" value="prettier" />
      <option name="program" value="prettier" />
    </TaskOptions>
  </component>
</project>
`
	if err := os.WriteFile(watcher, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install from a subdirectory: the file lands at the toplevel.
	sub := filepath.Join(tr.Dir, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runJogEnv(t, sub, env, "editors", "install", "jetbrains")
	if code != 0 {
		t.Fatalf("install: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "File Watchers plugin") || !strings.Contains(stdout, "re-run this in each project") {
		t.Errorf("install notes missing:\n%s", stdout)
	}
	b, err := os.ReadFile(watcher)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"editor-hook jetbrains $FilePath$", "prettier", `scopeName" value="Project Files`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("watcherTasks.xml missing %q:\n%s", want, b)
		}
	}

	// Idempotent.
	stdout, _, code = runJogEnv(t, tr.Dir, env, "editors", "install", "jetbrains")
	if code != 0 || !strings.Contains(stdout, "already wired") {
		t.Errorf("reinstall: code=%d\n%s", code, stdout)
	}

	// list shows project scope.
	stdout, _, code = runJogEnv(t, tr.Dir, env, "editors", "list", "jetbrains")
	if code != 0 || !strings.Contains(stdout, "(project)") {
		t.Errorf("list: code=%d\n%s", code, stdout)
	}

	// Uninstall removes exactly jog's watcher; prettier survives.
	stdout, _, code = runJogEnv(t, tr.Dir, env, "editors", "uninstall", "jetbrains")
	if code != 0 || !strings.Contains(stdout, "everything else untouched") {
		t.Errorf("uninstall: code=%d\n%s", code, stdout)
	}
	b, err = os.ReadFile(watcher)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "editor-hook jetbrains") || !strings.Contains(string(b), "prettier") {
		t.Errorf("uninstall was not surgical:\n%s", b)
	}
	stdout, _, code = runJogEnv(t, tr.Dir, env, "editors", "uninstall", "jetbrains")
	if code != 0 || !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("uninstall twice: code=%d\n%s", code, stdout)
	}
}

// TestEditorHookEndToEnd: the wired command itself — exit 0 with zero
// output everywhere, and a `<editor>: save <path>` subject inside a repo.
func TestEditorHookEndToEnd(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("base")
	tr.Write("b.txt", "fresh\n") // a clean tree would no-op the snapshot

	stdout, stderr, code := runJog(t, tr.Dir, "editor-hook", "vim", filepath.Join(tr.Dir, "b.txt"))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("editor-hook: code=%d stdout=%q stderr=%q — must be silent", code, stdout, stderr)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "vim: save b.txt" {
		t.Errorf("subject = %q", got)
	}

	// Outside a repo: silent no-op, still exit 0.
	loose := t.TempDir()
	file := filepath.Join(loose, "loose.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runJog(t, loose, "editor-hook", "vim", file)
	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("outside repo: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// Even bare misuse exits 0 — the iron rule.
	_, _, code = runJog(t, loose, "editor-hook")
	if code != 0 {
		t.Errorf("bare editor-hook: code=%d, want 0", code)
	}
}

// TestEditorsVSCode: the extension lands in every root a VS Code on this
// machine reads — the desktop dir, and the Remote-SSH server dir when it
// exists (a remote window's extension host loads only from the latter).
func TestEditorsVSCode(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".vscode-server"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := fakeHome(home)
	dir := t.TempDir()
	desktop := filepath.Join(home, ".vscode", "extensions", "jog.jog-0.0.1")
	server := filepath.Join(home, ".vscode-server", "extensions", "jog.jog-0.0.1")

	// Only the server root exists: desktop is not invented.
	stdout, stderr, code := runJogEnv(t, dir, env, "editors", "install", "vscode")
	if code != 0 {
		t.Fatalf("install: code=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(server, "extension.js")); err != nil {
		t.Errorf("server extension missing: %v", err)
	}
	if _, err := os.Stat(desktop); !os.IsNotExist(err) {
		t.Errorf("desktop root invented despite ~/.vscode not existing")
	}
	if !strings.Contains(stdout, "Remote-SSH covered") {
		t.Errorf("install notes missing the remote story:\n%s", stdout)
	}

	// Desktop appears (say, the app got installed): both roots covered.
	if err := os.MkdirAll(filepath.Join(home, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, code = runJogEnv(t, dir, env, "editors", "install", "vscode")
	if code != 0 {
		t.Fatalf("second install: code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(desktop, "extension.js")); err != nil {
		t.Errorf("desktop extension missing after root appeared: %v", err)
	}

	// A foreign-machine rendering (VS Code's install-on-remote copies the
	// other machine's baked path) is still jog's to overwrite and remove.
	if err := os.WriteFile(filepath.Join(server, "extension.js"),
		bytes.Replace(mustRead(t, filepath.Join(desktop, "extension.js")), []byte(`"`+filepath.ToSlash(jogBin)+`"`), []byte(`"/opt/homebrew/bin/jog"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, dir, env, "editors", "install", "vscode")
	if code != 0 || !strings.Contains(stdout, "updated — "+server) {
		t.Errorf("foreign rendering not updated: code=%d\n%s", code, stdout)
	}

	stdout, _, code = runJogEnv(t, dir, env, "editors", "uninstall", "vscode")
	if code != 0 || !strings.Contains(stdout, "removed") {
		t.Errorf("uninstall: code=%d\n%s", code, stdout)
	}
	for _, d := range []string{desktop, server} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived uninstall", d)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// loginShellFixture is the login shell the tests can rely on per OS:
// $SHELL=/bin/zsh on unix, always PowerShell on Windows (no $SHELL
// there). Returns the shell's name, rc path, tilde display, and the
// marked line `jog shell install` writes.
func loginShellFixture(home string) (name, rc, display, markedLine string) {
	if runtime.GOOS == "windows" {
		return "powershell",
			filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
			"~/Documents/PowerShell/Microsoft.PowerShell_profile.ps1",
			"function git { jog git @args } # jog — added by `jog shell install`"
	}
	return "zsh", filepath.Join(home, ".zshrc"), "~/.zshrc",
		"alias git='jog git' # jog — added by `jog shell install`"
}

// loginPreexecLine is the marked preexec line `jog shell install`
// writes for the login shell fixture — "" on Windows, where the login
// shell is PowerShell and preexec is unsupported.
func loginPreexecLine() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return `__jog_preexec() { command -v jog >/dev/null && jog shell-hook -- "$1"; }; preexec_functions+=(__jog_preexec)` +
		" # jog preexec — added by `jog shell install`"
}

// bashPreexecMarked is the marked PS0 line bash gets.
const bashPreexecMarked = `PS0='$(command -v jog >/dev/null && jog shell-hook --history -- "$(HISTTIMEFORMAT= builtin history 1)")'"$PS0"` +
	" # jog preexec — added by `jog shell install`"

// TestShell covers `jog shell`: the two marked lines in rc files — the
// alias by default, the preexec hook behind --preexec, --no-alias
// scoping, hand-added lines left alone, and surgical uninstall.
func TestShell(t *testing.T) {
	home := t.TempDir()
	env := append(fakeHome(home), "SHELL=/bin/zsh")
	loginName, loginRC, loginDisplay, markedLine := loginShellFixture(home)
	preexecLine := loginPreexecLine()

	// No names, no flags: the login shell gets the alias ONLY — the
	// preexec hook is opt-in, never wired silently.
	stdout, _, code := runJogEnv(t, home, env, "shell", "install")
	if code != 0 || !strings.Contains(stdout, "installed — "+loginDisplay) {
		t.Fatalf("shell install: code=%d\n%s", code, stdout)
	}
	b := string(mustRead(t, loginRC))
	if !strings.Contains(b, markedLine) {
		t.Errorf("login rc missing the marked alias line:\n%s", b)
	}
	if strings.Contains(b, "jog shell-hook") {
		t.Errorf("bare install must not wire the preexec hook:\n%s", b)
	}
	// The alias-only install advertises the opt-in.
	if !strings.Contains(stdout, "--preexec") {
		t.Errorf("alias-only install should mention --preexec:\n%s", stdout)
	}

	// --preexec adds the hook. (On Windows the login shell is
	// PowerShell: preexec reports not supported, exit stays 0.)
	stdout, _, code = runJogEnv(t, home, env, "shell", "install", "--preexec")
	if code != 0 {
		t.Fatalf("install --preexec: code=%d\n%s", code, stdout)
	}
	b = string(mustRead(t, loginRC))
	if preexecLine != "" {
		if !strings.Contains(b, preexecLine) {
			t.Errorf("login rc missing the marked preexec line:\n%s", b)
		}
	} else if !strings.Contains(stdout, "not supported") {
		t.Errorf("powershell preexec should say not supported:\n%s", stdout)
	}

	// Idempotent — per surface.
	stdout, _, code = runJogEnv(t, home, env, "shell", "install", "--preexec")
	if code != 0 || !strings.Contains(stdout, "already installed") {
		t.Errorf("re-install: code=%d\n%s", code, stdout)
	}

	// Flag misuse is a usage error: install with nothing left to do,
	// install spelling the default as the removed --no-preexec, and
	// uninstall with install's opt-in flag.
	for _, bad := range [][]string{
		{"shell", "install", "--no-alias"},
		{"shell", "install", "--no-preexec"},
		{"shell", "uninstall", "--preexec"},
		{"shell", "uninstall", "--no-alias", "--no-preexec"},
	} {
		if _, stderr, code := runJogEnv(t, home, env, bad...); code != 2 || !strings.Contains(stderr, "usage") {
			t.Errorf("%v: code=%d %q", bad, code, stderr)
		}
	}

	// Named shells force, whatever the login shell is; existing content
	// is preserved, and fish gets its own spellings under ~/.config.
	const bashMarked = "alias git='jog git' # jog — added by `jog shell install`"
	bashrc := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(bashrc, []byte("export FOO=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, home, env, "shell", "install", "--preexec", "bash", "fish")
	if code != 0 || !strings.Contains(stdout, ".bashrc") || !strings.Contains(stdout, "config.fish") {
		t.Fatalf("forced install: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, bashrc)); !strings.HasPrefix(b, "export FOO=1\n") ||
		!strings.Contains(b, bashMarked) || !strings.Contains(b, bashPreexecMarked) {
		t.Errorf(".bashrc content wrong:\n%s", b)
	}
	fishrc := filepath.Join(home, ".config", "fish", "config.fish")
	if b := string(mustRead(t, fishrc)); !strings.Contains(b, "alias git 'jog git'") ||
		!strings.Contains(b, "--on-event fish_preexec") {
		t.Errorf("config.fish missing the fish spellings:\n%s", b)
	}

	// list shows both surfaces.
	stdout, _, code = runJogEnv(t, home, env, "shell", "list")
	if code != 0 || !strings.Contains(stdout, loginDisplay) || !strings.Contains(stdout, "installed") ||
		!strings.Contains(stdout, "preexec") {
		t.Errorf("list: code=%d\n%s", code, stdout)
	}

	// --no-alias scopes uninstall to the preexec line; the alias line
	// survives byte-identical, and the scoped reinstall never duplicates.
	stdout, _, code = runJogEnv(t, home, env, "shell", "uninstall", "--no-alias", "bash")
	if code != 0 || !strings.Contains(stdout, "removed the preexec hook") {
		t.Fatalf("scoped uninstall: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, bashrc)); !strings.Contains(b, bashMarked) || strings.Contains(b, "jog shell-hook") {
		t.Errorf("scoped uninstall touched the alias:\n%s", b)
	}
	stdout, _, code = runJogEnv(t, home, env, "shell", "install", "--preexec", "--no-alias", "bash")
	if code != 0 || !strings.Contains(stdout, "installed — ") {
		t.Fatalf("scoped reinstall: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, bashrc)); strings.Count(b, bashMarked) != 1 || strings.Count(b, bashPreexecMarked) != 1 {
		t.Errorf("scoped reinstall duplicated a line:\n%s", b)
	}

	// Bare uninstall sweeps every rc file carrying either marker — and
	// only the marked lines go.
	stdout, _, code = runJogEnv(t, home, env, "shell", "uninstall")
	if code != 0 || !strings.Contains(stdout, "removed the alias") {
		t.Fatalf("uninstall: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, bashrc)); b != "export FOO=1\n" {
		t.Errorf(".bashrc not restored: %q", b)
	}
	for _, rc := range []string{loginRC, fishrc} {
		if b, err := os.ReadFile(rc); err == nil &&
			(strings.Contains(string(b), "jog git") || strings.Contains(string(b), "jog shell-hook")) {
			t.Errorf("%s still has jog lines:\n%s", rc, b)
		}
	}

	// PowerShell by name with --preexec: the alias wires; the preexec
	// row reports not supported and the exit code stays 0.
	stdout, _, code = runJogEnv(t, home, env, "shell", "install", "--preexec", "powershell")
	if code != 0 || !strings.Contains(stdout, "not supported") || !strings.Contains(stdout, "installed") {
		t.Errorf("powershell install: code=%d\n%s", code, stdout)
	}
	// …and its list preexec row says the same, now that its profile exists.
	stdout, _, code = runJogEnv(t, home, env, "shell", "list")
	if code != 0 || !strings.Contains(stdout, "not supported") {
		t.Errorf("list missing powershell's not-supported row: code=%d\n%s", code, stdout)
	}

	// A hand-written alias is recognized and never touched — by install
	// or uninstall. (The `alias` spelling stands in for any line
	// invoking `jog git` — detection is the same on every shell.)
	hand := "alias git='jog git'\n"
	if err := os.WriteFile(loginRC, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, home, env, "shell", "install")
	if code != 0 || !strings.Contains(stdout, "by hand") {
		t.Errorf("hand-added install: code=%d\n%s", code, stdout)
	}
	stdout, _, code = runJogEnv(t, home, env, "shell", "uninstall", "--no-preexec", loginName)
	if code != 0 || !strings.Contains(stdout, "not added by jog") {
		t.Errorf("hand-added uninstall: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, loginRC)); b != hand {
		t.Errorf("hand-written rc modified: %q", b)
	}

	// Same recognition for a hand-wired preexec hook — any unmarked line
	// invoking `jog shell-hook`.
	handHook := "precmd() { jog shell-hook -- \"$1\"; }\n"
	if err := os.WriteFile(bashrc, []byte(handHook), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runJogEnv(t, home, env, "shell", "install", "--preexec", "--no-alias", "bash")
	if code != 0 || !strings.Contains(stdout, "wired by hand") {
		t.Errorf("hand-wired install: code=%d\n%s", code, stdout)
	}
	stdout, _, code = runJogEnv(t, home, env, "shell", "uninstall", "--no-alias", "bash")
	if code != 0 || !strings.Contains(stdout, "not added by jog") {
		t.Errorf("hand-wired uninstall: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, bashrc)); b != handHook {
		t.Errorf("hand-wired rc modified: %q", b)
	}

	// A bare command group prints its help; unknown shells and an
	// unreadable $SHELL are usage errors.
	if stdout, _, code := runJogEnv(t, home, env, "shell"); code != 0 || !strings.Contains(stdout, "commands:") {
		t.Errorf("bare shell: code=%d %q", code, stdout)
	}
	if _, stderr, code := runJogEnv(t, home, env, "shell", "install", "tcsh"); code != 2 || !strings.Contains(stderr, "unknown shell") {
		t.Errorf("unknown shell: code=%d %q", code, stderr)
	}
	if runtime.GOOS != "windows" { // windows has no $SHELL; the login shell is always powershell
		badEnv := append(fakeHome(home), "SHELL=/bin/tcsh")
		if _, stderr, code := runJogEnv(t, home, badEnv, "shell", "install"); code != 2 || !strings.Contains(stderr, "cannot tell your shell") {
			t.Errorf("unknown $SHELL: code=%d %q", code, stderr)
		}
	}
}

// TestShellHook covers `jog shell-hook`, the preexec runtime entry:
// silent exit-0 snapshots causally before the typed command, the
// --history index strip, jog-prefixed skips, and the `--`/help
// interplay. The harness's CI=1 keeps the maintenance spawns inert.
func TestShellHook(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "one\n")
	tr.Commit("base")
	tr.Write("b.txt", "fresh\n") // a clean tree would no-op the snapshot

	stdout, stderr, code := runJog(t, tr.Dir, "shell-hook", "--", "rm -rf build")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("shell-hook: code=%d stdout=%q stderr=%q — must be silent", code, stdout, stderr)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: rm -rf build" {
		t.Errorf("subject = %q", got)
	}

	// --history strips bash's leading entry number (the * marks a
	// modified history entry).
	tr.Write("b.txt", "fresh2\n")
	stdout, stderr, code = runJog(t, tr.Dir, "shell-hook", "--history", "--", "  42* rm -rf build")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("--history: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: rm -rf build" {
		t.Errorf("--history subject = %q", got)
	}

	// jog commands snapshot themselves — the hook skips them.
	tr.Write("b.txt", "fresh3\n")
	tip := tr.Git("rev-parse", "refs/jog/main")
	if _, _, code := runJog(t, tr.Dir, "shell-hook", "--", "jog log"); code != 0 {
		t.Errorf("jog-prefixed: code=%d", code)
	}
	if got := tr.Git("rev-parse", "refs/jog/main"); got != tip {
		t.Errorf("a `jog …` cmdline must not snapshot")
	}

	// An empty cmdline still snapshots, labeled honestly.
	stdout, stderr, code = runJog(t, tr.Dir, "shell-hook", "--", "")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("empty cmdline: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: shell command" {
		t.Errorf("empty-cmdline subject = %q", got)
	}

	// A typed command literally equal to --help arrives after `--`: it is
	// snapshotted, never answered with a help page.
	tr.Write("b.txt", "fresh4\n")
	stdout, stderr, code = runJog(t, tr.Dir, "shell-hook", "--", "--help")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("--help payload: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := tr.Git("log", "-1", "--format=%s", "refs/jog/main"); got != "pre: --help" {
		t.Errorf("--help payload subject = %q", got)
	}

	// Outside a repo: silent no-op, still exit 0.
	loose := t.TempDir()
	stdout, stderr, code = runJog(t, loose, "shell-hook", "--", "rm x")
	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("outside repo: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// Bare misuse by a human: exit 0 with a stderr pointer.
	stdout, stderr, code = runJog(t, loose, "shell-hook")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "jog shell install") {
		t.Errorf("bare shell-hook: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// …but --help with no `--` is a real help request.
	stdout, _, code = runJog(t, loose, "shell-hook", "--help")
	if code != 0 || !strings.Contains(stdout, "usage:") {
		t.Errorf("shell-hook --help: code=%d\n%s", code, stdout)
	}
}

// TestInstallInteractive covers `jog install`: piped answers drive the
// wizard, EOF stops it without losing completed steps, and --yes takes
// every default silently.
func TestInstallInteractive(t *testing.T) {
	newHome := func() (string, []string) {
		home := t.TempDir()
		// ~/.claude and the vim runtime root make exactly one agent and
		// one editor detectable (PATH carries only git, so LookPath finds
		// nothing).
		for _, d := range []string{filepath.Join(home, ".claude"), filepath.Dir(filepath.Dir(vimPluginPath(home)))} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return home, append(fakeHome(home), "SHELL=/bin/zsh")
	}

	// All yes: alias, preexec (where the login shell has one), claude, vim.
	preexecLine := loginPreexecLine()
	allYes, allNo := "y\ny\ny\ny\n", "n\nn\nn\nn\n" // an unread trailing answer is harmless
	if preexecLine == "" {
		allYes = "y\ny\ny\n" // powershell login: no preexec question
	}
	home, env := newHome()
	loginName, loginRC, loginDisplay, markedLine := loginShellFixture(home)
	stdout, _, code := runJogEnvStdin(t, home, env, allYes, "install")
	if code != 0 {
		t.Fatalf("install: code=%d\n%s", code, stdout)
	}
	loginLine := strings.SplitN(markedLine, " # jog", 2)[0]
	wants := []string{"add `" + loginLine + "` to " + loginDisplay + "?", "detected agents (claude)", "vim detected"}
	if preexecLine != "" {
		wants = append(wants, "also snapshot before every command, not just git?")
	}
	// The summary lists exactly what landed, after every question — no
	// installer output between questions.
	wants = append(wants, "installed:", "✓ "+loginName, "✓ claude", "✓ vim", "~/.claude/settings.json")
	for _, want := range wants {
		if !strings.Contains(stdout, want) {
			t.Errorf("install output missing %q:\n%s", want, stdout)
		}
	}
	if q, s := strings.Index(stdout, "vim detected"), strings.Index(stdout, "installed:"); s < q {
		t.Errorf("summary printed before the last question:\n%s", stdout)
	}
	b := string(mustRead(t, loginRC))
	if !strings.Contains(b, "jog git") {
		t.Errorf("login rc missing alias:\n%s", b)
	}
	if preexecLine != "" && !strings.Contains(b, preexecLine) {
		t.Errorf("login rc missing the preexec line:\n%s", b)
	}
	if b := string(mustRead(t, filepath.Join(home, ".claude", "settings.json"))); !strings.Contains(b, hookNeedle("claude")) {
		t.Errorf("claude settings not wired:\n%s", b)
	}
	if _, err := os.Stat(vimPluginPath(home)); err != nil {
		t.Errorf("vim hook not installed: %v", err)
	}

	// All no: nothing changes.
	home2, env2 := newHome()
	_, loginRC2, _, _ := loginShellFixture(home2)
	stdout, _, code = runJogEnvStdin(t, home2, env2, allNo, "install")
	if code != 0 {
		t.Fatalf("all-no install: code=%d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "nothing installed") || strings.Contains(stdout, "installed:") {
		t.Errorf("all-no install should summarize as nothing installed:\n%s", stdout)
	}
	for _, p := range []string{loginRC2, filepath.Join(home2, ".claude", "settings.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("all-no install created %s", p)
		}
	}

	// EOF after the first answer: the alias stays, the rest is skipped.
	home3, env3 := newHome()
	_, loginRC3, _, _ := loginShellFixture(home3)
	stdout, _, code = runJogEnvStdin(t, home3, env3, "y\n", "install")
	if code != 0 || !strings.Contains(stdout, "stopped early") {
		t.Fatalf("EOF install: code=%d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "✓ "+loginName) || strings.Contains(stdout, "✓ claude") {
		t.Errorf("EOF install summary should carry the alias row and nothing more:\n%s", stdout)
	}
	if _, err := os.Stat(loginRC3); err != nil {
		t.Errorf("EOF install lost the completed alias step: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home3, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("EOF install still wired agents")
	}

	// Alias yes, preexec no: only the alias line lands.
	if preexecLine != "" {
		home5, env5 := newHome()
		_, loginRC5, _, markedLine5 := loginShellFixture(home5)
		stdout, _, code = runJogEnvStdin(t, home5, env5, "y\nn\nn\nn\n", "install")
		if code != 0 {
			t.Fatalf("alias-only install: code=%d\n%s", code, stdout)
		}
		b := string(mustRead(t, loginRC5))
		if !strings.Contains(b, markedLine5) || strings.Contains(b, "jog shell-hook") {
			t.Errorf("alias yes + preexec no wrote the wrong lines:\n%s", b)
		}
	}

	// --yes: no stdin at all, every default taken — the alias lands, the
	// preexec hook does NOT: it defaults to no, wired only on an
	// explicit yes.
	home4, env4 := newHome()
	_, loginRC4, _, markedLine4 := loginShellFixture(home4)
	stdout, _, code = runJogEnvStdin(t, home4, env4, "", "install", "--yes")
	if code != 0 {
		t.Fatalf("install --yes: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, loginRC4)); !strings.Contains(b, markedLine4) ||
		strings.Contains(b, "jog shell-hook") {
		t.Errorf("--yes must wire the alias and only the alias:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(home4, ".claude", "settings.json")); err != nil {
		t.Errorf("--yes skipped agents: %v", err)
	}
	if _, err := os.Stat(vimPluginPath(home4)); err != nil {
		t.Errorf("--yes skipped vim: %v", err)
	}

	// Per-question flags pre-answer without asking: --yes --preexec
	// wires the hook that --yes alone leaves out, --no-agents drops the
	// agents question entirely, and --yes still covers the rest.
	home6, env6 := newHome()
	_, loginRC6, _, _ := loginShellFixture(home6)
	stdout, _, code = runJogEnvStdin(t, home6, env6, "", "install", "--yes", "--preexec", "--no-agents")
	if code != 0 {
		t.Fatalf("install --yes --preexec --no-agents: code=%d\n%s", code, stdout)
	}
	if preexecLine != "" {
		if b := string(mustRead(t, loginRC6)); !strings.Contains(b, preexecLine) {
			t.Errorf("--preexec flag did not wire the hook:\n%s", b)
		}
	}
	if _, err := os.Stat(filepath.Join(home6, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("--no-agents still wired agents")
	}
	if _, err := os.Stat(vimPluginPath(home6)); err != nil {
		t.Errorf("--yes should still cover editors: %v", err)
	}

	// A flagged question is skipped even interactively — the piped
	// answers only reach the questions still open.
	home7, env7 := newHome()
	answers := "y\nn\ny\n" // alias, preexec, vim
	if preexecLine == "" {
		answers = "y\ny\n"
	}
	stdout, _, code = runJogEnvStdin(t, home7, env7, answers, "install", "--no-agents")
	if code != 0 || strings.Contains(stdout, "detected agents") {
		t.Fatalf("--no-agents still asked about agents: code=%d\n%s", code, stdout)
	}
	if _, err := os.Stat(vimPluginPath(home7)); err != nil {
		t.Errorf("vim answer lost under --no-agents: %v", err)
	}

	// Named --agents scopes the plan and beats detection, like the
	// standalone `jog agents install <name>` — codex is not detected in
	// this home, claude is, and only codex may land.
	home8, env8 := newHome()
	stdout, _, code = runJogEnvStdin(t, home8, env8, "", "install", "--yes", "--agents", "codex", "--no-editors")
	if code != 0 {
		t.Fatalf("install --agents codex: code=%d\n%s", code, stdout)
	}
	if b := string(mustRead(t, filepath.Join(home8, ".codex", "hooks.json"))); !strings.Contains(b, hookNeedle("codex")) {
		t.Errorf("named undetected agent not wired:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(home8, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("--agents codex wired claude too")
	}

	// Unknown names fail before anything runs.
	if _, stderr, code := runJogEnv(t, home, env, "install", "--agents", "nope"); code != 2 || !strings.Contains(stderr, "unknown agent client") {
		t.Errorf("install --agents nope: code=%d %q", code, stderr)
	}
	if _, stderr, code := runJogEnv(t, home, env, "install", "--editors=nope"); code != 2 || !strings.Contains(stderr, "unknown editor") {
		t.Errorf("install --editors=nope: code=%d %q", code, stderr)
	}

	// Contradictory flags are a usage error.
	if _, stderr, code := runJogEnv(t, home, env, "install", "--alias", "--no-alias"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("install --alias --no-alias: code=%d %q", code, stderr)
	}

	// Unknown flag.
	if _, stderr, code := runJogEnv(t, home, env, "install", "--nope"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("install --nope: code=%d %q", code, stderr)
	}
}

// TestUninstallCommand covers `jog uninstall`: the wired summary, the
// single confirmation, and the sweep across all three surfaces.
func TestUninstallCommand(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{filepath.Join(home, ".claude"), filepath.Dir(filepath.Dir(vimPluginPath(home)))} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(fakeHome(home), "SHELL=/bin/zsh")
	loginName, loginRC, _, _ := loginShellFixture(home)
	if stdout, _, code := runJogEnvStdin(t, home, env, "", "install", "--yes"); code != 0 {
		t.Fatalf("seed install: code=%d\n%s", code, stdout)
	}
	// --yes stays alias-only, so opt the preexec hook in separately —
	// uninstall must sweep it all the same.
	if stdout, _, code := runJogEnv(t, home, env, "shell", "install", "--preexec", "--no-alias"); code != 0 {
		t.Fatalf("seed preexec: code=%d\n%s", code, stdout)
	}

	// Declined: everything stays. The summary names both shell surfaces
	// (the --yes seed wired the preexec hook wherever the shell has one).
	stdout, _, code := runJogEnvStdin(t, home, env, "n\n", "uninstall")
	if code != 0 || !strings.Contains(stdout, "currently wired:") || !strings.Contains(stdout, "nothing removed") {
		t.Fatalf("declined uninstall: code=%d\n%s", code, stdout)
	}
	if loginPreexecLine() != "" && !strings.Contains(stdout, "preexec") {
		t.Errorf("summary missing the preexec row:\n%s", stdout)
	}
	if _, err := os.Stat(vimPluginPath(home)); err != nil {
		t.Errorf("declined uninstall removed the vim hook: %v", err)
	}

	// Confirmed via --yes: alias, agent wiring, editor hook all gone,
	// and the summary lists each removed surface — install's format.
	stdout, _, code = runJogEnvStdin(t, home, env, "", "uninstall", "--yes")
	if code != 0 || !strings.Contains(stdout, "snapshots are untouched") {
		t.Fatalf("uninstall --yes: code=%d\n%s", code, stdout)
	}
	for _, want := range []string{"removed:", "✓ " + loginName, "✓ claude", "✓ vim"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("uninstall summary missing %q:\n%s", want, stdout)
		}
	}
	if b := string(mustRead(t, loginRC)); strings.Contains(b, "jog git") || strings.Contains(b, "jog shell-hook") {
		t.Errorf("login rc still carries jog lines:\n%s", b)
	}
	if b := string(mustRead(t, filepath.Join(home, ".claude", "settings.json"))); strings.Contains(b, "jog hook") {
		t.Errorf("claude settings still wired:\n%s", b)
	}
	if _, err := os.Stat(vimPluginPath(home)); !os.IsNotExist(err) {
		t.Errorf("vim hook survived uninstall")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "jog", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("claude skill survived uninstall")
	}

	// Nothing wired: says so, exits 0.
	stdout, _, code = runJogEnvStdin(t, home, env, "", "uninstall", "--yes")
	if code != 0 || !strings.Contains(stdout, "nothing is wired") {
		t.Errorf("empty uninstall: code=%d\n%s", code, stdout)
	}
}

// TestJogGitEnv covers $JOG_GIT: it names the git binary jog runs, and
// a bad value fails loudly with the variable named.
func TestJogGitEnv(t *testing.T) {
	dir := t.TempDir()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, code := runJogEnv(t, dir, []string{"JOG_GIT=" + realGit}, "git", "version")
	if code != 0 || !strings.Contains(stdout, "git version") {
		t.Errorf("explicit path: code=%d\n%s", code, stdout)
	}

	_, stderr, code := runJogEnv(t, dir, []string{"JOG_GIT=" + filepath.Join(dir, "no-such-git")}, "git", "version")
	if code != 127 || !strings.Contains(stderr, "JOG_GIT") {
		t.Errorf("bad path: code=%d, want 127 naming JOG_GIT\n%s", code, stderr)
	}
}

// TestHelpIndexCoverage pins the help pages to their indexes: every
// command the root page lists has an embedded page (version and help are
// answered by the binary itself), and every group-index row has a nested
// page. A new command added to an index without a page fails here.
func TestHelpIndexCoverage(t *testing.T) {
	exempt := map[string]bool{"version": true, "help": true}
	for _, group := range []string{"jog", "agents", "editors", "shell"} {
		page, ok := helpTexts[group]
		if !ok {
			t.Fatalf("no page for %q", group)
		}
		_, rest, found := strings.Cut(page, "commands:\n")
		if !found {
			t.Fatalf("%s page has no commands index", group)
		}
		for _, line := range strings.Split(rest, "\n") {
			if strings.TrimSpace(line) == "" {
				break
			}
			name := strings.Fields(line)[0]
			key := name
			if group != "jog" {
				key = group + " " + name
			}
			if _, ok := helpTexts[key]; !ok && !exempt[name] {
				t.Errorf("%s index lists %q but there is no help/%s.txt", group, name, strings.ReplaceAll(key, " ", "_"))
			}
		}
	}
}
