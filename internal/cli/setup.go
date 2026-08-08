package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
)

// Wiring management for the Claude Code integration: `jog hook claude
// install|uninstall` edits Claude Code's settings JSON. Default scope is
// the home directory — the hook exits in milliseconds outside git repos,
// so user-level wiring covering every repo is the right default. --project
// scopes to the current repo's .claude/settings.local.json: the *personal*
// settings file, deliberately, because a `jog hook claude` command
// committed to the shared settings.json would fire (and fail) on
// teammates' machines that don't have jog installed.

const hookSetupUsage = "jog: usage: jog hook claude install|uninstall [--project]"

// HookSetup handles `jog hook claude install|uninstall [--project]`.
// Unlike the runtime hook entry (iron rule: exit 0 always), these are
// human-invoked and error normally.
func HookSetup(args []string) int {
	action, project := "", false
	for _, a := range args {
		switch a {
		case "install", "uninstall":
			if action != "" {
				fmt.Fprintln(os.Stderr, hookSetupUsage)
				return 2
			}
			action = a
		case "--project":
			project = true
		default:
			fmt.Fprintln(os.Stderr, hookSetupUsage)
			return 2
		}
	}
	if action == "" {
		fmt.Fprintln(os.Stderr, hookSetupUsage)
		return 2
	}

	path, err := claudeSettingsPath(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	if action == "install" {
		return hookInstall(path, project)
	}
	return hookUninstall(path)
}

func hookInstall(path string, project bool) int {
	m, err := loadSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	cmd := hookCommand()
	added, err := wireHooks(m, cmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	if len(added) == 0 {
		fmt.Printf("hooks already wired in %s — nothing to do\n", path)
		return 0
	}
	if err := writeSettings(path, m); err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	fmt.Printf("wired %s in %s\n", strings.Join(added, " and "), path)
	fmt.Printf("  hook command: %s\n", cmd)
	if project {
		fmt.Println("  (settings.local.json is personal and not meant to be committed —")
		fmt.Println("   a committed hook would break for teammates without jog installed)")
	}
	fmt.Println("Claude Code snapshots this way before every prompt and tool call.")
	fmt.Println("`jog hook claude uninstall` reverses this exactly; `jog doctor` verifies it.")
	return 0
}

func hookUninstall(path string) int {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("no settings file at %s — nothing to remove\n", path)
		return 0
	}
	m, err := loadSettings(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	removed := unwireHooks(m)
	if removed == 0 {
		fmt.Printf("no jog hooks found in %s — nothing to remove\n", path)
		return 0
	}
	if err := writeSettings(path, m); err != nil {
		fmt.Fprintln(os.Stderr, "jog:", err)
		return 1
	}
	fmt.Printf("removed %d jog hook(s) from %s — everything else untouched\n", removed, path)
	return 0
}

// jogHookEvents is what install wires: the same two events the README
// documents, covering every tool-call and prompt boundary.
var jogHookEvents = []struct{ name, matcher string }{
	{"PreToolUse", "Bash|Edit|Write|NotebookEdit"},
	{"UserPromptSubmit", ""},
}

// hookCommand picks how the hook invokes jog: the bare name when jog is on
// PATH (survives upgrades and relocations), otherwise this binary's
// absolute path as a fallback that at least works today.
func hookCommand() string {
	if _, err := exec.LookPath("jog"); err == nil {
		return "jog hook claude"
	}
	if exe, err := os.Executable(); err == nil {
		return exe + " hook claude"
	}
	return "jog hook claude"
}

// claudeSettingsPath resolves the settings file for the scope. Project
// scope anchors at the repo toplevel when inside one, else the cwd.
func claudeSettingsPath(project bool) (string, error) {
	if !project {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory: %w", err)
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".claude", "settings.local.json"), nil
}

func projectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if repo, err := gitx.Discover(wd); err == nil && !repo.Bare {
		if top, err := repo.Run("rev-parse", "--show-toplevel"); err == nil && top != "" {
			return top, nil
		}
	}
	return wd, nil
}

// loadSettings parses a Claude Code settings file into a generic map so
// every field jog doesn't understand round-trips untouched. A missing file
// is an empty map; malformed JSON is a hard error — never rewrite a file
// that can't be read back faithfully.
func loadSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v) — fix it, or wire the hooks by hand", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func writeSettings(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// wireHooks adds the jog hook entries that are missing and reports which
// events it added. An event already invoking `jog hook claude` — however
// the user shaped it — is left exactly alone.
func wireHooks(m map[string]any, cmd string) ([]string, error) {
	var hooks map[string]any
	switch h := m["hooks"].(type) {
	case nil:
		hooks = map[string]any{}
		m["hooks"] = hooks
	case map[string]any:
		hooks = h
	default:
		return nil, fmt.Errorf(`the settings file's "hooks" field has an unexpected shape — wire the hooks by hand`)
	}

	var added []string
	for _, ev := range jogHookEvents {
		if eventInvokesJog(hooks[ev.name]) {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": cmd}},
		}
		if ev.matcher != "" {
			entry["matcher"] = ev.matcher
		}
		groups, ok := hooks[ev.name].([]any)
		if !ok && hooks[ev.name] != nil {
			return nil, fmt.Errorf("the settings file's %q hooks have an unexpected shape — wire the hooks by hand", ev.name)
		}
		hooks[ev.name] = append(groups, entry)
		added = append(added, ev.name)
	}
	return added, nil
}

func eventInvokesJog(v any) bool {
	groups, _ := v.([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		entries, _ := gm["hooks"].([]any)
		for _, e := range entries {
			em, _ := e.(map[string]any)
			if c, _ := em["command"].(string); strings.Contains(c, "jog hook claude") {
				return true
			}
		}
	}
	return false
}

// unwireHooks removes every hook entry whose command invokes `jog hook
// claude`, wherever the user put it, then prunes the structures that
// emptied out. Everything else — other hooks in the same matcher group,
// unrelated events, unknown fields — survives byte-for-byte in value terms.
func unwireHooks(m map[string]any) int {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}
	removed := 0
	for ev, v := range hooks {
		groups, ok := v.([]any)
		if !ok || len(groups) == 0 {
			continue
		}
		var keptGroups []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			entries, _ := gm["hooks"].([]any)
			var kept []any
			removedHere := 0
			for _, e := range entries {
				em, _ := e.(map[string]any)
				if c, _ := em["command"].(string); em != nil && strings.Contains(c, "jog hook claude") {
					removed++
					removedHere++
					continue
				}
				kept = append(kept, e)
			}
			if removedHere == 0 {
				keptGroups = append(keptGroups, gm) // untouched, verbatim
				continue
			}
			if len(kept) == 0 {
				continue // group emptied by the removal — drop it whole
			}
			gm["hooks"] = kept
			keptGroups = append(keptGroups, gm)
		}
		if len(keptGroups) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = keptGroups
		}
	}
	if removed > 0 && len(hooks) == 0 {
		delete(m, "hooks")
	}
	return removed
}
