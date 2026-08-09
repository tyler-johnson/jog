package cli

import (
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

// TestTimelineItems covers matrix row 31's data layer, now shared by the
// log browser: path-filtered rows are exactly the chain snapshots that
// changed the path, newest first, and previews render the change (a
// chain-root version shows as an addition).
func TestTimelineItems(t *testing.T) {
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

	_, rng, exists, err := chainRange(repo)
	if err != nil || !exists {
		t.Fatalf("chainRange: exists=%v err=%v", exists, err)
	}
	items, err := timelineItems(repo, false, []string{rng}, []string{"f.txt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != s3 || items[1].ID != s1 {
		t.Fatalf("timelineItems: want [%s %s], got %+v", s3[:7], s1[:7], items)
	}

	// -n narrows to the newest.
	items, err = timelineItems(repo, false, []string{rng}, nil, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != s3 {
		t.Fatalf("timelineItems -n 1: want [%s], got %+v", s3[:7], items)
	}

	p := stripANSI(snapPreview(repo, s3, []string{"f.txt"}))
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
