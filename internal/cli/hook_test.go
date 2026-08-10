package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

func setup(t *testing.T) *testrepo.Repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	tr := testrepo.New(t)
	tr.Write("a.txt", "x\n")
	tr.Commit("first")
	return tr
}

func payload(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hook(t *testing.T, stdin string) string {
	t.Helper()
	return hookAs(t, "claude", stdin)
}

func hookAs(t *testing.T, client, stdin string) string {
	t.Helper()
	var out strings.Builder
	if code := Hook(client, strings.NewReader(stdin), &out); code != 0 {
		t.Fatalf("Hook(%q) exited %d — the iron rule is exit 0, always", client, code)
	}
	return out.String()
}

func subject(tr *testrepo.Repo) string {
	return tr.Git("log", "-1", "--format=%s", "refs/jog/main")
}

func TestHookBash(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	hook(t, payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "b3f1a2c4-5678-90ab",
		"cwd":             tr.Dir,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "rm -rf src/old"},
	}))
	if got := subject(tr); got != "claude[b3f1a2c4]: Bash(rm -rf src/old)" {
		t.Errorf("subject = %q", got)
	}
}

func TestHookEditRelativizesPath(t *testing.T) {
	tr := setup(t)
	tr.Write("src/main.go", "package main\n")
	hook(t, payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "b3f1a2c4",
		"cwd":             tr.Dir,
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": filepath.Join(tr.Dir, "src", "main.go")},
	}))
	if got := subject(tr); got != "claude[b3f1a2c4]: Edit(src/main.go)" {
		t.Errorf("subject = %q", got)
	}
}

func TestHookPrompt(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	hook(t, payload(t, map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "b3f1a2c4",
		"cwd":             tr.Dir,
		"prompt":          "refactor the frobnicator",
	}))
	if got := subject(tr); got != `claude[b3f1a2c4]: prompt "refactor the frobnicator"` {
		t.Errorf("subject = %q", got)
	}
}

func TestHookLongCommandTruncated(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	hook(t, payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"cwd":             tr.Dir,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": strings.Repeat("x", 300) + "\nsecond line"},
	}))
	got := subject(tr)
	if utf8.RuneCountInString(got) > 110 || !strings.Contains(got, "…") {
		t.Errorf("subject not truncated: %d runes, %q", utf8.RuneCountInString(got), got)
	}
	if strings.Contains(got, "\n") {
		t.Error("subject contains newline")
	}
	// No session_id → bare claude source.
	if !strings.HasPrefix(got, "claude: Bash(") {
		t.Errorf("subject = %q, want claude: Bash(…) prefix", got)
	}
}

func TestHookUnknownToolDegrades(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	hook(t, payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "b3f1a2c4",
		"cwd":             tr.Dir,
		"tool_name":       "SomeFutureTool",
	}))
	if got := subject(tr); got != "claude[b3f1a2c4]: SomeFutureTool" {
		t.Errorf("subject = %q", got)
	}
}

// The session notice reaches Claude's context via UserPromptSubmit stdout:
// once per session per repo, never on PreToolUse (whose stdout is not
// injected), and never without a session id to dedupe on.
func TestHookSessionNotice(t *testing.T) {
	tr := setup(t)
	prompt := func(session, text string) string {
		t.Helper()
		tr.Write("b.txt", text+"\n") // keep each snapshot distinct
		fields := map[string]any{
			"hook_event_name": "UserPromptSubmit",
			"cwd":             tr.Dir,
			"prompt":          text,
		}
		if session != "" {
			fields["session_id"] = session
		}
		return hook(t, payload(t, fields))
	}

	if out := prompt("s1", "first"); !strings.Contains(out, "[jog]") {
		t.Errorf("first prompt of session: want notice, got %q", out)
	}
	if out := prompt("s1", "second"); out != "" {
		t.Errorf("same session again: want silence, got %q", out)
	}
	if out := prompt("s2", "third"); !strings.Contains(out, "[jog]") {
		t.Errorf("new session: want notice again, got %q", out)
	}
	if out := prompt("", "fourth"); out != "" {
		t.Errorf("no session id: want silence (cannot dedupe), got %q", out)
	}

	tr.Write("b.txt", "pre\n")
	out := hook(t, payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "s3",
		"cwd":             tr.Dir,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "ls"},
	}))
	if out != "" {
		t.Errorf("PreToolUse: stdout is not context-injected, want empty, got %q", out)
	}
}

