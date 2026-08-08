package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// hookPayload is the JSON Claude Code writes to a hook's stdin. Parsed
// defensively: unknown fields ignored, missing fields degrade to generic
// provenance — the payload is external surface that may drift.
type hookPayload struct {
	HookEventName string         `json:"hook_event_name"`
	SessionID     string         `json:"session_id"`
	Cwd           string         `json:"cwd"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	Prompt        string         `json:"prompt"`
}

// HookClaude handles `jog hook claude` for PreToolUse and UserPromptSubmit.
//
// Iron rule: ALWAYS exit 0 (plan M4). A non-zero exit from PreToolUse blocks
// the user's tool call; from UserPromptSubmit it blocks their prompt. jog
// failing must never cost the user their action — diagnostics go to stderr
// only under JOG_DEBUG=1. Stdout stays empty: UserPromptSubmit stdout is
// injected into Claude's context on exit 0.
func HookClaude(stdin io.Reader) int {
	data, err := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err != nil {
		return hookDone("read stdin", err)
	}
	var p hookPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return hookDone("parse payload", err)
	}

	cwd := p.Cwd // trust the payload over process cwd; hooks may run anywhere
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return hookDone("getwd", err)
		}
	}
	repo, err := gitx.Discover(cwd)
	if err != nil {
		return hookDone("discover", err) // outside a repo: the common, silent case
	}
	res, err := snap.Take(repo, provenance.Claude(p.SessionID, hookDetail(&p)))
	if err != nil {
		return hookDone("snapshot", err)
	}
	debugf("hook: %+v", res)
	return 0
}

// hookDetail renders the payload into the provenance detail. Formats from
// DESIGN §3: `Bash(rm -rf src/old)`, `Edit(src/main.go)`, `prompt "refactor…"`.
func hookDetail(p *hookPayload) string {
	str := func(key string) string {
		s, _ := p.ToolInput[key].(string)
		return s
	}
	switch p.HookEventName {
	case "UserPromptSubmit":
		return `prompt "` + provenance.Truncate(p.Prompt, 60) + `"`
	case "PreToolUse":
		switch p.ToolName {
		case "Bash":
			return "Bash(" + provenance.Truncate(str("command"), 64) + ")"
		case "Edit", "Write":
			return p.ToolName + "(" + relPath(p.Cwd, str("file_path")) + ")"
		case "NotebookEdit":
			return "NotebookEdit(" + relPath(p.Cwd, str("notebook_path")) + ")"
		default:
			return p.ToolName // matcher normally limits us; degrade gracefully
		}
	default:
		return p.HookEventName // future events snapshot too, labeled honestly
	}
}

// relPath shortens absolute tool paths against the payload cwd for readable
// timelines; anything outside cwd stays absolute.
func relPath(base, path string) string {
	if base == "" || !filepath.IsAbs(path) {
		return path
	}
	if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func hookDone(stage string, err error) int {
	debugf("hook: %s: %v", stage, err)
	return 0
}

func debugf(format string, args ...any) {
	if os.Getenv("JOG_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "jog: "+format+"\n", args...)
	}
}
