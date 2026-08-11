package selfupdate

// The automatic update check. jog's users mostly never touch the binary
// after setup — it runs behind the git alias and hooks — so updating
// rides the passthrough: a detached background `jog update --check`
// refreshes a small cache file on the jog.updateCheck cadence (daily by
// default), and when the cache says a newer release exists, a detached
// `jog update` installs it — the next
// invocation simply runs the new release. With jog.autoUpdate off, the
// install becomes a notice: one line after the user's git command, once
// per release, ever. The hot path never touches the network — deciding
// whether anything is pending is one file read.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/tyler-johnson/jog/internal/cadence"
	"github.com/tyler-johnson/jog/internal/gitx"
)

// defaultCheckInterval is how long a completed check (successful or
// not) silences the background refresh when jog.updateCheck doesn't
// name a cadence of its own.
const defaultCheckInterval = cadence.Default

// minCheckInterval floors a configured cadence — see cadence.Min.
const minCheckInterval = cadence.Min

// autoInterval throttles auto-update: while a release is pending, the
// config probe (and any install it launches) runs at most daily, so a
// failing download — or a user who saw the notice and moved on — costs
// nothing in between.
const autoInterval = 24 * time.Hour

// checkState is the whole schema of the cache file. Notified records the
// last release a notice was printed for, so each release announces
// exactly once. AutoTriedAt records the last auto-update probe, whether
// or not it installed anything.
type checkState struct {
	CheckedAt   time.Time `json:"checked_at"`
	Latest      string    `json:"latest,omitempty"`
	Notified    string    `json:"notified,omitempty"`
	AutoTriedAt time.Time `json:"auto_tried_at,omitzero"`
	// IntervalSecs caches jog.updateCheck so the hot path decides
	// staleness from the state file alone: 0 = unset (default cadence),
	// -1 = checking disabled. Refreshed whenever the config is actually
	// read, and by `jog config` on a set.
	IntervalSecs int64 `json:"interval_secs,omitempty"`
}

