package agents

// Gemini CLI: hooks live under the "hooks" key of its settings.json, in
// the same event → matcher group → command JSON shape as Claude's, with
// one addition — each entry carries a "name", which Gemini's hook-trust
// fingerprinting requires (project-scope hooks prompt for trust on first
// run; manage with /hooks panel). Its prompt boundary is BeforeAgent and
// its tool boundary BeforeTool, with regex matchers over Gemini's
// snake_case tool names.
var geminiAgent = client{
	name:  "gemini",
	title: "Gemini CLI",
	hookEvents: []hookEvent{
		{"BeforeAgent", "*"},
		{"BeforeTool", "write_file|replace|run_shell_command"},
	},
	hookExtras: map[string]any{"name": "jog"},
	hooksPath: func(project bool) (string, error) {
		if project {
			return repoPath(".gemini", "settings.json")
		}
		return homePath(".gemini", "settings.json")
	},
	skillPath: func(project bool) (string, error) {
		if project {
			return repoPath(".gemini", "skills", "jog", "SKILL.md")
		}
		return homePath(".gemini", "skills", "jog", "SKILL.md")
	},
}
