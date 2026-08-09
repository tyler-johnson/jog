package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Agents is `jog agents install|uninstall|list [hooks|skill] [client…]
// [--project]` (singular `jog agent` is an alias): one command for every
// agent-client integration. Two surfaces per client — hooks (snapshot
// triggers wired into the client's settings; the wired command itself is
// always `jog hook claude`-style runtime entries) and a skill (recovery
// instructions the agent loads on demand). install covers both surfaces
// for every client detected on this machine and skips the rest; naming
// clients overrides detection, naming a surface narrows the work.
//
// Default scope is the home directory: hooks exit in milliseconds outside
// git repos, so user-level wiring covering every repo is the right
// default. --project scopes to the current repo, with one deliberate
// asymmetry: hooks go to the personal .claude/settings.local.json (a hook
// command committed to the shared file would fire and fail on machines
// without jog), while the skill goes to the committable .claude/skills/.

type agentClient struct {
	name   string
	detect func() bool
}

var agentClients = []agentClient{
	{name: "claude", detect: claudeDetected},
}

func claudeDetected() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && fileExists(filepath.Join(home, ".claude"))
}

func clientNames() string {
	names := make([]string, len(agentClients))
	for i, c := range agentClients {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}

func knownClient(name string) bool {
	for _, c := range agentClients {
		if c.name == name {
			return true
		}
	}
	return false
}

const agentsUsage = "jog: usage: jog agents install|uninstall|list [hooks|skill] [client…] [--project]"

func Agents(args []string) int {
	action := ""
	hooks, skill := false, false
	project := false
	var clients []string
	for _, a := range args {
		switch a {
		case "install", "uninstall", "list":
			if action != "" {
				fmt.Fprintln(os.Stderr, agentsUsage)
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
				fmt.Fprintln(os.Stderr, agentsUsage)
				return 2
			}
			clients = append(clients, a)
		}
	}
	if action == "" {
		fmt.Fprintln(os.Stderr, agentsUsage)
		return 2
	}
	if !hooks && !skill {
		hooks, skill = true, true
	}
	for _, c := range clients {
		if !knownClient(c) {
			fmt.Fprintf(os.Stderr, "jog: unknown agent client %q (supported: %s)\n", c, clientNames())
			return 2
		}
	}
	targets := agentClients
	if len(clients) > 0 {
		targets = nil
		for _, c := range agentClients {
			for _, want := range clients {
				if c.name == want {
					targets = append(targets, c)
					break
				}
			}
		}
	}

	switch action {
	case "list":
		return agentsList(targets, hooks, skill)
	case "install":
		return agentsInstall(targets, len(clients) > 0, hooks, skill, project)
	default:
		return agentsUninstall(targets, hooks, skill, project)
	}
}

func row(client, surface, msg string) {
	fmt.Printf("  %-8s %-6s %s\n", client, surface, msg)
}

func agentsList(targets []agentClient, hooks, skill bool) int {
	for _, c := range targets {
		found := c.detect()
		if hooks {
			switch loc := clientHooksLocation(c.name); {
			case loc != "":
				row(c.name, "hooks", "installed — "+loc)
			case !found:
				row(c.name, "hooks", "client not found — install skips it")
			default:
				row(c.name, "hooks", "not installed")
			}
		}
		if skill {
			switch loc := clientSkillLocation(c.name); {
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

// agentsInstall wires the requested surfaces. Detection gates only the
// no-names path: a client the user names is installed for regardless —
// they know their machine better than a heuristic does.
func agentsInstall(targets []agentClient, explicit, hooks, skill, project bool) int {
	code := 0
	changed := false
	for _, c := range targets {
		if !explicit && !c.detect() {
			row(c.name, "", "not found — skipped (`jog agents install "+c.name+"` to force)")
			continue
		}
		if hooks {
			msg, did, err := clientHooksInstall(c.name, project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s hooks: %v\n", c.name, err)
				code = 1
			} else {
				row(c.name, "hooks", msg)
				changed = changed || did
			}
		}
		if skill {
			msg, did, err := clientSkillInstall(c.name, project)
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
			fmt.Println("(project scope: settings.local.json is personal — a committed hook command")
			fmt.Println(" would break for teammates without jog; the skill in .claude/skills/ is")
			fmt.Println(" safe to commit.)")
		}
	}
	return code
}

func agentsUninstall(targets []agentClient, hooks, skill, project bool) int {
	code := 0
	for _, c := range targets {
		if hooks {
			msg, _, err := clientHooksUninstall(c.name, project)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s hooks: %v\n", c.name, err)
				code = 1
			} else {
				row(c.name, "hooks", msg)
			}
		}
		if skill {
			msg, _, err := clientSkillUninstall(c.name, project)
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

// Per-client dispatch. Only claude exists today; a new client adds its
// registry entry and these four implementations.

func clientHooksInstall(name string, project bool) (string, bool, error) {
	switch name {
	case "claude":
		return claudeHooksInstall(project)
	}
	return "", false, fmt.Errorf("no hooks implementation for %q", name)
}

func clientHooksUninstall(name string, project bool) (string, bool, error) {
	switch name {
	case "claude":
		return claudeHooksUninstall(project)
	}
	return "", false, fmt.Errorf("no hooks implementation for %q", name)
}

func clientSkillInstall(name string, project bool) (string, bool, error) {
	switch name {
	case "claude":
		return claudeSkillInstall(project)
	}
	return "", false, fmt.Errorf("no skill implementation for %q", name)
}

func clientSkillUninstall(name string, project bool) (string, bool, error) {
	switch name {
	case "claude":
		return claudeSkillUninstall(project)
	}
	return "", false, fmt.Errorf("no skill implementation for %q", name)
}

func clientHooksLocation(name string) string {
	switch name {
	case "claude":
		return claudeHooksLocation()
	}
	return ""
}

func clientSkillLocation(name string) string {
	switch name {
	case "claude":
		return claudeSkillLocation()
	}
	return ""
}
