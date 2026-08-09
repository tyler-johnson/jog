package cli

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// claudeSkill ships inside the binary so `jog skill claude` works offline
// and the installed skill always matches the installed jog.
//
//go:embed claude_skill.md
var claudeSkill []byte

const skillUsage = "jog: usage: jog skill claude install|uninstall [--project], or jog skill claude --print"

// Skill handles `jog skill claude`: manage the Claude Code skill that
// teaches agents the recovery workflow — the deliberate counterpart to the
// hook's one-line session notice (the notice says the net exists, the
// skill says how to use it). Default scope is the home directory; --project
// installs into the repo's .claude/skills/, which is shared and safe to
// commit — unlike a hook command, the skill is inert instructions and
// breaks nothing for teammates without jog.
func Skill(args []string) int {
	adapter, action, project, print := "", "", false, false
	for _, a := range args {
		switch a {
		case "install", "uninstall":
			if action != "" {
				fmt.Fprintln(os.Stderr, skillUsage)
				return 2
			}
			action = a
		case "--project":
			project = true
		case "--print":
			print = true
		default:
			if adapter != "" {
				fmt.Fprintln(os.Stderr, skillUsage)
				return 2
			}
			adapter = a
		}
	}
	if adapter != "claude" {
		fmt.Fprintln(os.Stderr, "jog: unknown skill adapter (want: jog skill claude)")
		return 2
	}
	if print {
		if action != "" || project {
			fmt.Fprintln(os.Stderr, skillUsage)
			return 2
		}
		os.Stdout.Write(claudeSkill)
		return 0
	}
	if action == "" {
		fmt.Fprintln(os.Stderr, skillUsage)
		return 2
	}

	path, err := claudeSkillPath(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	if action == "install" {
		return skillInstall(path, project)
	}
	return skillUninstall(path)
}

func skillInstall(path string, project bool) int {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, claudeSkill) {
		fmt.Printf("skill already installed and up to date: %s\n", path)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	if werr := os.WriteFile(path, claudeSkill, 0o644); werr != nil {
		fmt.Fprintln(os.Stderr, "jog:", werr)
		return 1
	}
	if err == nil {
		fmt.Printf("skill updated: %s\n", path)
	} else {
		fmt.Printf("skill installed: %s\n", path)
	}
	if project {
		fmt.Println("  (project-scoped: commit it and teammates' agents get it too)")
	}
	fmt.Println("Claude Code picks it up automatically; new sessions can use it immediately.")
	fmt.Println("`jog skill claude uninstall` removes it.")
	return 0
}

func skillUninstall(path string) int {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Printf("skill not installed at %s — nothing to remove\n", path)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	if !bytes.Equal(b, claudeSkill) {
		fmt.Fprintf(os.Stderr, "jog: %s differs from what jog installs — it may carry your edits, so jog won't remove it (delete the file yourself if you mean to)\n", path)
		return 1
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	os.Remove(filepath.Dir(path)) // best-effort: fails harmlessly if not empty
	fmt.Printf("skill removed: %s\n", path)
	return 0
}

// claudeSkillPath resolves the skill file for the scope, mirroring
// claudeSettingsPath's project-root anchoring.
func claudeSkillPath(project bool) (string, error) {
	if !project {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory: %w", err)
		}
		return filepath.Join(home, ".claude", "skills", "jog", "SKILL.md"), nil
	}
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".claude", "skills", "jog", "SKILL.md"), nil
}

// claudeSkillInstalled reports whether the skill file exists under home.
// Doctor uses it; content drift is not checked there — `jog skill claude
// install` refreshes it either way.
func claudeSkillInstalled(home string) bool {
	_, err := os.Stat(filepath.Join(home, ".claude", "skills", "jog", "SKILL.md"))
	return err == nil
}
