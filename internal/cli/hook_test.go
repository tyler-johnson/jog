package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/testrepo"
)

func setup(t *testing.T) *testrepo.Repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
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
	var out strings.Builder
	if code := HookClaude(strings.NewReader(stdin), &out); code != 0 {
		t.Fatalf("HookClaude exited %d — the iron rule is exit 0, always", code)
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

// Row 18 — the iron rule: garbage input, non-repos, bare repos, engine
// failures. All exit 0, none snapshot anything they shouldn't.
func TestHookIronRule(t *testing.T) {
	tr := setup(t)

	hook(t, "this is not json {{{")
	hook(t, "") // empty stdin
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
