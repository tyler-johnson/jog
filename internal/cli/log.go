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
// forest, every chain interleaved by time (plan D13). Interactive on a TTY;
// otherwise rendered by real `git log` over the exact snapshot ranges,
// exec'd so git's pager and coloring apply — or as JSON / a caller-supplied
// format, so scripts and agents never need to know how snapshots map onto
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

	format := logFormat
	var ranges []string
	if all {
		var err error
		if ranges, err = forestRanges(repo); err != nil {
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
		// %S attributes each entry to the chain it came from, spelled as
		// passed — forestRanges passes `jog/<branch>` so the label is short.
		format = logAllFormat
	} else {
		ref, rng, exists, err := chainRange(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %v\n", err)
			return 1
		}
		if !exists {
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
		ranges = []string{rng}
	}

	// Machine output first: JSON is JSON on a TTY too, so scripts and
	// agents get the same bytes everywhere.
	if jsonOut {
		return logJSON(repo, ranges, paths, limit)
	}

	// On a terminal, browse instead of print: the same scrub-and-preview
	// TUI as pick, over the whole tree. -p keeps the plain patch printout
	// even on a TTY, --format means the caller wants specific bytes, and
	// piped output is always the git passthrough.
	if !patch && userFormat == "" && term.IsTerminal(int(os.Stdout.Fd())) {
		return logTUI(repo, all, ranges, paths, limit)
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
func logJSON(repo *gitx.Repo, ranges, paths []string, limit string) int {
	gitArgs := []string{"log", "--first-parent",
		"--format=%x1e%H%x1f%cI%x1f%cr%x1f%S%x1f%s%x1f%b%x1e", "--name-status"}
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
		f := strings.SplitN(chunks[i], "\x1f", 6)
		if len(f) != 6 {
			continue
		}
		e := snapEntry{
			ID: f[0][:7], SHA: f[0], Time: f[1], Age: f[2],
			Chain: chainName(f[3]), Provenance: f[4],
			Note: strings.TrimSpace(f[5]), Files: []snapFile{},
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

// logTUI is the interactive timeline: the two-frame browser over whole-tree
// snapshots. r asks y/n before restoring, and a confirmed restore goes
// through the restore machinery, so it is snapshotted first and undoable
// like any other.
func logTUI(repo *gitx.Repo, all bool, ranges, paths []string, limit string) int {
	items, err := timelineItems(repo, all, ranges, paths, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if len(items) == 0 {
		// The chain exists (empty chains were handled above); a path
		// filter left nothing.
		fmt.Printf("no snapshots touch %s\n", strings.Join(paths, " "))
		return 0
	}

	title := "snapshots on every chain — r restores, q leaves everything untouched"
	if !all {
		title = fmt.Sprintf("snapshots on %s — r restores, q leaves everything untouched",
			strings.TrimPrefix(chainRef(repo), "refs/jog/"))
	}
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

// timelineItems lists the browser's rows — id, age, provenance, plus the
// chain column with all — over the exact snapshot ranges.
func timelineItems(repo *gitx.Repo, all bool, ranges, paths []string, limit string) ([]tui.PickItem, error) {
	format := "--format=%H\x1f%cr\x1f%s"
	if all {
		format = "--format=%H\x1f%S\x1f%cr\x1f%s"
	}
	gitArgs := append([]string{"log", "--first-parent", format}, ranges...)
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
		f := strings.SplitN(line, "\x1f", 4)
		switch {
		case all && len(f) == 4:
			items = append(items, tui.PickItem{ID: f[0],
				Label: fmt.Sprintf("%s  %s  %s  %s", f[0][:7], f[1], f[2], f[3])})
		case !all && len(f) == 3:
			items = append(items, tui.PickItem{ID: f[0],
				Label: fmt.Sprintf("%s  %s  %s", f[0][:7], f[1], f[2])})
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
func forestRanges(repo *gitx.Repo) ([]string, error) {
	out, err := repo.RunRead("for-each-ref", "--format=%(refname)", "refs/jog/")
	if err != nil || out == "" {
		return nil, err
	}
	var args []string
	var bounds []string
	for _, ref := range strings.Split(out, "\n") {
		if strings.HasPrefix(ref, "refs/jog/@trash/") {
			continue // trim's insurance refs are not chains
		}
		boundary, err := chainBoundary(repo, ref)
		if err != nil {
			return nil, err
		}
		args = append(args, "jog/"+strings.TrimPrefix(ref, "refs/jog/"))
		if boundary != "" {
			bounds = append(bounds, "^"+boundary)
		}
	}
	return append(args, bounds...), nil
}

// chainRange resolves the current chain ref and the git range covering
// exactly its snapshots — `boundary..ref` (see chainBoundary).
func chainRange(repo *gitx.Repo) (ref, rng string, exists bool, err error) {
	ref = chainRef(repo)
	if _, verr := repo.RunRead("rev-parse", "-q", "--verify", ref); verr != nil {
		return ref, "", false, nil
	}
	boundary, err := chainBoundary(repo, ref)
	if err != nil {
		return "", "", false, err
	}
	rng = ref
	if boundary != "" {
		rng = boundary + ".." + ref
	}
	return ref, rng, true, nil
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
// readout (D6).
func recentEntries(repo *gitx.Repo, n int) []string {
	_, rng, exists, err := chainRange(repo)
	if err != nil || !exists {
		return nil
	}
	out, err := repo.RunRead("log", "--first-parent", "-n", fmt.Sprint(n), "--format=%h  %cr  %s", rng)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
