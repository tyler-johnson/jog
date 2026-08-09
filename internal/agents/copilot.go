package agents

import "github.com/tyler-johnson/jog/internal/install"

// GitHub Copilot CLI: hooks live under the "hooks" key of its settings
// files, using its documented Claude-compatible mode — PascalCase event
// names get Claude-style matcher semantics (native tool names map to
// Bash/Edit/Write) and Claude-style snake_case stdin payloads, so the
// shared wiring and the shared payload parsing both apply unchanged.
//
// Copilot's preToolUse is fail-closed: a hook that crashes or exits
// non-zero DENIES the tool call. `jog hook copilot` exiting 0 always (the
// iron rule every adapter already obeys) is what keeps that safe.
//
// Project scope mirrors Claude's asymmetry: hooks go to the personal
// .github/copilot/settings.local.json, the skill to the committable
// .github/skills/.
var copilotAgent = client{
	name:  "copilot",
	title: "Copilot CLI",
	hookEvents: []hookEvent{
		{"PreToolUse", "Bash|Edit|Write"},
		{"UserPromptSubmit", ""},
	},
	hooksPath: func(project bool) (string, error) {
		if project {
			return install.RepoPath(".github", "copilot", "settings.local.json")
		}
		return install.HomePath(".copilot", "settings.json")
	},
	skillPath: func(project bool) (string, error) {
		if project {
			return install.RepoPath(".github", "skills", "jog", "SKILL.md")
		}
		return install.HomePath(".copilot", "skills", "jog", "SKILL.md")
	},
}
