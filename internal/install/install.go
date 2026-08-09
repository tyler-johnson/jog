// Package install holds the client-agnostic mechanics shared by jog's
// integration installers (`jog agents`, `jog editors`): resolving install
// paths, managing jog-owned files, JSON round-tripping, and the grouped
// terminal output both commands speak. Per-client and per-editor facts
// stay in their own packages — this one never knows who it is installing
// for.
package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tyler-johnson/jog/internal/gitx"
)

// HomePath joins elems under the home directory.
func HomePath(elems ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory: %w", err)
	}
	return filepath.Join(append([]string{home}, elems...)...), nil
}

// RepoPath joins elems under the project root: the repo toplevel when
// inside one, else the cwd.
func RepoPath(elems ...string) (string, error) {
	root, err := ProjectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, elems...)...), nil
}

func ProjectRoot() (string, error) {
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

// TildePath renders a home-anchored absolute path as ~/… for display.
func TildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

// ProjectDisplay renders a path inside the project root relative, tagged
// as project scope.
func ProjectDisplay(p string) string {
	if root, err := ProjectRoot(); err == nil {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel) + " (project)"
		}
	}
	return p
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// LoadJSON parses a JSON configuration file into a generic map so every
// field jog doesn't understand round-trips untouched. A missing file is
// an empty map; malformed JSON is a hard error — never rewrite a file
// that can't be read back faithfully.
func LoadJSON(path string) (map[string]any, error) {
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

func WriteJSON(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ManagedFile writes a file jog owns (a skill, a plugin, an editor hook)
// and reports what happened.
func ManagedFile(path string, content []byte) (string, bool, error) {
	existing, rerr := os.ReadFile(path)
	if rerr == nil && bytes.Equal(existing, content) {
		return "already up to date — " + path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", false, err
	}
	if rerr == nil {
		return "updated — " + path, true, nil
	}
	return "installed — " + path, true, nil
}

// RemoveManagedFile removes a jog-owned file — unless it differs from what
// jog installs, which may mean the user's edits; jog won't delete those.
func RemoveManagedFile(path string, content []byte) (string, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "not installed — nothing to remove", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !bytes.Equal(b, content) {
		return "", false, fmt.Errorf("%s differs from what jog installs — it may carry your edits, so jog won't remove it (delete the file yourself if you mean to)", path)
	}
	if err := os.Remove(path); err != nil {
		return "", false, err
	}
	os.Remove(filepath.Dir(path)) // best-effort: fails harmlessly if not empty
	return "removed — " + path, true, nil
}

// JogSlot is the placeholder templated assets carry where the jog
// invocation goes. GUI editors launched from a desktop don't inherit the
// shell's PATH, so their assets get an absolute path baked in at install
// time; Render fills the slot, RemoveRenderedFile treats it as a wildcard
// so any past rendering is still jog's to delete.
const JogSlot = "{{JOG}}"

// JogPath resolves how an installed asset should invoke jog on this
// machine: the PATH location when there is one (survives in-place
// upgrades), else this binary's own path, else the bare name.
func JogPath() string {
	if p, err := exec.LookPath("jog"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "jog"
}

// Render bakes jog's invocation into a templated asset.
func Render(tmpl []byte, jog string) []byte {
	return []byte(strings.ReplaceAll(string(tmpl), JogSlot, jog))
}

// RemoveRenderedFile removes a jog-owned rendered asset. Install baked a
// machine-specific jog path into the template's slot, so equality is
// checked with that slot as a wildcard — any rendering jog could have
// produced is jog's to delete; anything else may carry the user's edits
// and is refused, same as RemoveManagedFile.
func RemoveRenderedFile(path string, tmpl []byte) (string, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "not installed — nothing to remove", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !renderingPattern(tmpl).Match(b) {
		return "", false, fmt.Errorf("%s differs from what jog installs — it may carry your edits, so jog won't remove it (delete the file yourself if you mean to)", path)
	}
	if err := os.Remove(path); err != nil {
		return "", false, err
	}
	os.Remove(filepath.Dir(path)) // best-effort: fails harmlessly if not empty
	return "removed — " + path, true, nil
}

// RenderedMatch reports whether b is a rendering of tmpl — the slot
// treated as a wildcard — for callers composing their own removal flow.
func RenderedMatch(b, tmpl []byte) bool {
	return renderingPattern(tmpl).Match(b)
}

func renderingPattern(tmpl []byte) *regexp.Regexp {
	parts := strings.Split(string(tmpl), JogSlot)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`\A` + strings.Join(parts, `[^"'\s]+`) + `\z`)
}

// Output vocabulary shared by the installers' list/install/uninstall.
// lipgloss drops the styling on non-TTY output, so piped output stays
// plain text.
var (
	StyleTitle = lipgloss.NewStyle().Bold(true)
	StyleGood  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	StyleDim   = lipgloss.NewStyle().Faint(true)
)

// Header opens one client's or editor's output block: bold name, faint
// title, blank line between blocks.
func Header(name, title string, i int) {
	if i > 0 {
		fmt.Println()
	}
	fmt.Println(StyleTitle.Render(name) + StyleDim.Render(" — "+title))
}

// Row prints one surface's outcome: a green check when something was (or
// is) in place, a faint dot for the no-op cases.
func Row(surface, msg string, did bool) {
	if did {
		fmt.Printf("  %-6s %s %s\n", surface, StyleGood.Render("✓"), msg)
	} else {
		fmt.Printf("  %-6s %s\n", surface, StyleDim.Render("· "+msg))
	}
}
