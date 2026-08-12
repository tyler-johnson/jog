// Package agents implements `jog agents` — install, uninstall, and list
// jog's integrations for agent clients (Claude Code, Codex, …).
//
// Two surfaces per client: hooks (snapshot triggers wired into the
// client's settings; the wired command itself is always a `jog hook
// <client>` runtime entry, which lives in internal/cli) and a skill
// (recovery instructions the agent loads on demand). The mechanics are
// shared (wiring.go, skill.go); per-client facts are declarations in
// claude.go and codex.go — a new client is one more such file plus its
// entry in the clients registry.
package agents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// client declares one supported agent client: its hook events, where each
// surface lives per scope, and how wiring is located for list and doctor.
// Every func field except hooksPath and skillPath is optional — nil means
// the shared default (convention detection, Claude-style JSON wiring,
// location derived from the paths). Clients whose hook config is not the
// Claude-style JSON (Cursor's flat hooks.json, OpenCode's plugin file)
// override hooksInstall/hooksUninstall/hooksLocation wholesale.
type client struct {
	name           string
	title          string      // display name for list output
	detect         func() bool // nil: binary `name` on PATH, or ~/.<name> exists
	hookEvents     []hookEvent
	hookExtras     map[string]any // extra fields each written hook entry carries
	installNote    string         // client-specific coda for the wired message
	hooksPath      func(project bool) (string, error)
	hooksInstall   func(project bool) (string, bool, error)
	hooksUninstall func(project bool) (string, bool, error)
	hooksLocation  func() string // "" when not wired anywhere
	skillPath      func(project bool) (string, error)
}

var clients = []client{claudeAgent, codexAgent, copilotAgent, cursorAgent, geminiAgent, opencodeAgent}

// Status is one client's wiring, as doctor and `jog install` consume it.
type Status struct {
	Name          string
	Detected      bool   // plausibly on this machine
	HooksLocation string // "" when not wired
	SkillLocation string // "" when not installed
}

// Statuses reports every supported client's wiring.
func Statuses() []Status {
	out := make([]Status, len(clients))
	for i, c := range clients {
		out[i] = Status{Name: c.name, Detected: c.detected(),
			HooksLocation: c.whereHooks(), SkillLocation: c.skillLocation()}
	}
	return out
}

// detected reports whether the client is plausibly on this machine — by
// default its CLI on PATH, or its config directory in the home directory.
func (c client) detected() bool {
	if c.detect != nil {
		return c.detect()
	}
	if _, err := exec.LookPath(c.name); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && install.FileExists(filepath.Join(home, "."+c.name))
}

func (c client) installHooks(project bool) (string, bool, error) {
	if c.hooksInstall != nil {
		return c.hooksInstall(project)
	}
	path, err := c.hooksPath(project)
	if err != nil {
		return "", false, err
	}
	m, err := install.LoadJSON(path)
	if err != nil {
		return "", false, err
	}
	cmd := hookCommand(c.name)
	added, err := wireHooks(m, cmd, c.name, c.hookEvents, c.hookExtras)
	if err != nil {
		return "", false, err
	}
	if len(added) == 0 {
		return "already wired in " + path, false, nil
	}
	if err := install.WriteJSON(path, m); err != nil {
		return "", false, err
	}
	return "wired " + strings.Join(added, " and ") + " in " + path +
		" (command: " + cmd + c.installNote + ")", true, nil
}

func (c client) uninstallHooks(project bool) (string, bool, error) {
	if c.hooksUninstall != nil {
		return c.hooksUninstall(project)
	}
	path, err := c.hooksPath(project)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "no settings file at " + path + " — nothing to remove", false, nil
	}
	m, err := install.LoadJSON(path)
	if err != nil {
		return "", false, err
	}
	removed := unwireHooks(m, c.name)
	if removed == 0 {
		return "no jog hooks in " + path + " — nothing to remove", false, nil
	}
	if err := install.WriteJSON(path, m); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("removed %d jog hook(s) from %s — everything else untouched", removed, path), true, nil
}

