package cli

import (
	"strings"
	"testing"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

// rowShape is the compact form the assertions compare: kind + the sha the
// row is about.
func rowShapes(rows []logRow) []string {
	var out []string
	for _, r := range rows {
		k := map[rowKind]string{rowSnap: "snap", rowEvent: "event", rowAnchor: "anchor"}[r.kind]
		out = append(out, k+":"+short(r.sha))
	}
	return out
}

func wantShapes(t *testing.T, rows []logRow, want []string) {
	t.Helper()
	got := rowShapes(rows)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// The full arc: bases on every snapshot, an event row at each base flip
// labeled from the reflog, the anchor at the chain root, honest duplicate
// events across a reset zigzag, -n counting snapshots only, and the
// unlabeled fallback once the reflog is expired.
func TestBuildTimeline(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("f.txt", "v1\n")
	c1 := tr.Commit("base commit")
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
	ref := chainRef(repo)

	tr.Write("f.txt", "v2\n")
	s1 := take("first")
	tr.Write("g.txt", "one\n")
	s2 := take("second")

	// One base era: no events, root anchored to c1.
	rows, err := buildTimeline(repo, ref, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	wantShapes(t, rows, []string{"snap:" + short(s2), "snap:" + short(s1), "anchor:" + short(c1)})
	if rows[0].base != c1 || rows[1].base != c1 {
		t.Errorf("bases = %s %s, want both %s", short(rows[0].base), short(rows[1].base), short(c1))
	}
	if rows[2].text != "base commit" {
		t.Errorf("anchor subject = %q, want the commit subject", rows[2].text)
	}

	// HEAD moved after the newest snapshot: top event row, labeled "commit".
	c2 := tr.Commit("second commit")
	rows, err = buildTimeline(repo, ref, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	wantShapes(t, rows, []string{"event:" + short(c2), "snap:" + short(s2), "snap:" + short(s1), "anchor:" + short(c1)})
	if rows[0].reflogKind != "commit" || rows[0].text != "second commit" {
		t.Errorf("top event = kind %q text %q, want commit / second commit", rows[0].reflogKind, rows[0].text)
	}

	// A snapshot on the new base: the event row moves between the eras.
	tr.Write("f.txt", "v3\n")
	s3 := take("third")
	rows, err = buildTimeline(repo, ref, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	wantShapes(t, rows, []string{"snap:" + short(s3), "event:" + short(c2),
		"snap:" + short(s2), "snap:" + short(s1), "anchor:" + short(c1)})
	if rows[0].base != c2 {
		t.Errorf("newest base = %s, want %s", short(rows[0].base), short(c2))
	}

	// -n counts snapshots only; the root is off-screen, so no anchor.
	rows, err = buildTimeline(repo, ref, nil, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	wantShapes(t, rows, []string{"snap:" + short(s3), "event:" + short(c2), "snap:" + short(s2)})

	// Reset zigzag: HEAD returns to c1 — its event row is labeled "reset"
	// and c1 also remains the anchor, two honest mentions.
	tr.Git("reset", "--hard", c1)
	tr.Write("h.txt", "back\n")
	s4 := take("after reset")
	rows, err = buildTimeline(repo, ref, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	wantShapes(t, rows, []string{"snap:" + short(s4), "event:" + short(c1), "snap:" + short(s3),
		"event:" + short(c2), "snap:" + short(s2), "snap:" + short(s1), "anchor:" + short(c1)})
	if rows[1].reflogKind != "reset" {
		t.Errorf("zigzag event kind = %q, want reset", rows[1].reflogKind)
	}
	if rows[3].reflogKind != "commit" {
		t.Errorf("older event kind = %q, want commit (in-order reflog matching)", rows[3].reflogKind)
	}

	// Expired reflog: events stay, labels degrade to the commit subject.
	tr.Git("reflog", "expire", "--expire=now", "--all")
	rows, err = buildTimeline(repo, ref, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if rows[1].reflogKind != "" || rows[1].text != "base commit" {
		t.Errorf("expired reflog: kind %q text %q, want unlabeled with subject", rows[1].reflogKind, rows[1].text)
	}
}

// Path filtering: rows are the snapshots that touched the path; flips are
// computed between displayed neighbors only, and file blocks are scoped to
// the pathspec.
func TestBuildTimelinePaths(t *testing.T) {
	tr := testrepo.New(t)
	tr.Write("f.txt", "v1\n")
	tr.Commit("base commit")
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
	ref := chainRef(repo)

	tr.Write("f.txt", "v2\n")
	s1 := take("touches f")
	tr.Write("g.txt", "one\n")
	take("touches g only")
	c2 := tr.Commit("second commit")
	tr.Write("f.txt", "v3\n")
	s3 := take("touches f again")

	rows, err := buildTimeline(repo, ref, []string{"f.txt"}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	// s3 (base c2) and s1 (base c1) are neighbors in the filtered view; the
	// flip between them still renders. The root snapshot s1 is displayed, so
	// the anchor row survives the filter.
	shapes := rowShapes(rows)
	want := []string{"snap:" + short(s3), "event:" + short(c2), "snap:" + short(s1)}
	if len(shapes) < 3 || strings.Join(shapes[:3], " ") != strings.Join(want, " ") {
		t.Errorf("rows = %v, want prefix %v", shapes, want)
	}
	for _, r := range rows {
		if r.kind == rowSnap && !strings.Contains(r.files, "f.txt") {
			t.Errorf("snapshot %s files = %q, want f.txt entry", short(r.sha), r.files)
		}
		if r.kind == rowSnap && strings.Contains(r.files, "g.txt") {
			t.Errorf("snapshot %s files = %q, leaked past the pathspec", short(r.sha), r.files)
		}
	}
}

// Unborn-HEAD chain: no bases, no events, no anchor — just snapshots with
// blank base cells.
func TestBuildTimelineUnborn(t *testing.T) {
	tr := testrepo.New(t)
	repo, err := gitx.Discover(tr.Dir)
	if err != nil {
		t.Fatal(err)
	}
	tr.Write("f.txt", "v1\n")
	res, err := snap.Take(repo, provenance.Manual("first ever"))
	if err != nil {
		t.Fatal(err)
	}
	tr.Write("f.txt", "v2\n")
	res2, err := snap.Take(repo, provenance.Manual("second"))
	if err != nil {
		t.Fatal(err)
	}

	rows, err := buildTimeline(repo, chainRef(repo), nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	wantShapes(t, rows, []string{"snap:" + short(res2.Commit), "snap:" + short(res.Commit)})
	for _, r := range rows {
		if r.base != "" {
			t.Errorf("unborn snapshot %s base = %q, want empty", short(r.sha), r.base)
		}
	}
	if !strings.Contains(rows[0].snapLabel(false), strings.Repeat(" ", 7)) {
		t.Errorf("blank base cell missing: %q", rows[0].snapLabel(false))
	}
}
