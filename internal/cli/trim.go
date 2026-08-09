package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/retain"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Trim is `jog trim [--dry-run]`: apply the retention taper to every chain
// (plan M11). The only jog command that discards data, so it is layered in
// seams — list, plan, apply — with a dry-run, a one-deep insurance ref, and
// CAS-guarded writes. Manual-only: nothing schedules it (plan D19).
//
// The rewrite (plan D17): survivors are re-committed with tree, dates, and
// message verbatim; parent 1 relinks to the previous survivor; parent 2
// (the base edge) is preserved untouched — it records where HEAD was, and
// rewriting records is forgery. The reflog is replayed with each survivor's
// original timestamp (update-ref honors GIT_COMMITTER_DATE, lab-verified),
// so @{time} queries stay truthful.
func Trim(args []string) int {
	dry := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dry = true
		default:
			fmt.Fprintf(os.Stderr, "jog: trim takes only --dry-run (got %q)\n", a)
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

	// A command boundary like any other: the tree you trim from is on the
	// timeline first (and lands in the keep-all tier, untouchable).
	if !dry {
		if _, err := snap.Take(repo, provenance.Pre(strings.TrimSpace("jog trim "+strings.Join(args, " ")))); err != nil {
			fmt.Fprintf(os.Stderr, "jog: pre-trim snapshot failed: %v\n", err)
		}
	}

	pol := trimPolicy(repo)
	now := time.Now()

	out, err := repo.RunRead("for-each-ref", "--format=%(refname)", "refs/jog/")
	if err != nil || out == "" {
		fmt.Println("no snapshots anywhere yet — nothing to trim")
		return 0
	}

	trimmed := 0
	for _, ref := range strings.Split(out, "\n") {
		if strings.HasPrefix(ref, "refs/jog/@trash/") {
			continue // insurance refs are not chains
		}
		entries, err := listChain(repo, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", ref, err)
			continue
		}
		name := strings.TrimPrefix(ref, "refs/jog/")
		keep := planTrim(pol, now, entries)
		drops := 0
		var oldest, newest string
		for i, k := range keep {
			if !k {
				drops++
				oldest = entries[i].subjectLine() // list is newest-first
				if newest == "" {
					newest = oldest
				}
			}
		}
		if drops == 0 {
			fmt.Printf("%s: %d snapshots, nothing to trim\n", name, len(entries))
			continue
		}
		if dry {
			fmt.Printf("%s: would drop %d of %d snapshots (oldest: %s)\n", name, drops, len(entries), oldest)
			continue
		}
		if err := applyTrim(repo, ref, entries, keep); err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", name, err)
			continue
		}
		trimmed++
		fmt.Printf("%s: dropped %d of %d snapshots — previous tip saved at refs/jog/@trash/%s until the next trim\n",
			name, drops, len(entries), name)
	}

	if !dry && trimmed > 0 {
		// Plumbing never triggers gc --auto on its own (verified); this is
		// where dropped snapshots eventually get reclaimed — one trim cycle
		// later, once the insurance ref moves off them.
		if _, err := repo.Run("gc", "--auto", "--quiet"); err != nil {
			fmt.Fprintf(os.Stderr, "jog: gc --auto: %v\n", err)
		}
	}
	return 0
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

// planTrim maps the retention policy over a chain. The tip always survives
// — even on a chain older than every tier, the last state of a branch is
// its whole point.
func planTrim(pol retain.Policy, now time.Time, entries []chainEntry) []bool {
	times := make([]time.Time, len(entries))
	for i, e := range entries {
		times[i] = time.Unix(e.commitUnix, 0)
	}
	keep := pol.Keep(now, times)
	if len(keep) > 0 {
		keep[0] = true
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

// trimPolicy reads jog.keepAll/keepHourly/keepDaily (see config.go for
// the user-facing registry). Values use git's own
// expiry syntax ("3.days", "2.weeks", "never"), parsed by git itself
// (--type=expiry-date returns the cutoff as epoch seconds; "never" → 0,
// which turns the tier off by making its window effectively infinite).
func trimPolicy(repo *gitx.Repo) retain.Policy {
	pol := retain.Default
	read := func(key string, def time.Duration) time.Duration {
		out, err := repo.RunRead("config", "--type=expiry-date", "--get", key)
		if err != nil {
			return def
		}
		epoch, err := strconv.ParseInt(out, 10, 64)
		if err != nil {
			return def
		}
		return time.Since(time.Unix(epoch, 0))
	}
	pol.KeepAll = read("jog.keepAll", pol.KeepAll)
	pol.KeepHourly = read("jog.keepHourly", pol.KeepHourly)
	pol.KeepDaily = read("jog.keepDaily", pol.KeepDaily)
	return pol
}
