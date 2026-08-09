package agents

import (
	"path/filepath"

	"github.com/tyler-johnson/jog/internal/install"
)

// Claude Code: hooks in JSON settings files, skill under .claude/skills/.
//
// Project scope carries one deliberate asymmetry: hooks go to the personal
// .claude/settings.local.json — a hook command committed to the shared
// settings.json would fire and fail on machines without jog — while the
// skill goes to the committable .claude/skills/, where it is inert without
// jog installed.
var claudeAgent = client{
	name:  "claude",
	title: "Claude Code",
	hookEvents: []hookEvent{
		{"PreToolUse", "Bash|Edit|Write|NotebookEdit"},
		{"UserPromptSubmit", ""},
	},
	hooksPath: func(project bool) (string, error) {
		if project {
			return install.RepoPath(".claude", "settings.local.json")
		}
		return install.HomePath(".claude", "settings.json")
	},
	hooksLocation: claudeHooksLocation,
	skillPath: func(project bool) (string, error) {
		if project {
			return install.RepoPath(".claude", "skills", "jog", "SKILL.md")
		}
		return install.HomePath(".claude", "skills", "jog", "SKILL.md")
	},
}

// claudeHooksLocation reports where `jog hook claude` is wired — user
// scope first, then the current repo's shared and personal settings (the
// shared file because the README documents wiring it by hand) — or ""
// when it isn't.
func claudeHooksLocation() string {
	if p, err := install.HomePath(".claude", "settings.json"); err == nil && hooksFileWired(p, "claude") {
		return install.TildePath(p)
	}
	if root, err := install.ProjectRoot(); err == nil {
		for _, f := range []string{"settings.json", "settings.local.json"} {
			if hooksFileWired(filepath.Join(root, ".claude", f), "claude") {
				return ".claude/" + f + " (project)"
			}
		}
	}
	return ""
}
