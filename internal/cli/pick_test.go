package cli

import (
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

// TestFileVersions covers matrix row 31's data layer: versions are exactly
// the chain snapshots that changed the path, newest first, and previews
// render the change (a chain-root version shows as an addition).
func TestFileVersions(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("f.txt", "v1\n")
	tr.Commit("base")
	repo, err := gitx.Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}

	take := func(msg string) string {
		res, err := snap.Take(repo, provenance.Manual(msg))
		if err != nil {
			t.Fatal(err)
		}
		return res.Commit
	}
	tr.Write("f.txt", "v2\n")
	s1 := take("second version")
	tr.Write("g.txt", "unrelated\n")
	take("does not touch f")
	tr.Write("f.txt", "v3\n")
	s3 := take("third version")

	vs, err := fileVersions(repo, "f.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].SHA != s3 || vs[1].SHA != s1 {
		t.Fatalf("fileVersions: want [%s %s], got %+v", s3[:7], s1[:7], vs)
	}

	p := stripANSI(versionPreview(repo, s3, "f.txt"))
	if !strings.Contains(p, "-v2") || !strings.Contains(p, "+v3") {
		t.Errorf("preview of %s missing the change:\n%s", s3[:7], p)
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
