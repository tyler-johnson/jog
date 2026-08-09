package agents

import (
	_ "embed"
)

// agentSkill ships inside the binary so installs work offline and the
// installed skill always matches the installed jog. SKILL.md is the open
// Agent Skills format, so one file serves every client — only the install
// path differs (each client's skillPath declaration). The skill is the
// deliberate counterpart to the hook's one-line session notice — the
// notice says the net exists, the skill says how to use it.
//
//go:embed skill.md
var agentSkill []byte
