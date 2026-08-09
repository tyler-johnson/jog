package agents

// Codex: hooks in .codex/hooks.json (same event → matcher group → command
// JSON shape as Claude's), skill under the cross-agent .agents/skills/
// standard directory. Codex requires non-managed hooks to be trusted via
// /hooks before they run — hence the installNote.
//
// Codex reports file edits under the canonical tool name apply_patch, but
// documents Edit and Write as matcher aliases for it. Both hook files —
// user and project scope — are committable; each Codex user reviews
// project hooks with /hooks.
var codexAgent = client{
	name: "codex",
	hookEvents: []hookEvent{
		{"PreToolUse", "Bash|Edit|Write"},
		{"UserPromptSubmit", ""},
	},
	installNote: "; review with /hooks",
	hooksPath: func(project bool) (string, error) {
		if project {
			return repoPath(".codex", "hooks.json")
		}
		return homePath(".codex", "hooks.json")
	},
	skillPath: func(project bool) (string, error) {
		if project {
			return repoPath(".agents", "skills", "jog", "SKILL.md")
		}
		return homePath(".agents", "skills", "jog", "SKILL.md")
	},
}
