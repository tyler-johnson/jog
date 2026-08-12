package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
	"github.com/tyler-johnson/jog/internal/tui"
	"golang.org/x/term"
)

// logFormat renders one timeline entry: short id, age, provenance, then
// the body only when present (%+b) — it holds the maxFileSize skipped-list;
// --name-status or -p supplies the files-changed detail. The leading %n
// separates entries with a blank line, since the detail block runs flush
// against the next header otherwise.
const logFormat = "--format=%n%C(auto,yellow)%h%C(auto,reset)  %C(auto,green)%cr%C(auto,reset)  %s%+b"

// logAllFormat adds the chain each entry belongs to (%S — the ref as
// spelled on the command line).
const logAllFormat = "--format=%n%C(auto,yellow)%h%C(auto,reset)  %C(auto,cyan)%S%C(auto,reset)  %C(auto,green)%cr%C(auto,reset)  %s%+b"

// Log is `jog log [-p] [-n N] [--all] [--json] [--format=F] [path…]`:
// the timeline of the current branch's chain (D5) — or with --all the whole
// forest, every chain interleaved by time (plan D13). The default views
// (interactive on a TTY, plain rows when piped) render from the shared
// timeline builder: every snapshot row carries the commit it was based on,
// and commit/event rows are interleaved wherever HEAD moved. -p and
// --format hand rendering to real `git log` over the exact snapshot ranges,
// exec'd so git's pager and coloring apply — and --json emits the machine
// shape, so scripts and agents never need to know how snapshots map onto
// git refs and ranges. With paths, a restore from the browser touches only
// those paths — `snaps` and `pick` are aliases (verb records what was
// typed).
func Log(verb string, args []string) int {
	patch := false
	all := false
	jsonOut := false
	limit := ""
	userFormat := ""
	var paths []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--patch":
			patch = true
		case a == "--all":
			all = true
		case a == "--json":
			jsonOut = true
		case a == "-n":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "jog: -n wants a count (jog log -n 5)")
				return 2
			}
			i++
			limit = args[i]
		case strings.HasPrefix(a, "-n") && len(a) > 2:
			limit = a[2:]
		case strings.HasPrefix(a, "--format="):
			userFormat = strings.TrimPrefix(a, "--format=")
		case a == "--format":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "jog: --format wants a git log format (jog log --format='%%h %%s')\n")
				return 2
			}
			i++
			userFormat = args[i]
		default:
			paths = append(paths, a)
		}
	}
	if limit != "" {
		if n, err := strconv.Atoi(limit); err != nil || n <= 0 {
			fmt.Fprintf(os.Stderr, "jog: -n wants a positive count, not %q\n", limit)
			return 2
		}
	}
	if jsonOut && patch {
		fmt.Fprintln(os.Stderr, "jog: --json and -p don't combine — the JSON already lists each snapshot's files")
		return 2
	}
	if jsonOut && userFormat != "" {
		fmt.Fprintln(os.Stderr, "jog: --json and --format don't combine — pick one output shape")
		return 2
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	repo, err := gitx.Discover(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}

	// Snapshot first, jj-style: browsing the timeline is a command boundary
	// too, so the tree you're looking from is itself on the timeline.
	// Best-effort; a failure must not block the read.
	snap.Take(repo, provenance.Pre(strings.TrimSpace("jog "+verb+" "+strings.Join(args, " "))))

	var ranges []string
	var boundaries map[string]bool
	if all {
		var err error
		if ranges, boundaries, err = forestRanges(repo); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 1
		}
		if len(ranges) == 0 {
			if jsonOut {
				fmt.Println("[]")
				return 0
			}
			fmt.Println("no snapshots anywhere yet — run `jog`, or any git command via the alias")
			return 0
		}
	} else {
		ref := chainRef(repo)
		if _, err := repo.RunRead("rev-parse", "-q", "--verify", ref); err != nil {
			if jsonOut {
				fmt.Println("[]")
				return 0
			}
			// Loud empty state: until `doctor` exists, snaps doubles as the
			// liveness check — silence here could mask a dead engine.
			fmt.Printf("no snapshots on %s yet — run `jog`, or any git command via the alias\n",
				strings.TrimPrefix(ref, "refs/jog/"))
			return 0
		}

		// The default single-chain views render from the timeline builder —
		// browse on a TTY, plain rows when piped. Only -p, --format, and
		// --json continue past here to the range-based paths.
		if !patch && userFormat == "" && !jsonOut {
			n, _ := strconv.Atoi(limit) // "" → 0 = unlimited; validated above
			if term.IsTerminal(int(os.Stdout.Fd())) {
				return chainTUI(repo, ref, paths, n)
			}
			return logPiped(repo, ref, paths, n)
		}

		_, rng, boundary, _, err := chainRange(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 1
		}
		ranges = []string{rng}
		if boundary != "" {
			boundaries = map[string]bool{boundary: true}
		}
	}

	// Machine output first: JSON is JSON on a TTY too, so scripts and
	// agents get the same bytes everywhere.
	if jsonOut {
		return logJSON(repo, ranges, boundaries, paths, limit)
	}

	// --all on a terminal: browse the forest. -p keeps the plain patch
	// printout even on a TTY, --format means the caller wants specific
	// bytes, and piped --all output is the git passthrough.
	if !patch && userFormat == "" && term.IsTerminal(int(os.Stdout.Fd())) {
		return forestTUI(repo, ranges, paths, limit)
	}

	format := logFormat
	if all {
		// %S attributes each entry to the chain it came from, spelled as
		// passed — forestRanges passes `jog/<branch>` so the label is short.
		format = logAllFormat
	}
	gitArgs := []string{"log", "--first-parent"}
	if userFormat != "" {
		// A caller-supplied format owns the output: no --name-status
		// tacked on, so `--format=%H` really is one line per snapshot.
		gitArgs = append(gitArgs, "--format="+userFormat)
		if patch {
			gitArgs = append(gitArgs, "-p")
		}
	} else {
		gitArgs = append(gitArgs, format)
		if patch {
			gitArgs = append(gitArgs, "-p")
		} else {
			gitArgs = append(gitArgs, "--name-status")
		}
	}
	if limit != "" {
		gitArgs = append(gitArgs, "-n", limit)
	}
	gitArgs = append(gitArgs, ranges...)
	if len(paths) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	return execGit(gitArgs)
}

