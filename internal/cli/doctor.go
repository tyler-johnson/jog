package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-johnson/jog/internal/agents"
	"github.com/tyler-johnson/jog/internal/editors"
	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/snap"
)

// Doctor is `jog doctor [--fix]`: verify the net is really under you —
// every check maps to a DESIGN §9 invariant or a wiring step from the
// README (plan M10). Read-only by default; --fix writes exactly the two
// per-repo gc keys and nothing else (D15) — the explicit, consented front
// door for what the engine otherwise writes lazily (v0 D3).
//
// Exit codes: 0 healthy, 1 findings — scriptable.
func Doctor(args []string) int {
	fix := false
	for _, a := range args {
		switch a {
		case "--fix":
			fix = true
		default:
			fmt.Fprintf(os.Stderr, "jog: doctor takes only --fix (got %q)\n", a)
			return 2
		}
	}

	d := &doctor{}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}

	repo, derr := gitx.Discover(wd)
	switch {
	case derr == nil && repo.Bare:
		d.info("repository", "bare — nothing to snapshot, repo checks skipped")
		repo = nil
	case derr != nil:
		// Outside a repo the global wiring is still checkable.
		d.info("repository", "not inside a git repository — repo checks skipped")
		repo = nil
	default:
		d.ok("repository", repo.GitDir)
	}

	if repo != nil {
		d.checkRepo(repo, fix)
	}
	d.checkTriggers()

	if d.findings == 0 {
		fmt.Println("\n" + styleGood.Render("no findings — the net is under you"))
		return 0
	}
	fmt.Printf("\n%d finding(s)", d.findings)
	if d.fixable > 0 && !fix {
		fmt.Print(" — `jog doctor --fix` repairs the gc config")
	}
	fmt.Println()
	return 1
}

type doctor struct {
	findings int
	fixable  int
}

func (d *doctor) ok(what, detail string) {
	fmt.Printf("  %s    %-14s %s\n", styleGood.Render("ok"), what, detail)
}
func (d *doctor) info(what, detail string) {
	fmt.Printf("  %s  %-14s %s\n", styleDim.Render("info"), what, detail)
}
func (d *doctor) warn(what, detail string) {
	d.findings++
	fmt.Printf("  %s  %-14s %s\n", styleWarn.Render("WARN"), what, detail)
}