// Codex requires every non-empty UserPromptSubmit stdout stream to be one
// JSON document. In particular, its context injection is not Claude's raw
// stdout contract even though both clients use the same event name.
func TestHookCodexSessionNoticeJSON(t *testing.T) {
	tr := setup(t)
	prompt := func(session, text string) string {
		t.Helper()
		tr.Write("b.txt", text+"\n")
		return hookAs(t, "codex", payload(t, map[string]any{
			"hook_event_name": "UserPromptSubmit",
			"session_id":      session,
			"cwd":             tr.Dir,
			"prompt":          text,
		}))
	}

	out := prompt("codex-s1", "first")
	var notice struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &notice); err != nil {
		t.Fatalf("UserPromptSubmit stdout is not one JSON document: %v\n%s", err, out)
	}
	if got := notice.HookSpecificOutput.HookEventName; got != "UserPromptSubmit" {
		t.Errorf("hookEventName = %q, want UserPromptSubmit", got)
	}
	if got := notice.HookSpecificOutput.AdditionalContext; got != agentNotice {
		t.Errorf("additionalContext = %q, want agent notice", got)
	}
	if got := subject(tr); got != `codex[codex-s1]: prompt "first"` {
		t.Errorf("subject = %q", got)
	}

	if out := prompt("codex-s1", "again"); out != "" {
		t.Errorf("same session again: want silence, got %q", out)
	}

	tr.Write("b.txt", "tool\n")
	out = hookAs(t, "codex", payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "codex-s2",
		"cwd":             tr.Dir,
		"tool_name":       "apply_patch",
		"tool_input":      map[string]any{"command": "*** Begin Patch"},
	}))
	if out != "" {
		t.Errorf("PreToolUse must stay silent, got %q", out)
	}
}

// Cursor speaks its own dialect: conversation_id for the session,
// workspace_roots for the repo, and permission events that get an
// explicit allow — even when the snapshot path fails.
func TestHookCursor(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	out := hookAs(t, "cursor", payload(t, map[string]any{
		"hook_event_name": "beforeShellExecution",
		"conversation_id": "cur-conv-99",
		"workspace_roots": []string{tr.Dir},
		"command":         "rm -rf src",
	}))
	if out != "{\"permission\":\"allow\"}\n" {
		t.Errorf("beforeShellExecution response = %q", out)
	}
	if got := subject(tr); got != "cursor[cur-conv]: sh(rm -rf src)" {
		t.Errorf("subject = %q", got)
	}

	tr.Write("b.txt", "edited\n")
	out = hookAs(t, "cursor", payload(t, map[string]any{
		"hook_event_name": "afterFileEdit",
		"conversation_id": "cur-conv-99",
		"workspace_roots": []string{tr.Dir},
		"file_path":       filepath.Join(tr.Dir, "b.txt"),
	}))
	if out != "" {
		t.Errorf("afterFileEdit takes no answer, got %q", out)
	}
	if got := subject(tr); got != "cursor[cur-conv]: edit(b.txt)" {
		t.Errorf("subject = %q", got)
	}

	// Prompt submission: the continue ack, and no notice — Cursor has no
	// context injection at this event.
	tr.Write("b.txt", "prompted\n")
	out = hookAs(t, "cursor", payload(t, map[string]any{
		"hook_event_name": "beforeSubmitPrompt",
		"conversation_id": "cur-conv-99",
		"workspace_roots": []string{tr.Dir},
		"prompt":          "fix it",
	}))
	if out != "{\"continue\":true}\n" {
		t.Errorf("beforeSubmitPrompt response = %q", out)
	}

	// The allow must survive the failure paths: outside any repo.
	out = hookAs(t, "cursor", payload(t, map[string]any{
		"hook_event_name": "beforeShellExecution",
		"workspace_roots": []string{t.TempDir()},
		"command":         "ls",
	}))
	if out != "{\"permission\":\"allow\"}\n" {
		t.Errorf("failure-path response = %q", out)
	}
}