// snapEntry is one timeline entry in `jog log --json` — everything an
// agent or script needs without knowing how snapshots map onto git refs:
// ids for jog restore/since, an ISO timestamp, provenance, the chain, and
// the files each snapshot changed.
type snapEntry struct {
	ID         string     `json:"id"`
	SHA        string     `json:"sha"`
	Base       string     `json:"base"` // the HEAD commit the snapshot was based on; "" pre-first-commit
	Time       string     `json:"time"`
	Age        string     `json:"age"`
	Chain      string     `json:"chain"`
	Provenance string     `json:"provenance"`
	Note       string     `json:"note,omitempty"`
	Files      []snapFile `json:"files"`
}

// snapFile is one changed file: git's status letter (M, A, D, R, C…), the
// path, and for renames/copies the path it came from.
type snapFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	From   string `json:"from,omitempty"`
}

// logJSON prints the timeline as a JSON array. One git call: each entry's
// fields are fenced by \x1e records and \x1f fields (the body can hold
// newlines, so line-based parsing would be ambiguous); the --name-status
// block for a record lands between its terminator and the next record.
// boundaries — each chain's root parent — classify the single-parent case:
// a chain root's one parent is a boundary (its base commit), an unborn-era
// snapshot's one parent is the previous snapshot (no base). The two are
// disjoint by construction: boundaries are never jog commits.
func logJSON(repo *gitx.Repo, ranges []string, boundaries map[string]bool, paths []string, limit string) int {
	gitArgs := []string{"log", "--first-parent",
		"--format=%x1e%H%x1f%cI%x1f%cr%x1f%S%x1f%s%x1f%P%x1f%b%x1e", "--name-status"}
	if limit != "" {
		gitArgs = append(gitArgs, "-n", limit)
	}
	gitArgs = append(gitArgs, ranges...)
	if len(paths) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	out, err := repo.RunRead(gitArgs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}

	entries := []snapEntry{}
	chunks := strings.Split(out, "\x1e")
	// chunks alternate: junk, fields, name-status block, fields, block, …
	for i := 1; i < len(chunks); i += 2 {
		f := strings.SplitN(chunks[i], "\x1f", 7)
		if len(f) != 7 {
			continue
		}
		e := snapEntry{
			ID: f[0][:7], SHA: f[0], Time: f[1], Age: f[2],
			Chain: chainName(f[3]), Provenance: f[4],
			Note: strings.TrimSpace(f[6]), Files: []snapFile{},
		}
		switch parents := strings.Fields(f[5]); {
		case len(parents) > 1:
			e.Base = parents[1]
		case len(parents) == 1 && boundaries[parents[0]]:
			e.Base = parents[0] // chain root: parent 1 IS the base commit
		}
		if i+1 < len(chunks) {
			for _, line := range strings.Split(chunks[i+1], "\n") {
				parts := strings.Split(strings.TrimSpace(line), "\t")
				switch {
				case len(parts) == 2:
					e.Files = append(e.Files, snapFile{Status: parts[0][:1], Path: parts[1]})
				case len(parts) == 3: // rename/copy: status score, from, to
					e.Files = append(e.Files, snapFile{Status: parts[0][:1], Path: parts[2], From: parts[1]})
				}
			}
		}
		entries = append(entries, e)
	}

	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	fmt.Println(string(b))
	return 0
}

