package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Trim is `jog trim [--dry-run]`: drop snapshots older than the keep
// window (default 90 days) from every chain. The only jog command that
// discards data, so it is layered in seams — list, plan, apply — with a
// dry-run, a one-deep insurance ref, and CAS-guarded writes. Runs by
// hand, and in the background on the jog.autoTrim cadence (autotrim.go)
// — which is just this command, detached, so both paths share every
// safety seam.
//
// The rewrite (plan D17): survivors are re-committed with tree, dates, and
// message verbatim; parent 1 relinks to the previous survivor; parent 2
// (the base edge) is preserved untouched — it records where HEAD was, and
// rewriting records is forgery. The reflog is replayed with each survivor's
// original timestamp (update-ref honors GIT_COMMITTER_DATE, lab-verified),
// so @{time} queries stay truthful.
func Trim(args []string) int {
	dry, gone := false, false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dry = true
		case "--gone":
			gone = true
		default:
			fmt.Fprintf(os.Stderr, "jog: trim takes only --dry-run and --gone (got %q)\n", a)
			return 2
		}
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

	if !dry {
		// Any completed run resets the auto-trim clock — a manual trim
		// buys the repo a full quiet interval. Deferred so per-chain
		// errors still count as a run (they would repeat on the cadence,
		// not per command).
		defer stampTrim(repo)
		// A command boundary like any other: the tree you trim from is on
		// the timeline first (and, being brand new, sits far inside the
		// keep window — untouchable).
		if _, err := snap.Take(repo, provenance.Pre(strings.TrimSpace("jog trim "+strings.Join(args, " ")))); err != nil {
			fmt.Fprintf(os.Stderr, "jog: pre-trim snapshot failed: %v\n", err)
		}
	}

	keepFor := trimKeep(repo)
	now := time.Now()

	out, err := repo.RunRead("for-each-ref", "--format=%(refname)", "refs/jog/")
	if err != nil || out == "" {
		fmt.Println("no snapshots anywhere yet — nothing to trim")
		return 0
	}

	// Every chain is listed up front: the size budget needs the global
	// picture before any per-chain plan is final.
	var chains []trimChain
	var trashRefs []string
	chainExists := map[string]bool{}
	for _, ref := range strings.Split(out, "\n") {
		if strings.HasPrefix(ref, "refs/jog/@trash/") {
			trashRefs = append(trashRefs, ref)
			continue // insurance refs are not chains
		}
		chainExists[strings.TrimPrefix(ref, "refs/jog/")] = true
		entries, err := listChain(repo, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", ref, err)
			continue
		}
		chains = append(chains, trimChain{ref, entries, true})
	}

	// A chain is live while its branch still exists. A live tip is
	// immortal — the last state of a branch is its whole point — but a
	// dead chain's tip ages out like the rest, so deleted branches (and
	// @detached work) eventually vanish whole. If the branch listing
	// fails, every chain stays live: erring immortal loses nothing.
	if bout, err := repo.RunRead("for-each-ref", "--format=%(refname:short)", "refs/heads/"); err == nil {
		branches := map[string]bool{}
		for _, b := range strings.Split(bout, "\n") {
			branches[b] = true
		}
		if cur, detached := repo.HeadBranch(); !detached {
			branches[cur] = true // unborn HEAD: the branch is current, just refless
		}
		for i := range chains {
			chains[i].live = branches[strings.TrimPrefix(chains[i].ref, "refs/jog/")]
		}
	}

	// Trash whose chain is gone insures nothing — the chain aged out (or
	// was removed) on an earlier run, and its one-cycle grace ends now.
	trimmed := 0
	for _, tref := range trashRefs {
		name := strings.TrimPrefix(tref, "refs/jog/@trash/")
		if chainExists[name] {
			continue
		}
		if dry {
			fmt.Printf("%s: would remove stale trash (its chain is gone)\n", name)
			continue
		}
		if _, err := repo.Run("update-ref", "-d", tref); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", tref, err)
			continue
		}
		trimmed++
		fmt.Printf("%s: stale trash removed (its chain was already gone)\n", name)
	}

	cutoff := keepFor
	if budget := trimMaxSize(repo); budget > 0 {
		var msg string
		cutoff, msg = applyBudget(repo, chains, keepFor, now, budget, dry, gone)
		if msg != "" {
			fmt.Println(msg)
		}
	}

	anyDrops := false
	for _, c := range chains {
		name := strings.TrimPrefix(c.ref, "refs/jog/")
		keep := planTrim(cutoff, now, c.entries)
		if gone && !c.live {
			for i := range keep {
				keep[i] = false
			}
		}
		drops := 0
		var oldest string
		for i, k := range keep {
			if !k {
				drops++
				oldest = c.entries[i].subjectLine() // list is newest-first
			}
		}
		if drops == 0 {
			fmt.Printf("%s: %d %s, nothing to trim\n", name, len(c.entries), plural(len(c.entries), "snapshot"))
			continue
		}
		anyDrops = true
		removed := drops == len(c.entries)
		if dry {
			switch {
			case removed && !c.live:
				fmt.Printf("%s: branch is gone — would remove the chain (%d %s)\n",
					name, drops, plural(drops, "snapshot"))
			case removed:
				fmt.Printf("%s: would drop all %d %s and remove the chain\n",
					name, drops, plural(drops, "snapshot"))
			default:
				fmt.Printf("%s: would drop %d of %d %s (oldest: %s)\n",
					name, drops, len(c.entries), plural(len(c.entries), "snapshot"), oldest)
			}
			continue
		}
		if err := applyTrim(repo, c.ref, c.entries, keep); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", name, err)
			continue
		}
		trimmed++
		switch {
		case removed && !c.live:
			fmt.Printf("%s: branch is gone — chain removed (%d %s saved at refs/jog/@trash/%s until the next trim)\n",
				name, drops, plural(drops, "snapshot"), name)
		case removed:
			fmt.Printf("%s: dropped all %d %s — chain removed (saved at refs/jog/@trash/%s until the next trim)\n",
				name, drops, plural(drops, "snapshot"), name)
		default:
			fmt.Printf("%s: dropped %d of %d %s — previous tip saved at refs/jog/@trash/%s until the next trim\n",
				name, drops, len(c.entries), plural(len(c.entries), "snapshot"), name)
		}
	}

	if !dry && trimmed > 0 {
		// Plumbing never triggers gc --auto on its own (verified); this is
		// where dropped snapshots eventually get reclaimed — one trim cycle
		// later, once the insurance ref moves off them.
		if _, err := repo.Run("gc", "--auto", "--quiet"); err != nil {
			fmt.Fprintf(os.Stderr, "jog: gc --auto: %v\n", err)
		}
	}

	// The bill: what the timeline costs now, and — when this run drops
	// anything — where it settles. Reclaim is deferred by design (the
	// @trash ref holds dropped data one more cycle), so the wording never
	// promises immediate shrink. Silent skip on git without --disk-usage.
	if size, err := jogDiskUsage(repo); err == nil {
		line := fmt.Sprintf("snapshots hold ~%s", humanBytes(size))
		if anyDrops {
			if dry {
				if proj, perr := treesDiskUsage(repo, survivorTrees(chains, now, cutoff, gone)); perr == nil {
					line += fmt.Sprintf(" — this plan settles to ~%s after the next trim + gc", humanBytes(proj))
				}
			} else {
				line += " — dropped data frees after the next trim + gc"
			}
		}
		fmt.Println(line)
	}
	return 0
}