func (d *doctor) checkRepo(repo *gitx.Repo, fix bool) {
	// Chains + liveness: the age report is the "is the engine alive" check
	// snaps' loud empty state has been standing in for.
	out, err := repo.RunRead("for-each-ref",
		"--format=%(refname)\x1f%(committerdate:unix)\x1f%(committeremail)", "refs/jog/")
	if err != nil || out == "" {
		d.warn("chains", "no refs/jog/* refs — the engine has never run here (run `jog`, or any git command via the alias)")
		return
	}

	var chainLines []string
	var trashLines []string
	badIdentity := 0
	noReflog := 0
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\x1f")
		if len(parts) != 3 {
			continue
		}
		ref, unix, email := parts[0], parts[1], strings.Trim(parts[2], "<>")
		name := strings.TrimPrefix(ref, "refs/jog/")
		if strings.HasPrefix(name, "@trash/") {
			// trim's one-deep insurance refs — not chains, just report.
			if sec, err := strconv.ParseInt(unix, 10, 64); err == nil {
				trashLines = append(trashLines, fmt.Sprintf("%s %s",
					strings.TrimPrefix(name, "@trash/"), humanAge(time.Since(time.Unix(sec, 0)))))
			}
			continue
		}
		if sec, err := strconv.ParseInt(unix, 10, 64); err == nil {
			chainLines = append(chainLines, fmt.Sprintf("%s %s", name, humanAge(time.Since(time.Unix(sec, 0)))))
		}
		// The D1 identity is what fences every timeline walk; a tip that
		// lost it means something other than jog moved the ref.
		if email != snap.IdentityEmail {
			badIdentity++
			d.warn("identity", fmt.Sprintf("%s tip committer is %q, not %q — the chain was moved by something other than jog", name, email, snap.IdentityEmail))
		}
		// Reflogs power every @{…} time query (refs outside refs/heads get
		// none by default — the engine creates them; verified).
		if _, err := repo.RunRead("reflog", "exists", ref); err != nil {
			noReflog++
			d.warn("reflog", fmt.Sprintf("%s has no reflog — @{time} queries will not work on it", name))
		}
	}
	d.ok("chains", fmt.Sprintf("%d (%s)", len(chainLines), strings.Join(chainLines, ", ")))
	if len(trashLines) > 0 {
		d.info("trim trash", fmt.Sprintf("pre-trim tips held until the next trim: %s", strings.Join(trashLines, ", ")))
	}
	if badIdentity == 0 {
		d.ok("identity", fmt.Sprintf("chain tips carry %s <%s>", snap.IdentityName, snap.IdentityEmail))
	}
	if noReflog == 0 {
		d.ok("reflogs", "present on every chain")
	}

	// gc protection: without these, a manual `git gc` may expire jog reflog
	// entries (per-ref-pattern config wins over globals, verified).
	expire, eerr := repo.RunRead("config", "--local", "--get", "gc.refs/jog/*.reflogExpire")
	unreach, uerr := repo.RunRead("config", "--local", "--get", "gc.refs/jog/*.reflogExpireUnreachable")
	if eerr != nil || uerr != nil || expire != "never" || unreach != "never" {
		if fix {
			ferr1 := error(nil)
			if _, err := repo.Run("config", "gc.refs/jog/*.reflogExpire", "never"); err != nil {
				ferr1 = err
			}
			if _, err := repo.Run("config", "gc.refs/jog/*.reflogExpireUnreachable", "never"); err != nil {
				ferr1 = err
			}
			if ferr1 != nil {
				d.warn("gc config", fmt.Sprintf("could not write gc keys: %v", ferr1))
			} else {
				d.ok("gc config", "reflog expiry disabled for refs/jog/* (fixed)")
			}
		} else {
			d.fixable++
			d.warn("gc config", "gc.refs/jog/*.reflogExpire{,Unreachable} not `never` — a manual `git gc` could expire jog reflog entries (--fix writes them)")
		}
	} else {
		d.ok("gc config", "reflog expiry disabled for refs/jog/*")
	}

	if _, err := os.Stat(filepath.Join(repo.GitDir, "jog", "index")); err == nil {
		d.info("shadow index", "present (seeded)")
	} else {
		d.info("shadow index", "absent — will seed on the first dirty snapshot")
	}

	if out, err := repo.RunRead("config", "--type=int", "--get", "jog.maxFileSize"); err == nil {
		d.info("max file size", out+" bytes (the maxFileSize setting — `jog config` explains it)")
	} else {
		d.info("max file size", "50 MiB (default)")
	}

	d.checkDisk(repo, out)
}