// Gemini shares Claude's payload shape but its stdout must be a single
// JSON document: the once-per-session notice arrives as BeforeAgent
// additionalContext, and every other event stays silent.
func TestHookGemini(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	out := hookAs(t, "gemini", payload(t, map[string]any{
		"hook_event_name": "BeforeTool",
		"session_id":      "gem-sess-1",
		"cwd":             tr.Dir,
		"tool_name":       "run_shell_command",
		"tool_input":      map[string]any{"command": "make deploy"},
	}))
	if out != "" {
		t.Errorf("BeforeTool must stay silent, got %q", out)
	}
	if got := subject(tr); got != "gemini[gem-sess]: run_shell_command(make deploy)" {
		t.Errorf("subject = %q", got)
	}

	tr.Write("b.txt", "prompted\n")
	out = hookAs(t, "gemini", payload(t, map[string]any{
		"hook_event_name": "BeforeAgent",
		"session_id":      "gem-sess-1",
		"cwd":             tr.Dir,
		"prompt":          "refactor",
	}))
	var notice struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &notice); err != nil {
		t.Fatalf("BeforeAgent stdout is not one JSON document: %v\n%s", err, out)
	}
	if !strings.Contains(notice.HookSpecificOutput.AdditionalContext, "[jog]") {
		t.Errorf("notice not in additionalContext: %q", out)
	}

	tr.Write("b.txt", "again\n")
	out = hookAs(t, "gemini", payload(t, map[string]any{
		"hook_event_name": "BeforeAgent",
		"session_id":      "gem-sess-1",
		"cwd":             tr.Dir,
		"prompt":          "again",
	}))
	if out != "" {
		t.Errorf("same session again: want silence, got %q", out)
	}
}

// OpenCode's plugin pipes its events here and forwards non-empty stdout
// into the model's context, so chat.message carries the plain notice.
func TestHookOpencode(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	out := hookAs(t, "opencode", payload(t, map[string]any{
		"hook_event_name": "tool.execute.before",
		"session_id":      "oc-sess-1",
		"cwd":             tr.Dir,
		"tool_name":       "bash",
		"tool_input":      map[string]any{"command": "rm -rf ."},
	}))
	if out != "" {
		t.Errorf("tool.execute.before must stay silent, got %q", out)
	}
	if got := subject(tr); got != "opencode[oc-sess-]: bash(rm -rf .)" {
		t.Errorf("subject = %q", got)
	}

	tr.Write("b.txt", "prompted\n")
	out = hookAs(t, "opencode", payload(t, map[string]any{
		"hook_event_name": "chat.message",
		"session_id":      "oc-sess-1",
		"cwd":             tr.Dir,
		"prompt":          "help me",
	}))
	if !strings.Contains(out, "[jog]") {
		t.Errorf("first chat.message: want notice, got %q", out)
	}
	if got := subject(tr); got != `opencode[oc-sess-]: prompt "help me"` {
		t.Errorf("subject = %q", got)
	}
}

// Copilot's Claude-compatible mode sends Claude-shaped payloads; its
// prompt-submit stdout is ignored by the client, so jog stays silent.
func TestHookCopilot(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "change\n")
	out := hookAs(t, "copilot", payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "cop-sess-1",
		"cwd":             tr.Dir,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "npm test"},
	}))
	if out != "" {
		t.Errorf("PreToolUse must stay silent, got %q", out)
	}
	if got := subject(tr); got != "copilot[cop-sess]: Bash(npm test)" {
		t.Errorf("subject = %q", got)
	}

	tr.Write("b.txt", "prompted\n")
	out = hookAs(t, "copilot", payload(t, map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "cop-sess-1",
		"cwd":             tr.Dir,
		"prompt":          "ship it",
	}))
	if out != "" {
		t.Errorf("copilot gets no notice (stdout is not injected), got %q", out)
	}
}

// An unknown adapter name still exits 0: it may be wired by a newer jog,
// and a hook must never block the user's action.
func TestHookUnknownAdapter(t *testing.T) {
	var out strings.Builder
	if code := Hook("clippy", strings.NewReader("{}"), &out); code != 0 {
		t.Errorf("unknown adapter exited %d", code)
	}
}

// Row 18 — the iron rule: garbage input, non-repos, bare repos, engine
// failures. All exit 0, none snapshot anything they shouldn't.
func TestHookIronRule(t *testing.T) {
	tr := setup(t)

	hook(t, "this is not json {{{")
	hook(t, "")                        // empty stdin
	hook(t, payload(t, map[string]any{ // cwd outside any repo
		"hook_event_name": "PreToolUse",
		"cwd":             t.TempDir(),
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "ls"},
	}))
	hook(t, payload(t, map[string]any{"cwd": filepath.Join(tr.Dir, "does-not-exist")}))

	// Bare repo: Take errors with ErrBareRepo; hook still exits 0.
	bare := t.TempDir()
	r0 := &gitx.Repo{WorkDir: bare}
	if _, err := r0.Run("init", "-q", "--bare", "."); err != nil {
		t.Fatal(err)
	}
	hook(t, payload(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"cwd":             bare,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "ls"},
	}))

	// None of the above may have created a chain in the real repo.
	if _, err := tr.TryGit("rev-parse", "--verify", "refs/jog/main"); err == nil {
		t.Error("a failure-path hook run created refs/jog/main")
	}
}