// whereHooks reports where the client's hooks are wired — user scope
// first, then the current repo — or "" when they aren't.
func (c client) whereHooks() string {
	if c.hooksLocation != nil {
		return c.hooksLocation()
	}
	if p, err := c.hooksPath(false); err == nil && hooksFileWired(p, c.name) {
		return install.TildePath(p)
	}
	if p, err := c.hooksPath(true); err == nil && hooksFileWired(p, c.name) {
		return install.ProjectDisplay(p)
	}
	return ""
}

func (c client) installSkill(project bool) (string, bool, error) {
	path, err := c.skillPath(project)
	if err != nil {
		return "", false, err
	}
	return install.ManagedFile(path, agentSkill)
}

func (c client) uninstallSkill(project bool) (string, bool, error) {
	path, err := c.skillPath(project)
	if err != nil {
		return "", false, err
	}
	return install.RemoveManagedFile(path, agentSkill)
}

// skillLocation reports where the skill is installed — user scope first,
// then the current repo — or "" when it isn't.
func (c client) skillLocation() string {
	if p, err := c.skillPath(false); err == nil && install.FileExists(p) {
		return install.TildePath(p)
	}
	if p, err := c.skillPath(true); err == nil && install.FileExists(p) {
		return install.ProjectDisplay(p)
	}
	return ""
}

// Wiring is one surface's install or uninstall outcome for one client,
// returned without printing — the `jog install`/`jog uninstall` wizard
// folds these into its own summary.
type Wiring struct {
	Client  string
	Surface string // "hooks" or "skill"
	Where   string // display location; "" on error
	Changed bool
	Err     error
}

// InstallClients wires hooks + skill at user scope for the named
// clients, quietly. Same mechanics as `jog agents install`; only the
// reporting differs.
func InstallClients(names []string) []Wiring {
	var out []Wiring
	for _, name := range names {
		for _, c := range clients {
			if c.name != name {
				continue
			}
			_, hooksDid, hooksErr := c.installHooks(false)
			out = append(out, Wiring{Client: c.name, Surface: "hooks",
				Where: c.whereHooks(), Changed: hooksDid, Err: hooksErr})
			_, skillDid, skillErr := c.installSkill(false)
			out = append(out, Wiring{Client: c.name, Surface: "skill",
				Where: c.skillLocation(), Changed: skillDid, Err: skillErr})
		}
	}
	return out
}

// UninstallClients removes hooks + skill at user scope for the named
// clients, quietly. Same mechanics as `jog agents uninstall`; only the
// reporting differs. Locations are captured before removal, so the
// summary can say where each surface came out of.
func UninstallClients(names []string) []Wiring {
	var out []Wiring
	for _, name := range names {
		for _, c := range clients {
			if c.name != name {
				continue
			}
			hooksWhere := c.whereHooks()
			_, hooksDid, hooksErr := c.uninstallHooks(false)
			out = append(out, Wiring{Client: c.name, Surface: "hooks",
				Where: hooksWhere, Changed: hooksDid, Err: hooksErr})
			skillWhere := c.skillLocation()
			_, skillDid, skillErr := c.uninstallSkill(false)
			out = append(out, Wiring{Client: c.name, Surface: "skill",
				Where: skillWhere, Changed: skillDid, Err: skillErr})
		}
	}
	return out
}

