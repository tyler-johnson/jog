package cli

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// claudeSkill ships inside the binary so installs work offline and the
// installed skill always matches the installed jog. The skill is the
// deliberate counterpart to the hook's one-line session notice — the
// notice says the net exists, the skill says how to use it.
//
//go:embed claude_skill.md
var claudeSkill []byte

func claudeSkillInstall(project bool) (string, bool, error) {
	path, err := claudeSkillPath(project)
	if err != nil {
		return "", false, err
	}
	existing, rerr := os.ReadFile(path)
	if rerr == nil && bytes.Equal(existing, claudeSkill) {
		return "already up to date — " + path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, claudeSkill, 0o644); err != nil {
		return "", false, err
	}
	if rerr == nil {
		return "updated — " + path, true, nil
	}
	return "installed — " + path, true, nil
}

// claudeSkillUninstall removes the skill — unless the file differs from
// what jog installs, which may mean the user's edits; jog won't delete
// those.
func claudeSkillUninstall(project bool) (string, bool, error) {
	path, err := claudeSkillPath(project)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "not installed — nothing to remove", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !bytes.Equal(b, claudeSkill) {
		return "", false, fmt.Errorf("%s differs from what jog installs — it may carry your edits, so jog won't remove it (delete the file yourself if you mean to)", path)
	}
	if err := os.Remove(path); err != nil {
		return "", false, err
	}
	os.Remove(filepath.Dir(path)) // best-effort: fails harmlessly if not empty
	return "removed — " + path, true, nil
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

// claudeSkillLocation reports where the skill is installed — user scope
// first, then the current repo — or "" when it isn't.
func claudeSkillLocation() string {
	if home, err := os.UserHomeDir(); err == nil && claudeSkillInstalled(home) {
		return "~/.claude/skills/jog/SKILL.md"
	}
	if root, err := projectRoot(); err == nil && fileExists(filepath.Join(root, ".claude", "skills", "jog", "SKILL.md")) {
		return ".claude/skills/jog/SKILL.md (project)"
	}
	return ""
}

// claudeSkillInstalled reports whether the skill file exists under home.
func claudeSkillInstalled(home string) bool {
	return fileExists(filepath.Join(home, ".claude", "skills", "jog", "SKILL.md"))
}