// trimChain is one chain as listed for planning: its ref, its snapshots
// newest first, and whether its branch still exists.
type trimChain struct {
	ref     string
	entries []chainEntry
	live    bool
}

// survivorTrees collects the tree shas planTrim would keep at the given
// cutoff, across all chains — the input to a size projection. With gone
// set, dead chains contribute nothing (they are removed whole).
func survivorTrees(chains []trimChain, now time.Time, cutoff time.Duration, gone bool) []string {
	var trees []string
	for _, c := range chains {
		if gone && !c.live {
			continue
		}
		keep := planTrim(cutoff, now, c.entries)
		for i, k := range keep {
			if k {
				trees = append(trees, c.entries[i].tree)
			}
		}
	}
	return trees
}

// applyBudget enforces jog.maxSize by tightening the age cutoff — never
// loosening it. Deliberately one snapshot lenient: the cutoff lands ON
// the snapshot that crosses the budget rather than before it ("keep 100
// when 99 fit"), so the result runs at most one snapshot over, a budget
// exceeded only by its crossing snapshot drops nothing, and even a
// 1-byte budget leaves the newest snapshot. No protected tips beyond
// that — when one snapshot alone busts the budget, the fix is a bigger
// maxSize or a smaller maxFileSize. The projection is monotonic in the
// cutoff, so a binary search over "keep the m youngest" finds the
// crossing in ~log2(n) rev-list probes.
func applyBudget(repo *gitx.Repo, chains []trimChain, keepFor time.Duration, now time.Time, budget int64, dry, gone bool) (time.Duration, string) {
	probe := func(cutoff time.Duration) (int64, error) {
		return treesDiskUsage(repo, survivorTrees(chains, now, cutoff, gone))
	}
	size, err := probe(keepFor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: size budget ignored — measuring needs git ≥ 2.31 (%v)\n", err)
		return keepFor, ""
	}
	if size <= budget {
		return keepFor, ""
	}

	// Candidates: every snapshot inside the keep window, youngest first
	// (dead chains excluded under --gone — they are removed whole).
	// Keeping the m youngest means a cutoff at the m-th age; over budget
	// with survivors means at least one candidate exists.
	var ages []time.Duration
	for _, c := range chains {
		if gone && !c.live {
			continue
		}
		for _, e := range c.entries {
			if age := now.Sub(time.Unix(e.commitUnix, 0)); age <= keepFor {
				ages = append(ages, age)
			}
		}
	}
	sort.Slice(ages, func(i, j int) bool { return ages[i] < ages[j] })
	cutoffFor := func(m int) time.Duration {
		if m == 0 {
			return 0
		}
		return ages[m-1]
	}
	fits := func(m int) bool {
		n, err := probe(cutoffFor(m))
		return err == nil && n <= budget
	}
	// Largest m that fits: fits(0) holds trivially, and fits(len(ages))
	// is the full window, measured over above.
	lo, hi, best := 1, len(ages)-1, 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if fits(mid) {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}

	if best+1 == len(ages) {
		// Only the crossing snapshot is over: the leniency covers it.
		return keepFor, fmt.Sprintf("size budget %s: within one snapshot of the budget — nothing to drop", humanBytes(budget))
	}
	verb := "dropping"
	if dry {
		verb = "would drop"
	}
	cutoff := cutoffFor(best + 1) // one past: the crossing snapshot stays
	return cutoff, fmt.Sprintf("size budget %s: %s snapshots older than %s this run",
		humanBytes(budget), verb, humanDur(cutoff))
}

