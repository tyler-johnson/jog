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

// Skill handles `jog skill claude [--print]`: install (or refresh) the
// Claude Code skill that teaches agents the recovery workflow. The skill is
// the deliberate counterpart to the hook's one-line session notice — the
// notice says the net exists, the skill says how to use it.
func Skill(args []string) int {
	adapter, print := "", false
	for _, a := range args {
		switch a {
		case "--print":
			print = true
		default:
			if adapter != "" {
				fmt.Fprintln(os.Stderr, "jog: usage: jog skill claude [--print]")
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
		os.Stdout.Write(claudeSkill)
		return 0
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog: cannot resolve home directory:", err)
		return 1
	}
	path := filepath.Join(home, ".claude", "skills", "jog", "SKILL.md")
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, claudeSkill) {
		fmt.Printf("skill already installed and up to date: %s\n", path)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	if err := os.WriteFile(path, claudeSkill, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	fmt.Printf("skill installed: %s\n", path)
	fmt.Println("Claude Code picks it up automatically; new sessions can use it immediately.")
	return 0
}

// claudeSkillInstalled reports whether the skill file exists under home.
// Doctor uses it; content drift is not checked there — `jog skill claude`
// refreshes it either way.
func claudeSkillInstalled(home string) bool {
	_, err := os.Stat(filepath.Join(home, ".claude", "skills", "jog", "SKILL.md"))
	return err == nil
}
