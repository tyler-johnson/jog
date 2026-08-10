package cli

import (
	"testing"
	"time"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1536, "1.5 KiB"},
		{3670016, "3.5 MiB"},
		{1288490189, "1.2 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestDiskUsage: both measurements count only what jog's refs pin beyond
// real history — content unique to a snapshot registers, content the repo
// already commits costs zero.
func TestDiskUsage(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("a.txt", "real\n")
	tr.Commit("base")
	head := tr.Git("rev-parse", "HEAD")
	repo, err := gitx.Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	// Before any snapshot exists, jog costs nothing.
	if n, err := jogDiskUsage(repo); err != nil || n != 0 {
		t.Fatalf("empty jogDiskUsage = %d, %v; want 0", n, err)
	}

	// One snapshot holding ~64 KiB of content real history never saw.
	var big []byte
	for i := 0; i < 64<<10; i++ {
		big = append(big, byte(i*2654435761>>16)) // varied enough to resist zlib
	}
	tr.Write("scratch.bin", string(big))
	idx := tr.GitDir + "/size-test-index"
	env := []string{"GIT_INDEX_FILE=" + idx}
	tr.GitEnv(env, "add", "-A")
	tree := tr.GitEnv(env, "write-tree")
	d := time.Now().Format(time.RFC3339)
	ident := []string{
		"GIT_AUTHOR_NAME=jog", "GIT_AUTHOR_EMAIL=jog@local",
		"GIT_COMMITTER_NAME=jog", "GIT_COMMITTER_EMAIL=jog@local",
		"GIT_AUTHOR_DATE=" + d, "GIT_COMMITTER_DATE=" + d,
	}
	sha := tr.GitEnv(ident, "commit-tree", tree, "-p", head, "-m", "manual: big")
	tr.GitEnv(ident, "update-ref", "--create-reflog", "refs/jog/main", sha)

	n, err := jogDiskUsage(repo)
	if err != nil || n < 2<<10 {
		t.Fatalf("jogDiskUsage = %d, %v; want at least ~2 KiB (zlib squeezes the fixture hard)", n, err)
	}

	got, err := treesDiskUsage(repo, []string{tree, tree}) // dedup exercised
	if err != nil || got < 2<<10 {
		t.Fatalf("treesDiskUsage(snapshot) = %d, %v; want at least ~2 KiB", got, err)
	}
	if got >= n {
		t.Errorf("tree projection %d should be below the ref total %d (no commit object)", got, n)
	}

	// A tree real history already reaches is (nearly) free: rev-list always
	// emits an explicitly-listed positive, so the tree object itself counts
	// (~70 B), but every blob under it is excluded as already-reachable.
	headTree := tr.Git("rev-parse", "HEAD^{tree}")
	if got, err := treesDiskUsage(repo, []string{headTree}); err != nil || got >= 512 {
		t.Errorf("treesDiskUsage(HEAD tree) = %d, %v; want just the tree object, blobs excluded", got, err)
	}
	if got, err := treesDiskUsage(repo, nil); err != nil || got != 0 {
		t.Errorf("treesDiskUsage(nil) = %d, %v; want 0", got, err)
	}
}
