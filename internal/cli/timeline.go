package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/snap"
)

// This file is the single-chain timeline's data layer, shared by the log
// browser, the piped default output, and the bare-`jog` readout. It projects
// both facts every snapshot records — parent 1 (the previous snapshot: time
// order) and parent 2 (the HEAD commit at snapshot time: the base edge) —
// into one row list: snapshot rows carry their base, and wherever the base
// changes between neighbors, HEAD moved there, so an event row marking the
// commit is interleaved.

type rowKind int

const (
	rowSnap   rowKind = iota // a snapshot on the chain (selectable)
	rowEvent                 // HEAD moved here between the neighboring snapshots
	rowAnchor                // the commit the chain grew from (below the oldest snapshot)
)

// logRow is one rendered timeline entry.
type logRow struct {
	kind       rowKind
	sha        string // snapshot sha (rowSnap) or the base commit sha (rowEvent/rowAnchor)
	base       string // full base-edge sha; "" when none (unborn-era snapshot)
	age        string // %cr of the snapshot or base commit
	text       string // provenance subject (rowSnap) / base commit subject (rowEvent/rowAnchor)
	body       string // %b — carries the maxFileSize skipped-list (rowSnap)
	reflogKind string // what the reflog called the move ("commit", "reset", …); "" = unlabeled
	files      string // raw --name-status block (rowSnap, when requested)
}

// short is the display form of a sha (git's default abbreviation width).
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// snapLabel renders a snapshot row: id, dim base column (blank-padded when
// the snapshot has none), age, provenance. styled=false yields byte-plain
// text — the TUI needs it because its cursor row is wrapped in raw reverse
// video that an embedded ANSI reset would terminate mid-row.
func (r logRow) snapLabel(styled bool) string {
	base := strings.Repeat(" ", 7)
	if r.base != "" {
		base = short(r.base)
		if styled {
			base = styleDim.Render(base)
		}
	}
	id := short(r.sha)
	if styled {
		id = styleID.Render(id)
	}
	return fmt.Sprintf("%s  %s  %s  %s", id, base, r.age, r.text)
}

// markerLabel renders an event or anchor row: `● <sha>  <kind>: <subject>
// — <age>`, the kind present only when the reflog labeled the move. Dim as
// a whole so the commit reads as context between snapshots, with the sha
// left bright enough to cross-reference `git log`. lipgloss drops the
// styling off a TTY, so the piped renderer reuses this verbatim.
func (r logRow) markerLabel() string {
	s := "● " + short(r.sha) + "  "
	if r.reflogKind != "" {
		s += r.reflogKind + ": "
	}
	s += r.text
	if r.age != "" {
		s += " — " + r.age
	}
	return styleDim.Render(s)
}

// walkRec is one record off the chain walk.
type walkRec struct {
	sha     string
	parents []string
	age     string
	subject string
	body    string
	files   string
}

