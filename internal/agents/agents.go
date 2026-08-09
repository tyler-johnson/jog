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
)

// client declares one supported agent client: its hook events, where each
// surface lives per scope, and how wiring is located for list and doctor.
type client struct {
	name          string
	hookEvents    []hookEvent
	installNote   string // client-specific coda for the wired message
	hooksPath     func(project bool) (string, error)
	hooksLocation func() string // "" when not wired anywhere
	skillPath     func(project bool) (string, error)
}

var clients = []client{claudeAgent, codexAgent}

// Status is one client's wiring, as doctor reports it.
type Status struct {
	Name          string
	HooksLocation string // "" when not wired
	SkillLocation string // "" when not installed
}

// Statuses reports every supported client's wiring.
func Statuses() []Status {
	out := make([]Status, len(clients))
	for i, c := range clients {
		out[i] = Status{Name: c.name, HooksLocation: c.hooksLocation(), SkillLocation: c.skillLocation()}
	}
	return out
}

// detected reports whether the client is plausibly on this machine: its
// CLI on PATH, or its config directory in the home directory. Both
// current clients follow the `<name>` / `~/.<name>` convention; a future
// client that doesn't will need this to become a per-client field.
func (c client) detected() bool {
	if _, err := exec.LookPath(c.name); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && fileExists(filepath.Join(home, "."+c.name))
}

func (c client) hooksInstall(project bool) (string, bool, error) {
	path, err := c.hooksPath(project)
	if err != nil {
		return "", false, err
	}
	m, err := loadSettings(path)
	if err != nil {
		return "", false, err
	}
	cmd := hookCommand(c.name)
	added, err := wireHooks(m, cmd, c.name, c.hookEvents)
	if err != nil {
		return "", false, err
	}
	if len(added) == 0 {
		return "already wired in " + path, false, nil
	}
	if err := writeSettings(path, m); err != nil {
		return "", false, err
	}
	return "wired " + strings.Join(added, " and ") + " in " + path +
		" (command: " + cmd + c.installNote + ")", true, nil
}

func (c client) hooksUninstall(project bool) (string, bool, error) {
	path, err := c.hooksPath(project)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "no settings file at " + path + " — nothing to remove", false, nil
	}
	m, err := loadSettings(path)
	if err != nil {
		return "", false, err
	}
	removed := unwireHooks(m, c.name)
	if removed == 0 {
		return "no jog hooks in " + path + " — nothing to remove", false, nil
	}
	if err := writeSettings(path, m); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("removed %d jog hook(s) from %s — everything else untouched", removed, path), true, nil
}

func (c client) skillInstall(project bool) (string, bool, error) {
	path, err := c.skillPath(project)
	if err != nil {
		return "", false, err
	}
	return installSkillFile(path)
}

func (c client) skillUninstall(project bool) (string, bool, error) {
	path, err := c.skillPath(project)
	if err != nil {
		return "", false, err
	}
	return removeSkillFile(path)
}

// skillLocation reports where the skill is installed — user scope first,
// then the current repo — or "" when it isn't.
func (c client) skillLocation() string {
	if p, err := c.skillPath(false); err == nil && fileExists(p) {
		return tildePath(p)
	}
	if p, err := c.skillPath(true); err == nil && fileExists(p) {
		return projectPathDisplay(p)
	}
	return ""
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
		return install(targets, len(names) > 0, hooks, skill, project)
	default:
		return uninstall(targets, hooks, skill, project)
	}
}

func row(client, surface, msg string) {
	fmt.Printf("  %-8s %-6s %s\n", client, surface, msg)
}

func list(targets []client, hooks, skill bool) int {
	for _, c := range targets {
		found := c.detected()
		if hooks {
			switch loc := c.hooksLocation(); {
			case loc != "":
				row(c.name, "hooks", "installed — "+loc)
			case !found:
				row(c.name, "hooks", "client not found — install skips it")
			default:
				row(c.name, "hooks", "not installed")
			}
		}
		if skill {
			switch loc := c.skillLocation(); {
			case loc != "":
				row(c.name, "skill", "installed — "+loc)
			case !found:
				row(c.name, "skill", "client not found — install skips it")
			default:
				row(c.name, "skill", "not installed")
			}
		}
	}
	return 0
}

// install wires the requested surfaces. Detection gates only the
// no-names path: a client the user names is installed for regardless —
// they know their machine better than a heuristic does.
func install(targets []client, explicit, hooks, skill, project bool) int {
	code := 0
	changed := false
	for _, c := range targets {
		if !explicit && !c.detected() {
			row(c.name, "", "not found — skipped (`jog agents install "+c.name+"` to force)")
			continue
		}
		if hooks {
			msg, did, err := c.hooksInstall(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s hooks: %v\n", c.name, err)
				code = 1
			} else {
				row(c.name, "hooks", msg)
				changed = changed || did
			}
		}
		if skill {
			msg, did, err := c.skillInstall(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s skill: %v\n", c.name, err)
				code = 1
			} else {
				row(c.name, "skill", msg)
				changed = changed || did
			}
		}
	}
	if changed {
		fmt.Println("`jog agents uninstall` removes them; `jog doctor` verifies the wiring.")
		if project {
			fmt.Println("(project scope: Claude settings.local.json is personal; Codex's")
			fmt.Println(" .codex/hooks.json and both clients' skill directories are committable.")
			fmt.Println(" Codex users review project hooks with /hooks before they run.)")
		}
	}
	return code
}

func uninstall(targets []client, hooks, skill, project bool) int {
	code := 0
	for _, c := range targets {
		if hooks {
			msg, _, err := c.hooksUninstall(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s hooks: %v\n", c.name, err)
				code = 1
			} else {
				row(c.name, "hooks", msg)
			}
		}
		if skill {
			msg, _, err := c.skillUninstall(project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s skill: %v\n", c.name, err)
				code = 1
			} else {
				row(c.name, "skill", msg)
			}
		}
	}
	return code
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