// checkDisk reports what the timeline costs and whether trim has work —
// the same plan `jog trim --dry-run` would compute, reduced to one line.
// info, not warn: an old timeline is data waiting for a decision, not a
// broken net. refLines is checkRepo's for-each-ref output, reused so the
// refs are only listed once.
func (d *doctor) checkDisk(repo *gitx.Repo, refLines string) {
	size, err := jogDiskUsage(repo)
	if err != nil {
		d.info("snapshot disk", "unavailable (needs git ≥ 2.31)")
		return
	}
	d.info("snapshot disk", "~"+humanBytes(size))

	keepFor := trimKeep(repo)
	now := time.Now()
	drops := 0
	for _, line := range strings.Split(refLines, "\n") {
		ref, _, _ := strings.Cut(line, "\x1f")
		if !strings.HasPrefix(ref, "refs/jog/") || strings.HasPrefix(ref, "refs/jog/@trash/") {
			continue
		}
		entries, err := listChain(repo, ref)
		if err != nil {
			continue
		}
		for _, k := range planTrim(keepFor, now, entries) {
			if !k {
				drops++
			}
		}
	}

	budget := trimMaxSize(repo)
	overBudget := budget > 0 && size > budget
	switch {
	case drops > 0 && overBudget:
		d.info("trim", fmt.Sprintf("%d %s older than %s, and over the size budget — `jog trim` drops them (--dry-run previews)",
			drops, plural(drops, "snapshot"), humanDur(keepFor)))
	case drops > 0:
		d.info("trim", fmt.Sprintf("%d %s older than %s — `jog trim` drops them (--dry-run previews)",
			drops, plural(drops, "snapshot"), humanDur(keepFor)))
	case overBudget:
		d.info("trim", "over the maxSize budget — `jog trim` drops oldest snapshots to fit (--dry-run previews)")
	default:
		d.info("trim", "nothing to drop — every snapshot is inside the keep window")
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// checkTriggers verifies something is actually invoking the engine: the
// Claude hooks (deterministic — settings.json either wires `jog hook
// claude` or it doesn't) and the shell alias (heuristic — an rc-file grep
// can't see a live shell's aliases, so it is reported, never asserted).
// Neither wired at all is the real finding: a silent engine feels safe
// while capturing nothing.
func (d *doctor) checkTriggers() {
	home, err := os.UserHomeDir()
	if err != nil {
		d.info("triggers", "cannot resolve home directory; wiring checks skipped")
		return
	}

	// Wherever the user chose to wire — user or project scope — doctor
	// should find it. Every registered client is checked, so a new client
	// in internal/agents shows up here without doctor changing. Clients
	// with no jog integration at all collapse into one line: their absence
	// is unremarkable, and six clients' worth of "not installed" would
	// bury the findings that matter.
	hooks := false
	absent := 0
	for _, s := range agents.Statuses() {
		if s.HooksLocation == "" && s.SkillLocation == "" {
			absent++
			continue
		}
		if s.HooksLocation != "" {
			d.ok(s.Name+" hooks", "`jog hook "+s.Name+"` wired in "+s.HooksLocation)
			hooks = true
		} else {
			d.info(s.Name+" hooks", "not wired (optional — `jog agents install`)")
		}
		if s.SkillLocation != "" {
			d.ok(s.Name+" skill", "installed at "+s.SkillLocation)
		} else {
			d.info(s.Name+" skill", "not installed (optional — `jog agents install`)")
		}
	}
	if absent > 0 {
		d.info("agent clients", fmt.Sprintf("%d more supported, not integrated (optional — `jog agents install`)", absent))
	}

	// Editor save hooks are triggers too — one line per installed editor,
	// the uninstalled majority collapsed, same shape as the agents above.
	absentEditors := 0
	for _, s := range editors.Statuses() {
		if s.Location == "" {
			absentEditors++
			continue
		}
		d.ok(s.Name+" editor", "`jog editor-hook "+s.Name+"` wired at "+s.Location)
		hooks = true
	}
	if absentEditors > 0 {
		d.info("editors", fmt.Sprintf("%d supported, not integrated (optional — `jog editors install <name>`)", absentEditors))
	}

	aliasFile := ""
	rcs := []string{".bashrc", ".zshrc", ".config/fish/config.fish", ".profile"}
	if runtime.GOOS == "windows" {
		// PowerShell's `function git { jog git @args }` lives in the
		// profile; both the modern and Windows-PowerShell locations count.
		rcs = append(rcs,
			"Documents/PowerShell/Microsoft.PowerShell_profile.ps1",
			"Documents/WindowsPowerShell/Microsoft.PowerShell_profile.ps1")
	}
	for _, rc := range rcs {
		b, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rc)))
		if err == nil && strings.Contains(string(b), "jog git") {
			aliasFile = "~/" + rc
			break
		}
	}
	if aliasFile != "" {
		d.info("alias", "`git='jog git'` found in "+aliasFile+" (heuristic — check `type git` in your shell)")
	} else {
		d.info("alias", "no `jog git` alias found in shell rc files (heuristic)")
	}

	if !hooks && aliasFile == "" {
		d.warn("triggers", "neither the alias nor agent/editor hooks are wired — snapshots only happen when you run `jog` by hand (`jog agents install`, `jog editors install <name>`, or add the alias)")
	}
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
