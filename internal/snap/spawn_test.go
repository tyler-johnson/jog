package snap

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

// TestSpawnBudget makes the M8 spawn budget executable: every scenario's
// git-spawn count is asserted exactly, so a regression (or an improvement)
// shows up as a failing diff against this table instead of a slow hook in
// the field. Counts cover Take only; the full hook path adds one Discover
// spawn per process.
//
// The counter is a $JOG_GIT shim that logs each invocation and execs the
// real git — jog's own layer is untouched, and the fixture's git calls
// (testrepo) bypass $JOG_GIT entirely, so only engine spawns are counted.
func TestSpawnBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn counting uses a shell-script git shim; the budget itself is platform-independent")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "spawns.log")
	shim := filepath.Join(dir, "git-shim")
	// One line per invocation no matter what the args contain (commit
	// messages may embed newlines).
	script := "#!/bin/sh\n" +
		"{ printf '%s ' \"$@\" | tr '\\n' ' '; echo; } >> '" + logPath + "'\n" +
		"exec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	tr, r := setup(t)
	t.Setenv("JOG_GIT", shim) // after setup: Discover stays uncounted

	count := func(scenario string, want int, f func()) {
		t.Helper()
		os.Remove(logPath)
		f()
		var lines []string
		if b, err := os.ReadFile(logPath); err == nil {
			for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
				if l != "" {
					lines = append(lines, l)
				}
			}
		}
		if len(lines) != want {
			t.Errorf("%s: %d spawns, want %d:\n  %s",
				scenario, len(lines), want, strings.Join(lines, "\n  "))
		}
	}

	tr.Write("a.txt", "committed\n")
	tr.Commit("first")
	tr.Write("b.txt", "wip\n")

	// First snapshot, cold shadow: state 1 + config 1 + status 1 +
	// read-tree seed 1 + add 1 + write-tree 1 + commit-tree 1 +
	// update-ref 1 + gc-config get/set/set 3.
	count("first snapshot (cold shadow)", 11, func() {
		if res := take(t, r, "manual: first"); res.Commit == "" {
			t.Fatalf("expected a snapshot, got %+v", res)
		}
	})

	// The hook hot path (repeated tool calls mid-edit), M8 headline number:
	// state 1 + config 1 + status 1 + add 1 + write-tree 1.
	count("dirty-but-unchanged no-op (warm shadow)", 5, func() {
		if res := take(t, r, "manual: unchanged"); !res.NoOp {
			t.Fatalf("expected NoOp, got %+v", res)
		}
	})

	// A real change, warm shadow: the no-op path + commit-tree + update-ref.
	count("changed tree (warm shadow)", 7, func() {
		tr.Write("b.txt", "wip 2\n")
		if res := take(t, r, "manual: changed"); res.Commit == "" {
			t.Fatalf("expected a snapshot, got %+v", res)
		}
	})

	// Clean tree, M8's other headline: state 1 + config 1 + status 1 —
	// the empty status doubles as proof the tree is HEAD's, no shadow work.
	tr.Commit("absorb the wip") // worktree now == HEAD == last snapshot's tree
	count("clean no-op", 3, func() {
		if res := take(t, r, "manual: clean"); !res.NoOp {
			t.Fatalf("expected NoOp, got %+v", res)
		}
	})

	// Unborn HEAD, first snapshot: the batched rev-parse dies at HEAD and
	// the chain is queried alone (2), then config 1 + status 1 +
	// read-tree --empty 1 + add 1 + write-tree 1 + commit-tree 1 +
	// update-ref 1 + gc-config 3.
	tu := testrepo.New(t)
	ru, err := gitx.Discover(tu.Dir)
	if err != nil {
		t.Fatal(err)
	}
	tu.Write("a.txt", "before first commit\n")
	count("unborn HEAD, first snapshot", 12, func() {
		if res := take(t, ru, "manual: unborn"); res.Commit == "" {
			t.Fatalf("expected a snapshot, got %+v", res)
		}
	})
}
