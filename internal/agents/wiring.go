package agents

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
)

// Shared hook-wiring mechanics — everything here is client-agnostic;
// per-client facts (events, paths) live in claude.go and codex.go. The
// commands written into settings are runtime entries such as `jog hook
// claude`; this file only edits configuration. Path resolution and
// managed-file mechanics live in internal/install, shared with `jog
// editors`.

// hookEvent is one event install wires: its name and an optional tool
// matcher.
type hookEvent struct{ name, matcher string }

// hookCommand picks how the hook invokes jog: the bare name when jog is on
// PATH (survives upgrades and relocations), otherwise this binary's
// absolute path as a fallback that at least works today.
func hookCommand(client string) string {
	if _, err := exec.LookPath("jog"); err == nil {
		return "jog hook " + client
	}
	if exe, err := os.Executable(); err == nil {
		return exe + " hook " + client
	}
	return "jog hook " + client
}

// wireHooks adds the jog hook entries that are missing and reports which
// events it added. An event already invoking this client's adapter —
// however the user shaped it — is left exactly alone. extras are
// client-specific fields each written entry carries (e.g. Gemini's
// "name", which its hook-trust fingerprinting requires).
func wireHooks(m map[string]any, cmd, client string, events []hookEvent, extras map[string]any) ([]string, error) {
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
	for _, ev := range events {
		if eventInvokesJog(hooks[ev.name], cmd, client) {
			continue
		}
		inner := map[string]any{"type": "command", "command": cmd}
		maps.Copy(inner, extras)
		entry := map[string]any{"hooks": []any{inner}}
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

// eventInvokesJog recognizes both the exact command chosen for this
// install (which may be an absolute path to a differently-named binary)
// and any user-authored jog command for the adapter.
func eventInvokesJog(v any, cmd, client string) bool {
	groups, _ := v.([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		entries, _ := gm["hooks"].([]any)
		for _, e := range entries {
			em, _ := e.(map[string]any)
			if c, _ := em["command"].(string); c == cmd || strings.Contains(c, "jog hook "+client) {
				return true
			}
		}
	}
	return false
}

// unwireHooks removes every hook entry whose command invokes the requested
// adapter, wherever the user put it, then prunes the structures that
// emptied out. Everything else — other hooks in the same matcher group,
// unrelated events, unknown fields — survives byte-for-byte in value terms.
func unwireHooks(m map[string]any, client string) int {
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
				if c, _ := em["command"].(string); em != nil && strings.Contains(c, "jog hook "+client) {
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

// hooksFileWired parses a client's JSON hook file defensively, reporting
// whether any command invokes the client's jog adapter. A malformed
// external file simply reads as "not wired".
func hooksFileWired(path, client string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	for _, matchers := range s.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				if strings.Contains(hook.Command, "jog hook "+client) {
					return true
				}
			}
		}
	}
	return false
}
