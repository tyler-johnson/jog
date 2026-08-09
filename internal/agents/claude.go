package agents

import "path/filepath"

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
			return repoPath(".claude", "settings.local.json")
		}
		return homePath(".claude", "settings.json")
	},
	hooksLocation: claudeHooksLocation,
	skillPath: func(project bool) (string, error) {
		if project {
			return repoPath(".claude", "skills", "jog", "SKILL.md")
		}
		return homePath(".claude", "skills", "jog", "SKILL.md")
	},
}

// claudeHooksLocation reports where `jog hook claude` is wired — user
// scope first, then the current repo's shared and personal settings (the
// shared file because the README documents wiring it by hand) — or ""
// when it isn't.
func claudeHooksLocation() string {
	if p, err := homePath(".claude", "settings.json"); err == nil && hooksFileWired(p, "claude") {
		return tildePath(p)
	}
	if root, err := projectRoot(); err == nil {
		for _, f := range []string{"settings.json", "settings.local.json"} {
			if hooksFileWired(filepath.Join(root, ".claude", f), "claude") {
				return ".claude/" + f + " (project)"
			}
		}
	}
	return ""
}
