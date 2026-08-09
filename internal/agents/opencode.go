package agents

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
)

// OpenCode: no declarative hook config — lifecycle hooks are JavaScript
// plugins, so the hooks surface here is a jog-owned plugin file that
// pipes chat.message and tool.execute.before events to `jog hook
// opencode`. The plugin ships inside the binary (like the skill) and is
// managed with the same edited-file refusal on uninstall.
//
// OpenCode breaks the `~/.<name>` convention — its config is XDG-style
// under ~/.config/opencode — hence the detect override. It also reads
// skills from ~/.claude/skills and ~/.agents/skills, so other clients'
// installs already cover it; the native path keeps this one surgical.
//
//go:embed opencode_plugin.js
var opencodePlugin []byte

var opencodeAgent = client{
	name:  "opencode",
	title: "OpenCode",
	detect: func() bool {
		if _, err := exec.LookPath("opencode"); err == nil {
			return true
		}
		home, err := os.UserHomeDir()
		return err == nil && fileExists(filepath.Join(home, ".config", "opencode"))
	},
	hooksPath:      opencodeHooksPath,
	hooksInstall:   opencodeHooksInstall,
	hooksUninstall: opencodeHooksUninstall,
	hooksLocation:  opencodeHooksLocation,
	skillPath: func(project bool) (string, error) {
		if project {
			return repoPath(".opencode", "skills", "jog", "SKILL.md")
		}
		return homePath(".config", "opencode", "skills", "jog", "SKILL.md")
	},
}

func opencodeHooksPath(project bool) (string, error) {
	if project {
		return repoPath(".opencode", "plugins", "jog.js")
	}
	return homePath(".config", "opencode", "plugins", "jog.js")
}

func opencodeHooksInstall(project bool) (string, bool, error) {
	path, err := opencodeHooksPath(project)
	if err != nil {
		return "", false, err
	}
	msg, did, err := installManagedFile(path, opencodePlugin)
	if err != nil {
		return "", false, err
	}
	if did {
		msg += " (a plugin running `jog hook opencode`)"
	}
	return msg, did, nil
}

func opencodeHooksUninstall(project bool) (string, bool, error) {
	path, err := opencodeHooksPath(project)
	if err != nil {
		return "", false, err
	}
	return removeManagedFile(path, opencodePlugin)
}

func opencodeHooksLocation() string {
	if p, err := opencodeHooksPath(false); err == nil && fileExists(p) {
		return tildePath(p)
	}
	if p, err := opencodeHooksPath(true); err == nil && fileExists(p) {
		return projectPathDisplay(p)
	}
	return ""
}