// interval is the cached check cadence. Disabled still returns the
// default: that is how often the config is re-read to notice a
// re-enable.
func (s checkState) interval() time.Duration {
	return cadence.Interval(s.IntervalSecs)
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

// needsCheck reports whether the cache is stale enough to refresh,
// judged by the cached cadence — the live config is only consulted once
// this says yes.
func needsCheck(s checkState, now time.Time) bool {
	return now.Sub(s.CheckedAt) >= s.interval()
}

// gatesOpen is the cheap half of the enablement check: source builds
// ("(devel)", pseudo-versions) have no release to compare against, and
// CI must never spawn checkers or print notices.
func gatesOpen(version string) bool {
	return releaseVersion.MatchString(version) && os.Getenv("CI") == ""
}

// configCheckInterval is the expensive half — a git spawn or two — so
// callers only reach it when the cache is stale or an update is
// pending; the common case pays nothing. jog.updateCheck speaks the
// shared cadence language (see internal/cadence): bool, seconds, or git
// expiry syntax; unset defaults to daily. The check is global, so the
// read runs from wherever jog happens to be — usually inside a repo,
// where local config wins, which is the right repo to listen to.
func configCheckInterval() (time.Duration, bool) {
	return cadence.Read(func(typeFlags ...string) (string, error) {
		args := append(append([]string{"config"}, typeFlags...), "--get", "jog.updateCheck")
		out, err := exec.Command(gitx.Bin(), args...).Output()
		return strings.TrimSpace(string(out)), err
	})
}

// intervalSecs is the state-file encoding of configCheckInterval's
// answer.
func intervalSecs(iv time.Duration, enabled bool) int64 {
	return cadence.Encode(iv, enabled)
}

// SyncInterval re-reads jog.updateCheck into the cache. `jog config`
// calls this after changing the setting so a new cadence (or a
// re-enable) applies now, not at the next check under the old one.
func SyncInterval() {
	s := loadState()
	iv, enabled := configCheckInterval()
	s.IntervalSecs = intervalSecs(iv, enabled)
	saveState(s)
}

// configAutoUpdate reports whether jog.autoUpdate is on: install new
// releases in the background instead of printing the notice. Unset
// defaults to on — setting it to false is how you opt into notices.
// Like configEnabled, one git spawn, reached only while an update is
// pending.
func configAutoUpdate() bool {
	out, err := exec.Command(gitx.Bin(), "config", "--type=bool", "--get", "jog.autoUpdate").Output()
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
	s := loadState()
	now := time.Now()
	if !needsCheck(s, now) {
		return
	}
	// The cached cadence says a check is due — now the config gets its
	// say, and whatever it says is cached so the next command is back to
	// one file read.
	iv, enabled := configCheckInterval()
	s.IntervalSecs = intervalSecs(iv, enabled)
	if !enabled {
		// Disabled: stamp CheckedAt so this config re-read itself runs at
		// most on the default cadence.
		s.CheckedAt = now
		saveState(s)
		return
	}
	if now.Sub(s.CheckedAt) < iv {
		// The live cadence is longer than the cached one — not due after
		// all.
		saveState(s)
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
	iv, enabled := configCheckInterval()
	s.IntervalSecs = intervalSecs(iv, enabled)
	saveState(s)
	rel, err := u.latest()
	if err != nil || !releaseVersion.MatchString(rel.TagName) {
		return
	}
	s.Latest = rel.TagName
	saveState(s)
}

// Pending returns the one-line update notice when one is due, "" when
// nothing should print. Reading the cache is the only cost until an
// update is actually pending; only then is the config consulted. With
// jog.autoUpdate on, a pending update starts a detached `jog update`
// instead of printing anything: the command in flight finishes on the
// running binary, the next one runs the new release.
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
	s := loadState()
	tty := term.IsTerminal(int(os.Stderr.Fd()))
	notice := pendingNotice(s, version, exe, tty, true)
	probe := autoUpdateDue(s, version, exe, time.Now())
	if notice == "" && !probe {
		return ""
	}
	if _, enabled := configCheckInterval(); !enabled {
		return ""
	}
	if probe {
		// Stamp the probe before anything else — whatever happens next,
		// the pending release must not re-read the config on every
		// command for the next day.
		s.AutoTriedAt = time.Now()
		saveState(s)
		if configAutoUpdate() {
			spawnUpdate(exe)
			return ""
		}
		return notice
	}
	// No probe due but a notice is. An auto-capable install only gets
	// here inside the probe throttle — an install attempt is at most a
	// day old — so auto mode still owns the release and the notice stays
	// suppressed. Brew installs never probe and keep their notice.
	if isAutoCapable(exe) && configAutoUpdate() {
		return ""
	}
	return notice
}

// isAutoCapable reports whether this install is jog's own to replace —
// brew owns its binaries, and gatesOpen has already excluded source
// builds by the time this is asked.
func isAutoCapable(exe string) bool {
	return !IsBrewInstall(exe)
}

// autoUpdateDue is the pure decision core behind auto-update: a newer
// release is cached, the install is jog's own to replace, and the daily
// probe throttle has lapsed. Notified is irrelevant here — notices and
// installs are separate ledgers.
func autoUpdateDue(s checkState, version, exe string, now time.Time) bool {
	if !releaseVersion.MatchString(version) || !releaseVersion.MatchString(s.Latest) {
		return false
	}
	return isNewer(version, s.Latest) && isAutoCapable(exe) && now.Sub(s.AutoTriedAt) >= autoInterval
}

// spawnUpdate starts the detached `jog update` that is an auto-update
// install. Like the checker, the child is never waited on and its
// output goes nowhere: success shows up as the next command running the
// new release, and failure is retried after autoInterval. The child
// cannot recurse: `jog update` dispatches straight to Run and never
// enters the notice machinery — and even if it did, the caller stamps
// AutoTriedAt before spawning, so a child's probe finds the throttle
// closed.
func spawnUpdate(exe string) {
	cmd := exec.Command(exe, "update")
	if err := cmd.Start(); err == nil {
		cmd.Process.Release()
	}
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
	if IsBrewInstall(exe) {
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