func clientNames() string {
	names := make([]string, len(clients))
	for i, c := range clients {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}

const usage = "jog: usage: jog agents install|uninstall|list [hooks|skill] [client…] [--project]"

// Run is the `jog agents` command: parse the action, surfaces, client
// names, and scope, then dispatch. install covers both surfaces for
// every client detected on this machine and skips the rest; naming
// clients overrides detection, naming a surface narrows the work.
//
// Default scope is the home directory: hooks exit in milliseconds outside
// git repos, so user-level wiring covering every repo is the right
// default. --project scopes to the current repo; each client's file
// declares where that lands.
func Run(args []string) int {
	action := ""
	hooks, skill := false, false
	project := false
	var names []string
	for _, a := range args {
		switch a {
		case "install", "uninstall", "list":
			if action != "" {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
			action = a
		case "hooks", "hook":
			hooks = true
		case "skill", "skills":
			skill = true
		case "--project":
			project = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
			names = append(names, a)
		}
	}
	if action == "" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if !hooks && !skill {
		hooks, skill = true, true
	}
	var targets []client
	if len(names) == 0 {
		targets = clients
	} else {
		for _, want := range names {
			found := false
			for _, c := range clients {
				if c.name == want {
					targets = append(targets, c)
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "jog: unknown agent client %q (supported: %s)\n", want, clientNames())
				return 2
			}
		}
	}

	switch action {
	case "list":
		return list(targets, hooks, skill)
	case "install":
		return installClients(targets, len(names) > 0, hooks, skill, project)
	default:
		return uninstallClients(targets, hooks, skill, project)
	}
}

// Output vocabulary lives in internal/install, shared with `jog editors`.
func clientHeader(c client, i int) { install.Header(c.name, c.title, i) }

var surfaceRow = install.Row

// list groups output per client: a title line, then one line per
// requested surface. Clients with no binary, no config, and no jog
// integration collapse to a single quiet line — their absence is
// unremarkable.
func list(targets []client, hooks, skill bool) int {
	surface := func(name, loc string) {
		if loc != "" {
			fmt.Printf("  %-6s %s  %s\n", name, install.StyleGood.Render("✓ installed"), loc)
		} else {
			fmt.Printf("  %-6s %s\n", name, install.StyleDim.Render("· not installed"))
		}
	}
	for i, c := range targets {
		clientHeader(c, i)
		hooksLoc, skillLoc := c.whereHooks(), c.skillLocation()
		if !c.detected() && hooksLoc == "" && skillLoc == "" {
			fmt.Println(install.StyleDim.Render("  not found on this machine — `jog agents install " + c.name + "` forces it"))
			continue
		}
		if hooks {
			surface("hooks", hooksLoc)
		}
		if skill {
			surface("skill", skillLoc)
		}
	}
	return 0
}

// installClients wires the requested surfaces. Detection gates only the
// no-names path: a client the user names is installed for regardless —
// they know their machine better than a heuristic does.
func installClients(targets []client, explicit, hooks, skill, project bool) int {
	code := 0
	changed := false
	for i, c := range targets {
		clientHeader(c, i)
		if !explicit && !c.detected() {
			fmt.Println(install.StyleDim.Render("  not found — skipped (`jog agents install " + c.name + "` to force)"))
			continue
		}
		if hooks {
			msg, did, err := c.installHooks(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s hooks: %v\n", c.name, err)
				code = 1
			} else {
				surfaceRow("hooks", msg, did)
				changed = changed || did
			}
		}
		if skill {
			msg, did, err := c.installSkill(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s skill: %v\n", c.name, err)
				code = 1
			} else {
				surfaceRow("skill", msg, did)
				changed = changed || did
			}
		}
	}
	if changed {
		fmt.Println()
		fmt.Println("`jog agents uninstall` removes them; `jog doctor` verifies the wiring.")
		if project {
			fmt.Println("(project scope: a committed hook file only works for teammates who")
			fmt.Println(" also have jog installed; some clients keep hooks in a personal,")
			fmt.Println(" uncommitted file instead — the paths above show which.)")
		}
	}
	return code
}

func uninstallClients(targets []client, hooks, skill, project bool) int {
	code := 0
	for i, c := range targets {
		clientHeader(c, i)
		if hooks {
			msg, did, err := c.uninstallHooks(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s hooks: %v\n", c.name, err)
				code = 1
			} else {
				surfaceRow("hooks", msg, did)
			}
		}
		if skill {
			msg, did, err := c.uninstallSkill(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s skill: %v\n", c.name, err)
				code = 1
			} else {
				surfaceRow("skill", msg, did)
			}
		}
	}
	return code
}