// chainName normalizes a %S ref spelling (refs/jog/main, jog/main) to the
// bare chain name users see everywhere else (main, @detached).
func chainName(s string) string {
	s = strings.TrimPrefix(s, "refs/")
	return strings.TrimPrefix(s, "jog/")
}

// logPiped is the default single-chain output off a TTY: the same rows the
// browser shows — base column, event rows — printed plainly (lipgloss drops
// styling when piped) with each snapshot's --name-status block, replacing
// the old git passthrough, which could not render the base edge or
// interleave commits.
func logPiped(repo *gitx.Repo, ref string, paths []string, limit int) int {
	rows, err := buildTimeline(repo, ref, paths, limit, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("\n")
		if r.kind != rowSnap {
			b.WriteString(r.markerLabel() + "\n")
			continue
		}
		b.WriteString(r.snapLabel(true) + "\n")
		if r.body != "" {
			b.WriteString(r.body + "\n")
		}
		if r.files != "" {
			b.WriteString("\n" + r.files + "\n")
		}
	}
	fmt.Print(b.String())
	return 0
}

// chainTUI is the interactive single-chain timeline: the two-frame browser
// over whole-tree snapshots, with inert commit rows for context. r asks y/n
// before restoring, and a confirmed restore goes through the restore
// machinery, so it is snapshotted first and undoable like any other.
func chainTUI(repo *gitx.Repo, ref string, paths []string, limit int) int {
	rows, err := buildTimeline(repo, ref, paths, limit, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		// The chain exists (empty chains were handled above); a path
		// filter left nothing.
		fmt.Printf("no snapshots touch %s\n", strings.Join(paths, " "))
		return 0
	}
	items := make([]tui.PickItem, 0, len(rows))
	for _, r := range rows {
		if r.kind == rowSnap {
			// Selectable labels stay byte-plain: the browser's cursor row
			// is raw reverse video that an embedded reset would cut short.
			items = append(items, tui.PickItem{ID: r.sha, Label: r.snapLabel(false)})
		} else {
			items = append(items, tui.PickItem{ID: r.sha, Label: r.markerLabel(), Inert: true})
		}
	}
	title := "snapshots on " + strings.TrimPrefix(ref, "refs/jog/")
	return runTimelinePick(repo, title, items, paths)
}

