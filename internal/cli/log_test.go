package cli

import (
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

// TestSnapPreview covers matrix row 31's preview layer: the browser's
// preview renders a snapshot's parent-1 change, path-scoped. (The row data
// layer — path filtering, -n, bases, event rows — is TestBuildTimeline*.)
func TestSnapPreview(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("f.txt", "v1\n")
	tr.Commit("base")
	repo, err := gitx.Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	tr.Write("f.txt", "v2\n")
	if _, err := snap.Take(repo, provenance.Manual("second version")); err != nil {
		t.Fatal(err)
	}
	tr.Write("f.txt", "v3\n")
	res, err := snap.Take(repo, provenance.Manual("third version"))
	if err != nil {
		t.Fatal(err)
	}

	p := stripANSI(snapPreview(repo, res.Commit, []string{"f.txt"}))
	if !strings.Contains(p, "-v2") || !strings.Contains(p, "+v3") {
		t.Errorf("preview of %s missing the change:\n%s", res.Commit[:7], p)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