// humanDur renders a cutoff age for humans; always approximate.
func humanDur(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("~%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("~%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("~%d minutes", int(d.Minutes()))
	}
}

// trimMaxSize reads jog.maxSize (see config.go); 0 or unset means no
// size budget.
func trimMaxSize(repo *gitx.Repo) int64 {
	out, err := repo.RunRead("config", "--type=int", "--get", "jog.maxSize")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(out, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// chainEntry is one snapshot as listed from a chain, newest first.
type chainEntry struct {
	sha        string
	tree       string
	authorDate string // raw ("1723164000 -0600") — env-format for recommit
	commitDate string // raw
	commitUnix int64
	parents    []string
	message    string // full %B, trimmed
}

func (e chainEntry) subjectLine() string {
	s, _, _ := strings.Cut(e.message, "\n")
	return s
}

// base returns the entry's base edge (the HEAD commit at snapshot time)
// given the sha of its predecessor on the chain ("" for the chain-oldest,
// whose parent 1 — when present — is itself the base).
func (e chainEntry) base(prevSha string) string {
	if prevSha == "" {
		if len(e.parents) > 0 {
			return e.parents[0]
		}
		return "" // unborn-HEAD root: no base at all
	}
	if len(e.parents) > 1 {
		return e.parents[1]
	}
	return "" // unborn-era snapshot: prev only
}

// listChain streams the chain newest-first, stopping at the boundary (first
// non-jog committer, D1) — the walk below it is the repo's whole history.
func listChain(repo *gitx.Repo, ref string) ([]chainEntry, error) {
	cmd, out, err := repo.StartRead("log", "--first-parent", "--date=raw",
		"--format=%H%x1f%T%x1f%ad%x1f%cd%x1f%ct%x1f%ce%x1f%P%x1f%B%x00", ref)
	if err != nil {
		return nil, err
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	var entries []chainEntry
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF {
			return 0, nil, bufio.ErrFinalToken
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		rec := strings.TrimLeft(scanner.Text(), "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 8)
		if len(f) != 8 {
			return nil, fmt.Errorf("unexpected log record (%d fields)", len(f))
		}
		if f[5] != snap.IdentityEmail {
			break // boundary: real history begins here
		}
		unix, err := strconv.ParseInt(strings.Fields(f[4])[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad committer date %q: %v", f[4], err)
		}
		e := chainEntry{
			sha: f[0], tree: f[1], authorDate: f[2], commitDate: f[3],
			commitUnix: unix, message: strings.TrimSpace(f[7]),
		}
		if f[6] != "" {
			e.parents = strings.Fields(f[6])
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// planTrim maps the retention window over a chain: a snapshot survives
// iff it is younger than cutoff — the keep window, possibly tightened by
// the size budget. No exceptions, tips included: a chain older than the
// whole window plans to zero survivors and is removed whole.
func planTrim(cutoff time.Duration, now time.Time, entries []chainEntry) []bool {
	keep := make([]bool, len(entries))
	for i, e := range entries {
		keep[i] = now.Sub(time.Unix(e.commitUnix, 0)) <= cutoff
	}
	return keep
}

// applyTrim rewrites one chain to its survivors. Failure modes are ordered
// to stay safe: the insurance ref is written before anything moves, the
// delete is CAS-guarded against a concurrent snapshot (the loser skips,
// v0's contention policy), and a mid-replay failure leaves the old tip
// recoverable from the insurance ref.
func applyTrim(repo *gitx.Repo, ref string, entries []chainEntry, keep []bool) error {
	tip := entries[0].sha
	if cur, err := repo.RunRead("rev-parse", "-q", "--verify", ref); err != nil || cur != tip {
		return fmt.Errorf("chain moved while trimming (a concurrent snapshot won); skipped — rerun trim")
	}

	name := strings.TrimPrefix(ref, "refs/jog/")
	trash := "refs/jog/@trash/" + name
	if _, err := repo.Run("update-ref", trash, tip); err != nil {
		return fmt.Errorf("could not write insurance ref %s: %v — chain untouched", trash, err)
	}

	// Rebuild oldest→newest. Original shas are kept until the first drop
	// (or first rewritten parent) — only commits above that point change.
	newShas := make([]string, 0, len(entries))
	var subjects []string
	var dates []string
	prevOrig, prevNew := "", ""
	dirty := false
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !keep[i] {
			dirty = true
			prevOrig = e.sha
			continue
		}
		newSha := e.sha
		if dirty {
			// Parent 1: the previous survivor — or, for a new oldest
			// survivor, its own base edge, so timeline walks still
			// terminate on a real commit it genuinely sat on. Parent 2:
			// the base edge, verbatim.
			ct := []string{"commit-tree", e.tree}
			if prevNew != "" {
				ct = append(ct, "-p", prevNew)
			}
			if b := e.base(prevOrig); b != "" {
				ct = append(ct, "-p", b)
			}
			ct = append(ct, "-m", e.message)
			ident := repo.WithEnv(
				"GIT_AUTHOR_NAME="+snap.IdentityName, "GIT_AUTHOR_EMAIL="+snap.IdentityEmail,
				"GIT_COMMITTER_NAME="+snap.IdentityName, "GIT_COMMITTER_EMAIL="+snap.IdentityEmail,
				"GIT_AUTHOR_DATE="+e.authorDate, "GIT_COMMITTER_DATE="+e.commitDate,
			)
			sha, err := ident.Run(ct...)
			if err != nil {
				return fmt.Errorf("rewrite failed: %v — chain untouched", err)
			}
			newSha = sha
		}
		newShas = append(newShas, newSha)
		subjects = append(subjects, e.subjectLine())
		dates = append(dates, e.commitDate)
		prevOrig, prevNew = e.sha, newSha
	}

	// Swap the ref: CAS-guarded delete (a concurrent mint moves the tip and
	// the delete fails — chain untouched, rerun later), then replay the
	// reflog with original timestamps so @{time} stays truthful.
	if _, err := repo.Run("update-ref", "-d", ref, tip); err != nil {
		return fmt.Errorf("chain moved while trimming (a concurrent snapshot won); skipped — rerun trim")
	}
	prev := ""
	for i, sha := range newShas {
		ident := repo.WithEnv(
			"GIT_COMMITTER_NAME="+snap.IdentityName, "GIT_COMMITTER_EMAIL="+snap.IdentityEmail,
			"GIT_COMMITTER_DATE="+dates[i],
		)
		if _, err := ident.Run("update-ref", "--create-reflog", "-m", subjects[i], ref, sha, prev); err != nil {
			return fmt.Errorf("reflog replay failed at %s: %v — previous tip is saved at %s", sha[:7], err, trash)
		}
		prev = sha
	}
	return nil
}

// defaultKeep is how long snapshots live before trim drops them.
const defaultKeep = 90 * 24 * time.Hour

// trimKeep reads jog.keep (see config.go for the user-facing registry).
// The value uses git's own expiry syntax ("30.days", "6.months",
// "never"), parsed by git itself (--type=expiry-date returns the cutoff
// as epoch seconds; "never" → 0, which makes the window effectively
// infinite — trim keeps everything).
func trimKeep(repo *gitx.Repo) time.Duration {
	out, err := repo.RunRead("config", "--type=expiry-date", "--get", "jog.keep")
	if err != nil {
		return defaultKeep
	}
	epoch, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		return defaultKeep
	}
	return time.Since(time.Unix(epoch, 0))
}