// buildTimeline returns the chain's rows newest-first: snapshot rows (path-
// filtered and limited when asked — limit counts snapshots only, event and
// anchor rows ride along free, matching what -n means under --json and
// --format), event rows at every base-edge change between displayed
// neighbors, a top event when HEAD moved after the newest snapshot, and the
// anchor row when the displayed rows reach the chain's root. withFiles
// attaches each snapshot's --name-status block for the piped renderer.
//
// Spawns: the streamed chain walk, plus — only when needed — one path-
// membership walk, one batched subject lookup, and one reflog read.
func buildTimeline(repo *gitx.Repo, ref string, paths []string, limit int, withFiles bool) ([]logRow, error) {
	// With paths the walk must run unfiltered end-to-end (flip detection and
	// base classification need the chain's true shape), so the limit can only
	// kill the walk early on the no-paths fast path. The +1 is a peek: it
	// tells root from unborn-era for the last displayed snapshot without
	// reaching the boundary.
	cutoff := 0
	if len(paths) == 0 && limit > 0 {
		cutoff = limit + 1
	}
	filesInWalk := withFiles && len(paths) == 0
	recs, anchor, err := walkChain(repo, ref, cutoff, filesInWalk)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}

	// Path membership (and path-scoped file blocks): a second bounded walk
	// with the pathspec. The boundary bound keeps it off real history.
	var member map[string]string
	if len(paths) > 0 {
		rng := ref
		if anchor != nil {
			rng = anchor.sha + ".." + ref
		}
		if member, err = pathMembers(repo, rng, paths, withFiles); err != nil {
			return nil, err
		}
	}

	rootSHA := ""
	if anchor != nil {
		rootSHA = recs[len(recs)-1].sha // boundary reached: the last jog record is the chain root
	}

	// Displayed snapshot rows, newest-first.
	var snaps []logRow
	for _, rec := range recs {
		if member != nil {
			blk, ok := member[rec.sha]
			if !ok {
				continue
			}
			if withFiles {
				rec.files = blk
			}
		}
		var base string
		if rec.sha == rootSHA {
			if len(rec.parents) > 0 {
				base = rec.parents[0] // chain root: parent 1 IS the base commit
			}
		} else if len(rec.parents) > 1 {
			base = rec.parents[1]
		}
		snaps = append(snaps, logRow{kind: rowSnap, sha: rec.sha, base: base,
			age: rec.age, text: rec.subject, body: rec.body, files: rec.files})
		if limit > 0 && len(snaps) == limit {
			break
		}
	}
	if len(snaps) == 0 {
		return nil, nil
	}

	// Interleave: an event row after the last snapshot of each base run —
	// HEAD moved to base(upper) chronologically between the two neighbors.
	var rows []logRow
	if head := headCommit(repo); head != "" && head != snaps[0].base {
		rows = append(rows, logRow{kind: rowEvent, sha: head})
	}
	for i, r := range snaps {
		rows = append(rows, r)
		if i+1 < len(snaps) && r.base != snaps[i+1].base && r.base != "" {
			rows = append(rows, logRow{kind: rowEvent, sha: r.base})
		}
	}
	if anchor != nil && snaps[len(snaps)-1].sha == rootSHA {
		rows = append(rows, logRow{kind: rowAnchor, sha: anchor.sha,
			age: anchor.age, text: anchor.subject})
	}

	if err := fillEvents(repo, ref, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// walkChain streams the chain newest-first: jog records until the D1
// boundary — the first record by another committer, returned as anchor
// (it is the chain root's base commit, its subject and age captured for
// free) — or, when cutoff > 0, until cutoff records are read. Killed
// early either way: the walk below the boundary is the repo's whole
// history.
func walkChain(repo *gitx.Repo, ref string, cutoff int, withFiles bool) (recs []walkRec, anchor *walkRec, err error) {
	args := []string{"log", "--first-parent", "--format=%x1e%H%x1f%P%x1f%ce%x1f%cr%x1f%s%x1f%b%x1e"}
	if withFiles {
		args = append(args, "--name-status")
	}
	args = append(args, ref)
	cmd, out, err := repo.StartRead(args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0x1e); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF {
			return 0, nil, bufio.ErrFinalToken
		}
		return 0, nil, nil
	})

	// Tokens alternate: junk, fields, interstitial (the record's
	// --name-status block when requested), fields, interstitial, … — the
	// same fencing logJSON parses, here streamed.
	tok := 0
	for scanner.Scan() {
		t := scanner.Text()
		tok++
		if tok == 1 {
			continue // before the first fence
		}
		if tok%2 == 1 { // interstitial: files block for the latest record
			if withFiles && len(recs) > 0 {
				recs[len(recs)-1].files = strings.Trim(t, "\n")
			}
			continue
		}
		f := strings.SplitN(t, "\x1f", 6)
		if len(f) != 6 {
			return nil, nil, fmt.Errorf("unexpected log record (%d fields)", len(f))
		}
		rec := walkRec{sha: f[0], age: f[3], subject: f[4], body: strings.TrimSpace(f[5])}
		if f[1] != "" {
			rec.parents = strings.Fields(f[1])
		}
		if f[2] != snap.IdentityEmail {
			return recs, &rec, nil // boundary: real history begins here
		}
		recs = append(recs, rec)
		if cutoff > 0 && len(recs) == cutoff {
			return recs, nil, nil
		}
	}
	return recs, nil, scanner.Err()
}

