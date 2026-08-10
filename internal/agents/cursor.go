package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// Cursor: hooks.json in Cursor's own flat schema — {"version": 1,
// "hooks": {"<event>": [{"command": "..."}]}} — not the Claude-style
// nested shape, so wiring is implemented here rather than shared. The
// user-scope file is read by both the Cursor IDE and its agent CLI.
//
// Events: beforeShellExecution (before every shell command),
// afterFileEdit (right after an edit — Cursor has no before-edit event
// in its CLI, and an immediately-after snapshot protects the edit just
// as well), and beforeSubmitPrompt. The first and last are permission
// events: `jog hook cursor` answers them with an explicit allow, and
// Cursor fails open on empty output, so jog can never block the action.
var cursorAgent = client{
	name:           "cursor",
	title:          "Cursor",
	hooksPath:      cursorHooksPath,
	hooksInstall:   cursorHooksInstall,
	hooksUninstall: cursorHooksUninstall,
	hooksLocation:  cursorHooksLocation,
	skillPath: func(project bool) (string, error) {
		if project {
			return install.RepoPath(".cursor", "skills", "jog", "SKILL.md")
		}
		return install.HomePath(".cursor", "skills", "jog", "SKILL.md")
	},
}

var cursorHookEvents = []string{"beforeShellExecution", "afterFileEdit", "beforeSubmitPrompt"}

func cursorHooksPath(project bool) (string, error) {
	if project {
		return install.RepoPath(".cursor", "hooks.json")
	}
	return install.HomePath(".cursor", "hooks.json")
}

func cursorHooksInstall(project bool) (string, bool, error) {
	path, err := cursorHooksPath(project)
	if err != nil {
		return "", false, err
	}
	m, err := install.LoadJSON(path)
	if err != nil {
		return "", false, err
	}
	if _, ok := m["version"]; !ok {
		m["version"] = 1
	}
	var hooks map[string]any
	switch h := m["hooks"].(type) {
	case nil:
		hooks = map[string]any{}
		m["hooks"] = hooks
	case map[string]any:
		hooks = h
	default:
		return "", false, fmt.Errorf(`the hooks file's "hooks" field has an unexpected shape — wire the hooks by hand`)
	}

	cmd := hookCommand("cursor")
	var added []string
	for _, ev := range cursorHookEvents {
		entries, ok := hooks[ev].([]any)
		if !ok && hooks[ev] != nil {
			return "", false, fmt.Errorf("the hooks file's %q entries have an unexpected shape — wire the hooks by hand", ev)
		}
		if cursorEntriesInvokeJog(entries, cmd) {
			continue
		}
		hooks[ev] = append(entries, map[string]any{"command": cmd})
		added = append(added, ev)
	}
	if len(added) == 0 {
		return "already wired in " + path, false, nil
	}
	if err := install.WriteJSON(path, m); err != nil {
		return "", false, err
	}
	return "wired " + strings.Join(added, ", ") + " in " + path + " (command: " + cmd + ")", true, nil
}

func cursorEntriesInvokeJog(entries []any, cmd string) bool {
	for _, e := range entries {
		em, _ := e.(map[string]any)
		if c, _ := em["command"].(string); c == cmd || invokesJog(c, "cursor") {
			return true
		}
	}
	return false
}

func cursorHooksUninstall(project bool) (string, bool, error) {
	path, err := cursorHooksPath(project)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "no settings file at " + path + " — nothing to remove", false, nil
	}
	m, err := install.LoadJSON(path)
	if err != nil {
		return "", false, err
	}
	removed := 0
	if hooks, _ := m["hooks"].(map[string]any); hooks != nil {
		for ev, v := range hooks {
			entries, ok := v.([]any)
			if !ok || len(entries) == 0 {
				continue
			}
			var kept []any
			removedHere := 0
			for _, e := range entries {
				em, _ := e.(map[string]any)
				if c, _ := em["command"].(string); em != nil && invokesJog(c, "cursor") {
					removed++
					removedHere++
					continue
				}
				kept = append(kept, e)
			}
			switch {
			case removedHere == 0:
				// untouched, verbatim
			case len(kept) == 0:
				delete(hooks, ev) // event emptied by the removal — drop it
			default:
				hooks[ev] = kept
			}
		}
		if removed > 0 && len(hooks) == 0 {
			delete(m, "hooks")
		}
	}
	if removed == 0 {
		return "no jog hooks in " + path + " — nothing to remove", false, nil
	}
	if err := install.WriteJSON(path, m); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("removed %d jog hook(s) from %s — everything else untouched", removed, path), true, nil
}

func cursorHooksLocation() string {
	if p, err := cursorHooksPath(false); err == nil && cursorFileWired(p) {
		return install.TildePath(p)
	}
	if p, err := cursorHooksPath(true); err == nil && cursorFileWired(p) {
		return install.ProjectDisplay(p)
	}
	return ""
}

// cursorFileWired parses Cursor's flat hooks.json defensively; a
// malformed file simply reads as "not wired".
func cursorFileWired(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var s struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	for _, entries := range s.Hooks {
		for _, e := range entries {
			if invokesJog(e.Command, "cursor") {
				return true
			}
		}
	}
	return false
}
