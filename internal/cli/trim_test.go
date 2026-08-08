package cli

import (
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/retain"
	"github.com/tyler-johnson/jog/internal/testrepo"
	"time"
)

// TestTrimContention covers matrix row 28: a chain that moves between
// planning and applying (a concurrent snapshot won the race) is skipped
// untouched — v0's contention policy, no lost snapshot, no partial rewrite.
func TestTrimContention(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("base")
	head := tr.Git("rev-parse", "HEAD")
	ref := "refs/jog/main"

	mint := func(content string, age time.Duration) string {
		tr.Write("w.txt", content)
		idx := tr.GitDir + "/contention-index"
		env := []string{"GIT_INDEX_FILE=" + idx}
		tr.GitEnv(env, "add", "-A")
		tree := tr.GitEnv(env, "write-tree")
		d := time.Now().Add(-age).Format(time.RFC3339)
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
		args = append(args, "-p", head, "-m", "manual: "+content)
		sha := tr.GitEnv(ident, args...)
		if perr != nil {
			prev = ""
		}
		tr.GitEnv(ident, "update-ref", "--create-reflog", ref, sha, prev)
		return sha
	}

	mint("old", 95*24*time.Hour)
	mint("mid", 10*24*time.Hour)
	mint("tip", time.Hour)

	repo, err := gitx.Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := listChain(repo, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("listChain: want 3 entries, got %d", len(entries))
	}
	keep := planTrim(retain.Default, time.Now(), entries)
	if keep[len(keep)-1] {
		t.Fatalf("expected the 95d snapshot to be planned for dropping: %v", keep)
	}

	// The race: a concurrent snapshot moves the tip after planning.
	winner := mint("concurrent", time.Minute)
	reflogBefore := tr.Git("reflog", "show", ref)

	if err := applyTrim(repo, ref, entries, keep); err == nil ||
		!strings.Contains(err.Error(), "moved while trimming") {
		t.Fatalf("want contention skip, got %v", err)
	}
	if got := tr.Git("rev-parse", ref); got != winner {
		t.Errorf("chain clobbered: tip %s, want the concurrent winner %s", got[:7], winner[:7])
	}
	if got := tr.Git("reflog", "show", ref); got != reflogBefore {
		t.Errorf("reflog touched by a skipped trim")
	}
	if _, err := tr.TryGit("rev-parse", "-q", "--verify", "refs/jog/@trash/main"); err == nil {
		t.Errorf("insurance ref written by a skipped trim")
	}
}