// pathMembers walks the bounded chain range with the pathspec: which
// snapshots changed the paths and — when wanted — the path-scoped
// --name-status block for each.
func pathMembers(repo *gitx.Repo, rng string, paths []string, withFiles bool) (map[string]string, error) {
	args := []string{"log", "--first-parent", "--format=%x1e%H%x1e"}
	if withFiles {
		args = append(args, "--name-status")
	}
	args = append(args, rng, "--")
	args = append(args, paths...)
	out, err := repo.RunRead(args...)
	if err != nil {
		return nil, err
	}
	member := map[string]string{}
	chunks := strings.Split(out, "\x1e")
	for i := 1; i < len(chunks); i += 2 {
		blk := ""
		if i+1 < len(chunks) {
			blk = strings.Trim(chunks[i+1], "\n")
		}
		member[chunks[i]] = blk
	}
	return member, nil
}

// headCommit is the current HEAD sha — natively when the ref backend
// allows, one rev-parse otherwise; "" on unborn HEAD.
func headCommit(repo *gitx.Repo) string {
	if sha, ok := repo.HeadSHA(); ok {
		return sha
	}
	sha, err := repo.RunRead("rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err != nil {
		return ""
	}
	return sha
}

// fillEvents decorates the event rows: each commit's subject and age in one
// batched lookup (the shas always resolve — even a rebased-away base stays
// reachable through its snapshot's parent-2 edge), and what the move was
// from the branch's reflog. Reflog entries are consumed newest-first in
// step with the rows, so each leg of an A→B→A zigzag gets its own label;
// an expired or absent reflog just leaves the rows unlabeled.
func fillEvents(repo *gitx.Repo, ref string, rows []logRow) error {
	var shas []string
	seen := map[string]bool{}
	for _, r := range rows {
		if r.kind == rowEvent && !seen[r.sha] {
			seen[r.sha] = true
			shas = append(shas, r.sha)
		}
	}
	if len(shas) == 0 {
		return nil
	}

	out, err := repo.RunRead(append([]string{"log", "--no-walk=unsorted",
		"--format=%H%x1f%s%x1f%cr"}, shas...)...)
	if err != nil {
		return err
	}
	type meta struct{ subject, age string }
	commits := map[string]meta{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(line, "\x1f", 3); len(f) == 3 {
			commits[f[0]] = meta{f[1], f[2]}
		}
	}

	logref := "refs/heads/" + strings.TrimPrefix(ref, "refs/jog/")
	if logref == "refs/heads/@detached" {
		logref = "HEAD"
	}
	type rentry struct{ sha, kind string }
	var rlog []rentry
	if out, err := repo.RunRead("log", "-g", "--format=%H%x1f%gs", logref); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if sha, gs, ok := strings.Cut(line, "\x1f"); ok {
				kind, _, _ := strings.Cut(gs, ":")
				rlog = append(rlog, rentry{sha, kind})
			}
		}
	}

	ri := 0
	for i := range rows {
		if rows[i].kind != rowEvent {
			continue
		}
		m := commits[rows[i].sha]
		rows[i].text, rows[i].age = m.subject, m.age
		for j := ri; j < len(rlog); j++ {
			if rlog[j].sha == rows[i].sha {
				rows[i].reflogKind = rlog[j].kind
				ri = j + 1
				break
			}
		}
	}
	return nil
}
