package testrepo

import (
	"bytes"
	"testing"
)

func TestFixture(t *testing.T) {
	r := New(t)

	if _, err := r.TryGit("rev-parse", "--verify", "-q", "HEAD"); err == nil {
		t.Fatal("fresh repo should have an unborn HEAD")
	}

	r.Write("a.txt", "hello\n")
	sha := r.Commit("first")
	if got := r.Git("rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD = %s, want %s", got, sha)
	}
	if got := r.Git("symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}

	idx := r.IndexBytes()
	if len(idx) == 0 {
		t.Fatal("index should exist after a commit")
	}
	// Read-only git commands must not disturb the index bytes when run with
	// --no-optional-locks — the assertion style the whole matrix relies on.
	r.Git("--no-optional-locks", "status", "--porcelain")
	if !bytes.Equal(idx, r.IndexBytes()) {
		t.Fatal("index bytes changed under a no-optional-locks read")
	}
}
