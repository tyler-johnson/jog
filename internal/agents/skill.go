package agents

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
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

func installSkillFile(path string) (string, bool, error) {
	existing, rerr := os.ReadFile(path)
	if rerr == nil && bytes.Equal(existing, agentSkill) {
		return "already up to date — " + path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, agentSkill, 0o644); err != nil {
		return "", false, err
	}
	if rerr == nil {
		return "updated — " + path, true, nil
	}
	return "installed — " + path, true, nil
}

// removeSkillFile removes the skill — unless the file differs from what
// jog installs, which may mean the user's edits; jog won't delete those.
func removeSkillFile(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "not installed — nothing to remove", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !bytes.Equal(b, agentSkill) {
		return "", false, fmt.Errorf("%s differs from what jog installs — it may carry your edits, so jog won't remove it (delete the file yourself if you mean to)", path)
	}
	if err := os.Remove(path); err != nil {
		return "", false, err
	}
	os.Remove(filepath.Dir(path)) // best-effort: fails harmlessly if not empty
	return "removed — " + path, true, nil
}
