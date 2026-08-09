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

// hookPayload is the JSON an agent client writes to a hook's stdin. One
// struct covers every client: Claude, Codex, Gemini, and Copilot (in its
// Claude-compatible mode) share the snake_case field names outright;
// Cursor's divergent names (conversation_id, workspace_roots, top-level
// command/file_path) ride alongside; OpenCode's plugin emits the shared
// names itself. Parsed defensively: unknown fields ignored, missing
// fields degrade to generic provenance — the payload is external surface
// that may drift.
type hookPayload struct {
	HookEventName  string         `json:"hook_event_name"`
	SessionID      string         `json:"session_id"`
	ConversationID string         `json:"conversation_id"` // cursor
	Cwd            string         `json:"cwd"`
	WorkspaceRoots []string       `json:"workspace_roots"` // cursor
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	Command        string         `json:"command"`   // cursor beforeShellExecution
	FilePath       string         `json:"file_path"` // cursor afterFileEdit
	Prompt         string         `json:"prompt"`
}

func (p *hookPayload) session() string {
	if p.SessionID != "" {
		return p.SessionID
	}
	return p.ConversationID
}

// hookAdapter is the per-client runtime behavior that differs: which
// event's stdout can inject context (and how the notice must be encoded),
// and whether the client's protocol wants an explicit acknowledgement.
type hookAdapter struct {
	noticeEvent string                          // "" = this client gets no session notice
	notice      func(w io.Writer)               // how the notice is encoded for this client
	respond     func(event string, w io.Writer) // protocol ack some events require
}

var hookAdapters = map[string]hookAdapter{
	// Claude Code injects UserPromptSubmit stdout into context on exit 0
	// (PreToolUse stdout is not injected, so those events stay silent).
	"claude": {noticeEvent: "UserPromptSubmit", notice: textNotice},
	// Codex: same contract as Claude.
	"codex": {noticeEvent: "UserPromptSubmit", notice: textNotice},
	// Gemini parses hook stdout as JSON on exit 0, so the notice is
	// wrapped as BeforeAgent additionalContext; anything else stays
	// silent (empty stdout, never stray text).
	"gemini": {noticeEvent: "BeforeAgent", notice: geminiNotice},
	// Copilot ignores prompt-submit stdout from config hooks and has no
	// context injection there — no notice; the skill does the teaching.
	// Its preToolUse is fail-closed on non-zero exit, so the iron rule
	// (exit 0, always) is what keeps jog from ever blocking a tool call.
	"copilot": {},
	// Cursor's permission events want an explicit allow (it fails open on
	// empty output, but answering is belt-and-braces); prompt-submit
	// stdout cannot inject context — no notice.
	"cursor": {respond: cursorRespond},
	// OpenCode has no stdout contract of its own — jog's plugin captures
	// this process's stdout and pushes a non-empty result into the
	// message parts, so the plain-text notice reaches the model.
	"opencode": {noticeEvent: "chat.message", notice: textNotice},
}