// forestTUI is the --all browser: every chain's snapshots interleaved by
// time, each row naming its chain. (Base columns and commit rows are
// single-chain views for now.)
func forestTUI(repo *gitx.Repo, ranges, paths []string, limit string) int {
	items, err := forestItems(repo, ranges, paths, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if len(items) == 0 {
		fmt.Printf("no snapshots touch %s\n", strings.Join(paths, " "))
		return 0
	}
	return runTimelinePick(repo, "snapshots on every chain", items, paths)
}

// runTimelinePick runs the browser over prepared items and dispatches a
// confirmed choice to restore. The footer owns the hotkeys; the title just
// says where you are.
func runTimelinePick(repo *gitx.Repo, title string, items []tui.PickItem, paths []string) int {
	confirm := "restore the whole tree to %s? y/n"
	if len(paths) > 0 {
		confirm = fmt.Sprintf("restore %s to %%s? y/n", strings.Join(paths, " "))
	}
	chosen, aborted, err := tui.RunPick(title, items,
		func(id string) string { return snapPreview(repo, id, paths) },
		confirm,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if aborted {
		return 0
	}
	if len(paths) > 0 {
		return Restore("restore", append(append([]string{}, paths...), "--at", chosen))
	}
	return Restore("restore", []string{"--all", "--at", chosen})
}

// forestItems lists the --all browser's rows — id, chain, age, provenance —
// over the exact snapshot ranges.
func forestItems(repo *gitx.Repo, ranges, paths []string, limit string) ([]tui.PickItem, error) {
	gitArgs := append([]string{"log", "--first-parent", "--format=%H\x1f%S\x1f%cr\x1f%s"}, ranges...)
	if limit != "" {
		gitArgs = append(gitArgs, "-n", limit)
	}
	if len(paths) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	out, err := repo.RunRead(gitArgs...)
	if err != nil || out == "" {
		return nil, err
	}

	var items []tui.PickItem
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(line, "\x1f", 4); len(f) == 4 {
			items = append(items, tui.PickItem{ID: f[0],
				Label: fmt.Sprintf("%s  %s  %s  %s", f[0][:7], f[1], f[2], f[3])})
		}
	}
	return items, nil
}

// snapPreview renders what a snapshot changed: stat header, then the patch.
// Same parent-1-only diff as versionPreview — snapshots are two-parent
// commits, so a plain `git show` would render a useless combined diff.
func snapPreview(repo *gitx.Repo, sha string, paths []string) string {
	gitArgs := []string{"log", "--first-parent", "-1", "--color=always", "--stat", "-p", "--format=", sha}
	if len(paths) > 0 {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	out, err := repo.RunRead(gitArgs...)
	if err != nil || out == "" {
		return "(no preview)"
	}
	return out
}

// forestRanges builds the git log arguments covering every chain: each tip
// (spelled `jog/<branch>` so %S renders short), plus a `^boundary` per chain
// so no walk runs off its oldest snapshot into real history. Multi-tip
// --first-parent follows each chain's own first-parent line and interleaves
// by commit date (lab-verified, D13).
func forestRanges(repo *gitx.Repo) ([]string, map[string]bool, error) {
	out, err := repo.RunRead("for-each-ref", "--format=%(refname)", "refs/jog/")
	if err != nil || out == "" {
		return nil, nil, err
	}
	var args []string
	var bounds []string
	boundaries := map[string]bool{}
	for _, ref := range strings.Split(out, "\n") {
		if strings.HasPrefix(ref, "refs/jog/@trash/") {
			continue // trim's insurance refs are not chains
		}
		boundary, err := chainBoundary(repo, ref)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "jog/"+strings.TrimPrefix(ref, "refs/jog/"))
		if boundary != "" {
			bounds = append(bounds, "^"+boundary)
			boundaries[boundary] = true
		}
	}
	return append(args, bounds...), boundaries, nil
}

// chainRange resolves the current chain ref, the git range covering exactly
// its snapshots — `boundary..ref` — and the boundary itself (see
// chainBoundary).
func chainRange(repo *gitx.Repo) (ref, rng, boundary string, exists bool, err error) {
	ref = chainRef(repo)
	if _, verr := repo.RunRead("rev-parse", "-q", "--verify", ref); verr != nil {
		return ref, "", "", false, nil
	}
	if boundary, err = chainBoundary(repo, ref); err != nil {
		return "", "", "", false, err
	}
	rng = ref
	if boundary != "" {
		rng = boundary + ".." + ref
	}
	return ref, rng, boundary, true, nil
}

// chainBoundary finds where a chain's snapshots end and real history begins.
// The oldest snapshot's parent 1 is a real HEAD commit, so an unbounded
// --first-parent walk would run off the chain; the boundary is the first
// commit not committed by the fixed jog identity (D1). Empty when the chain
// bottoms out without one (unborn-HEAD root). Streamed and killed early —
// the walk below the boundary is the repo's whole history.
func chainBoundary(repo *gitx.Repo, ref string) (string, error) {
	cmd, out, err := repo.StartRead("log", "--first-parent", "--format=%H %ce", ref)
	if err != nil {
		return "", err
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		hash, email, _ := strings.Cut(scanner.Text(), " ")
		if email != snap.IdentityEmail {
			return hash, nil
		}
	}
	return "", nil
}

// chainRef resolves the current branch's chain ref (refs/jog/<branch>, or
// refs/jog/@detached on a detached HEAD).
func chainRef(repo *gitx.Repo) string {
	branch, detached := repo.HeadBranch()
	if detached {
		return "refs/jog/@detached"
	}
	return "refs/jog/" + branch
}

// recentEntries returns the newest n timeline lines, for the bare-`jog`
// readout (D6) — the same rows the log renders: base columns, and any
// commit rows that fall inside the window ride along free of the count.
func recentEntries(repo *gitx.Repo, n int) []string {
	rows, err := buildTimeline(repo, chainRef(repo), nil, n, false)
	if err != nil {
		return nil
	}
	var lines []string
	for _, r := range rows {
		if r.kind == rowSnap {
			lines = append(lines, r.snapLabel(true))
		} else {
			lines = append(lines, r.markerLabel())
		}
	}
	return lines
}
