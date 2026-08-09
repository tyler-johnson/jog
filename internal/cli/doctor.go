package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	d.checkTriggers(repo)

	if d.findings == 0 {
		fmt.Println("\nno findings — the net is under you")
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

func (d *doctor) ok(what, detail string)   { fmt.Printf("  ok    %-13s %s\n", what, detail) }
func (d *doctor) info(what, detail string) { fmt.Printf("  info  %-13s %s\n", what, detail) }
func (d *doctor) warn(what, detail string) {
	d.findings++
	fmt.Printf("  WARN  %-13s %s\n", what, detail)
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
		d.info("max file size", out+" bytes (jog.maxFileSize)")
	} else {
		d.info("max file size", "50 MiB (default)")
	}
}

// checkTriggers verifies something is actually invoking the engine: the
// Claude hooks (deterministic — settings.json either wires `jog hook
// claude` or it doesn't) and the shell alias (heuristic — an rc-file grep
// can't see a live shell's aliases, so it is reported, never asserted).
// Neither wired at all is the real finding: a silent engine feels safe
// while capturing nothing.
func (d *doctor) checkTriggers(repo *gitx.Repo) {
	home, err := os.UserHomeDir()
	if err != nil {
		d.info("triggers", "cannot resolve home directory; wiring checks skipped")
		return
	}

	// User scope first, then the project's shared and personal settings —
	// wherever the user chose to wire, doctor should find it.
	settings := []struct{ path, label string }{
		{filepath.Join(home, ".claude", "settings.json"), "~/.claude/settings.json"},
	}
	var top string
	if repo != nil {
		if t, err := repo.Run("rev-parse", "--show-toplevel"); err == nil {
			top = t
			settings = append(settings,
				struct{ path, label string }{filepath.Join(top, ".claude", "settings.json"), ".claude/settings.json"},
				struct{ path, label string }{filepath.Join(top, ".claude", "settings.local.json"), ".claude/settings.local.json"},
			)
		}
	}
	hooks := false
	for _, s := range settings {
		if claudeHooksWired(s.path) {
			d.ok("claude hooks", "`jog hook claude` wired in "+s.label)
			hooks = true
			break
		}
	}
	if !hooks {
		d.info("claude hooks", "not wired (optional — `jog hook claude install`)")
	}

	switch {
	case claudeSkillInstalled(home):
		d.ok("claude skill", "installed at ~/.claude/skills/jog/SKILL.md")
	case top != "" && fileExists(filepath.Join(top, ".claude", "skills", "jog", "SKILL.md")):
		d.ok("claude skill", "installed at .claude/skills/jog/SKILL.md (project)")
	default:
		d.info("claude skill", "not installed (optional — `jog skill claude install`)")
	}

	aliasFile := ""
	for _, rc := range []string{".bashrc", ".zshrc", ".config/fish/config.fish", ".profile"} {
		b, err := os.ReadFile(filepath.Join(home, rc))
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
		d.warn("triggers", "neither the alias nor Claude hooks are wired — snapshots only happen when you run `jog` by hand (`jog hook claude install`, or add the alias)")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// claudeHooksWired reports whether any hook command in the settings file
// invokes `jog hook claude`. Parsed defensively: the file is external
// surface, and a malformed one simply reads as "not wired".
func claudeHooksWired(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	for _, matchers := range s.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if strings.Contains(h.Command, "jog hook claude") {
					return true
				}
			}
		}
	}
	return false
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