// Hook handles `jog hook <client>` — the runtime entries `jog agents
// install` wires into each client's configuration.
//
// Iron rule: ALWAYS exit 0 (plan M4). A non-zero exit can block the
// user's tool call or prompt (Claude and Gemini treat some codes as a
// veto; Copilot's preToolUse blocks on any non-zero). jog failing must
// never cost the user their action — diagnostics go to stderr only under
// JOG_DEBUG=1.
func Hook(clientName string, stdin io.Reader, stdout io.Writer) int {
	ad, known := hookAdapters[clientName]
	if !known {
		// Possibly wired by a newer/older jog: still never block.
		fmt.Fprintf(os.Stderr, "jog: unknown hook adapter %q — `jog agents list` shows the supported clients\n", clientName)
		return 0
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err != nil {
		return hookDone("read stdin", err)
	}
	var p hookPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return hookDone("parse payload", err)
	}
	// The protocol ack goes out no matter what happens below: an
	// unreadable repo or failed snapshot must still answer "allow".
	if ad.respond != nil {
		defer ad.respond(p.HookEventName, stdout)
	}

	cwd := p.Cwd // trust the payload over process cwd; hooks may run anywhere
	if cwd == "" && len(p.WorkspaceRoots) > 0 {
		cwd = p.WorkspaceRoots[0]
	}
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return hookDone("getwd", err)
		}
	}
	repo, err := gitx.Discover(cwd)
	if err != nil {
		return hookDone("discover", err) // outside a repo: the common, silent case
	}
	res, err := snap.Take(repo, provenance.Agent(clientName, p.session(), hookDetail(&p, cwd)))
	if err != nil {
		return hookDone("snapshot", err)
	}
	debugf("hook: %+v", res)
	if ad.noticeEvent != "" && p.HookEventName == ad.noticeEvent && p.session() != "" {
		sessionNotice(repo, clientName, p.session(), stdout, ad.notice)
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
	"If a file is lost or overwritten, run `jog log <path>` to list its saved versions and " +
	"`jog restore <path> --at <id>` to restore it — check before concluding work is gone. " +
	"Before a risky operation, `jog -m \"msg\"` takes a labeled checkpoint."

func sessionNotice(repo *gitx.Repo, client, session string, w io.Writer, emit func(io.Writer)) {
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
	emit(w)
}

func textNotice(w io.Writer) {
	fmt.Fprintln(w, agentNotice)
}

// geminiNotice wraps the notice in Gemini's hook-output JSON: on exit 0
// stdout must be a single JSON document, and additionalContext is its
// context-injection channel for BeforeAgent.
func geminiNotice(w io.Writer) {
	b, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "BeforeAgent",
			"additionalContext": agentNotice,
		},
	})
	if err != nil {
		return
	}
	fmt.Fprintln(w, string(b))
}

// cursorRespond answers Cursor's permission events with an explicit
// allow. jog only observes — it must never be the reason an action was
// denied — and after-events take no answer.
func cursorRespond(event string, w io.Writer) {
	switch event {
	case "beforeShellExecution", "beforeMCPExecution", "beforeReadFile", "preToolUse":
		fmt.Fprintln(w, `{"permission":"allow"}`)
	case "beforeSubmitPrompt":
		fmt.Fprintln(w, `{"continue":true}`)
	}
}

// hookDetail renders the payload into the provenance detail. Formats from
// DESIGN §3: `Bash(rm -rf src/old)`, `Edit(src/main.go)`, `prompt "refactor…"`.
// Every client's prompt and tool boundaries funnel into the same shapes;
// unknown events and tools degrade to their own names, labeled honestly.
func hookDetail(p *hookPayload, cwd string) string {
	str := func(key string) string {
		s, _ := p.ToolInput[key].(string)
		return s
	}
	switch p.HookEventName {
	case "UserPromptSubmit", "BeforeAgent", "beforeSubmitPrompt", "chat.message":
		return `prompt "` + provenance.Truncate(p.Prompt, 60) + `"`
	case "beforeShellExecution": // cursor: the command rides the payload itself
		return "sh(" + provenance.Truncate(p.Command, 64) + ")"
	case "afterFileEdit": // cursor: fires after the edit; the snapshot catches it immediately
		return "edit(" + relPath(cwd, p.FilePath) + ")"
	case "PreToolUse", "BeforeTool", "tool.execute.before":
		switch p.ToolName {
		case "Bash":
			return "Bash(" + provenance.Truncate(str("command"), 64) + ")"
		case "apply_patch":
			if c := str("command"); c != "" {
				return "apply_patch(" + provenance.Truncate(c, 64) + ")"
			}
			return "apply_patch" // input shape drifted — no empty parens
		case "Edit", "Write":
			return p.ToolName + "(" + relPath(cwd, str("file_path")) + ")"
		case "NotebookEdit":
			return "NotebookEdit(" + relPath(cwd, str("notebook_path")) + ")"
		default:
			// Other clients' tool vocabularies (gemini's run_shell_command,
			// opencode's bash/edit/write): find the command or path by its
			// conventional key, else the bare tool name.
			if c := str("command"); c != "" {
				return p.ToolName + "(" + provenance.Truncate(c, 64) + ")"
			}
			for _, k := range []string{"file_path", "filePath", "path"} {
				if f := str(k); f != "" {
					return p.ToolName + "(" + relPath(cwd, f) + ")"
				}
			}
			return p.ToolName
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
