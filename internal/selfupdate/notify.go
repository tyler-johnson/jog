package selfupdate

// The automatic update check. jog's users mostly never touch the binary
// after setup — it runs behind the git alias and hooks — so the notice
// that a release exists rides the passthrough: a detached background
// `jog update --check` refreshes a small cache file at most weekly, and
// when the cache says a newer release exists, one line prints after the
// user's git command, once per release, ever. The hot path never touches
// the network — deciding whether anything is pending is one file read.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// checkInterval is how long a completed check (successful or not)
// silences the background refresh.
const checkInterval = 7 * 24 * time.Hour

// checkState is the whole schema of the cache file. Notified records the
// last release a notice was printed for, so each release announces
// exactly once.
type checkState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	Notified  string    `json:"notified,omitempty"`
}

// statePath is <user cache dir>/jog/update.json — XDG_CACHE_HOME or
// ~/.cache on linux, Library/Caches on darwin, LOCALAPPDATA on windows.
func statePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jog", "update.json"), nil
}

// loadState returns the zero state on any error: a missing or corrupt
// cache means "never checked", which only makes the next check happen
// sooner.
func loadState() checkState {
	p, err := statePath()
	if err != nil {
		return checkState{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return checkState{}
	}
	var s checkState
	if json.Unmarshal(b, &s) != nil {
		return checkState{}
	}
	return s
}

// saveState writes atomically — temp file plus rename in the same
// directory — so a reader never sees a half-written cache.
func saveState(s checkState) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".update-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// needsCheck reports whether the cache is stale enough to refresh.
func needsCheck(s checkState, now time.Time) bool {
	return now.Sub(s.CheckedAt) >= checkInterval
}

// gatesOpen is the cheap half of the enablement check: source builds
// ("(devel)", pseudo-versions) have no release to compare against, and
// CI must never spawn checkers or print notices.
func gatesOpen(version string) bool {
	return releaseVersion.MatchString(version) && os.Getenv("CI") == ""
}

// configEnabled is the expensive half — one git spawn — so callers only
// reach it when the cache is stale or a notice is about to print; the
// common case pays nothing. Unset defaults to on.
func configEnabled() bool {
	out, err := exec.Command("git", "config", "--type=bool", "--get", "jog.updateCheck").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != "false"
}

// MaybeSpawnCheck starts a detached `jog update --check` when the cache
// is stale and checks are enabled. Called from human-facing commands
// only — never hooks. The child is never waited on; its exit and output
// are nobody's business.
func MaybeSpawnCheck(version string) {
	if !gatesOpen(version) {
		return
	}
	if !needsCheck(loadState(), time.Now()) {
		return
	}
	if !configEnabled() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "update", "--check")
	if err := cmd.Start(); err == nil {
		cmd.Process.Release()
	}
}

// RefreshCache is the `jog update --check` body: record that a check
// happened, then record the latest release if the lookup succeeds.
// CheckedAt advances even on failure — and before the fetch — so an
// offline machine doesn't spawn a checker on every command.
func (u *Updater) RefreshCache() {
	s := loadState()
	s.CheckedAt = time.Now()
	saveState(s)
	rel, err := u.latest()
	if err != nil || !releaseVersion.MatchString(rel.TagName) {
		return
	}
	s.Latest = rel.TagName
	saveState(s)
}

// Pending returns the one-line update notice when one is due, "" when
// nothing should print. Reading the cache is the only cost until a
// notice is actually about to show; only then is the config consulted.
func Pending(version string) string {
	if !gatesOpen(version) {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	tty := term.IsTerminal(int(os.Stderr.Fd()))
	notice := pendingNotice(loadState(), version, exe, tty, true)
	if notice == "" || !configEnabled() {
		return ""
	}
	return notice
}

// pendingNotice is the pure decision core behind Pending: given the
// cached state and the environment's answers, the notice line or "".
func pendingNotice(s checkState, version, exe string, tty, enabled bool) string {
	if !tty || !enabled || !releaseVersion.MatchString(version) {
		return ""
	}
	if !releaseVersion.MatchString(s.Latest) || !isNewer(version, s.Latest) || s.Notified == s.Latest {
		return ""
	}
	cmd := "jog update"
	if isBrewInstall(exe) {
		cmd = "brew upgrade jog"
	}
	return fmt.Sprintf("jog: %s is available (running %s) — update with: %s", s.Latest, version, cmd)
}

// MarkNotified records that the notice for the cached latest release has
// been shown.
func MarkNotified() {
	s := loadState()
	if s.Latest == "" || s.Notified == s.Latest {
		return
	}
	s.Notified = s.Latest
	saveState(s)
}

// CheckStatus is doctor's one-line summary of the cached check — part of
// the report, so unlike Pending it is neither TTY- nor config-gated.
func CheckStatus(version string) string {
	if !releaseVersion.MatchString(version) {
		return "source build — updates via go install"
	}
	s := loadState()
	switch {
	case !releaseVersion.MatchString(s.Latest):
		return "no check yet"
	case isNewer(version, s.Latest):
		return s.Latest + " available — jog update"
	default:
		return "up to date (" + version + ")"
	}
}
