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

// hookPayload is the JSON an agent client writes to a hook's stdin. Parsed
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
// only under JOG_DEBUG=1.
//
// Stdout is context injection: on exit 0, Claude Code adds a
// UserPromptSubmit hook's stdout to the model's context (PreToolUse stdout
// is not injected, so those events stay silent). jog uses that channel for
// exactly one line per session — see sessionNotice.
func HookClaude(stdin io.Reader, stdout io.Writer) int {
	return hookAgent("claude", stdin, stdout)
}

// HookCodex handles `jog hook codex` using Codex's PreToolUse and
// UserPromptSubmit payloads. Like the Claude adapter, it always exits 0.
func HookCodex(stdin io.Reader, stdout io.Writer) int {
	return hookAgent("codex", stdin, stdout)
}

func hookAgent(client string, stdin io.Reader, stdout io.Writer) int {
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
	res, err := snap.Take(repo, provenance.Agent(client, p.SessionID, hookDetail(&p)))
	if err != nil {
		return hookDone("snapshot", err)
	}
	debugf("hook: %+v", res)
	if p.HookEventName == "UserPromptSubmit" && p.SessionID != "" {
		sessionNotice(repo, client, p.SessionID, stdout)
	}
	return 0
}

// sessionNotice tells an agent, once per session per repo, that jog's safety
// net is live and how to use it. Once is the whole design: repeated every
// prompt it becomes noise the model learns to ignore, so the marker file
// must be durably written before anything is emitted — on any doubt
// (no session id, unwritable marker) we stay silent rather than risk
// spamming every prompt.
const agentNotice = "[jog] This repo's uncommitted work is snapshotted before every prompt and tool call. " +
	"If a file is lost or overwritten, run `jog snaps <path>` to list its saved versions and " +
	"`jog back <path> --at <id>` to restore it — check before concluding work is gone. " +
	"Before a risky operation, `jog -m \"msg\"` takes a labeled checkpoint."

func sessionNotice(repo *gitx.Repo, client, session string, w io.Writer) {
	marker := filepath.Join(repo.GitDir, "jog", client+"-session")
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == session {
		return
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(marker, []byte(session+"\n"), 0o644); err != nil {
		return
	}
	fmt.Fprintln(w, agentNotice)
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
		case "apply_patch":
			if c := str("command"); c != "" {
				return "apply_patch(" + provenance.Truncate(c, 64) + ")"
			}
			return "apply_patch" // input shape drifted — no empty parens
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
