package cli

// The automatic trim. Snapshots age out via `jog trim`, and nobody
// should have to remember to run it: like the update check, trimming
// rides the passthrough — a detached background `jog trim` runs on the
// jog.autoTrim cadence (daily by default), per repository. The hot path
// pays one file read to decide nothing is due; the config is consulted
// only when the stamp says it might be.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyler-johnson/jog/internal/cadence"
	"github.com/tyler-johnson/jog/internal/gitx"
)

// trimState is the per-repo stamp file. TrimmedAt is the last time a
// trim ran (or auto-trim decided it was disabled); IntervalSecs caches
// the parsed jog.autoTrim so staleness is decided from the file alone —
// 0 = never read (default cadence), -1 = disabled.
type trimState struct {
	TrimmedAt    time.Time `json:"trimmed_at"`
	IntervalSecs int64     `json:"interval_secs,omitempty"`
}

// trimStatePath is <common git dir>/jog/autotrim.json. The common dir,
// not the per-worktree one: chains are repo-wide, so linked worktrees
// share one clock instead of each running their own daily trim.
func trimStatePath(repo *gitx.Repo) string {
	dir := repo.GitDir
	// gitrepository-layout(5): a linked worktree's git dir holds a
	// `commondir` file pointing at the main repository's.
	if b, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
		p := strings.TrimSpace(string(b))
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		dir = filepath.Clean(p)
	}
	return filepath.Join(dir, "jog", "autotrim.json")
}

// loadTrimState returns the zero state on any error: a missing or
// corrupt stamp means "never trimmed", which only makes the next trim
// happen sooner.
func loadTrimState(repo *gitx.Repo) trimState {
	b, err := os.ReadFile(trimStatePath(repo))
	if err != nil {
		return trimState{}
	}
	var s trimState
	if json.Unmarshal(b, &s) != nil {
		return trimState{}
	}
	return s
}

// saveTrimState writes atomically — temp file plus rename in the same
// directory — so a reader never sees a half-written stamp.
func saveTrimState(repo *gitx.Repo, s trimState) error {
	p := trimStatePath(repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".autotrim-*")
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

// configTrimInterval reads jog.autoTrim through the repo — local config
// wins, so a huge repo can run on its own cadence. Shares the update
// check's value language (see internal/cadence).
func configTrimInterval(repo *gitx.Repo) (time.Duration, bool) {
	return cadence.Read(func(typeFlags ...string) (string, error) {
		args := append(append([]string{"config"}, typeFlags...), "--get", "jog.autoTrim")
		return repo.RunRead(args...)
	})
}

// maybeSpawnTrim starts a detached `jog trim` in this repo when the
// stamp is stale and auto-trim is enabled. Called from human-facing
// commands that already hold a repo — never agent or editor hooks,
// with one deliberate exception: shell-hook, so a preexec-only setup
// (no alias, no human-facing jog commands) still maintains itself. The
// spawn is silent and detached, so the iron rule holds. The stamp advances
// before the spawn, so a failing trim retries on the cadence, not on
// every command; concurrent racers are harmless besides (trim's ref
// writes are CAS-guarded). The child is never waited on and its output
// goes nowhere: success is old snapshots quietly gone.
func maybeSpawnTrim(repo *gitx.Repo) {
	// CI clones are ephemeral; nothing there is worth a background spawn
	// (same gate as the update check).
	if os.Getenv("CI") != "" {
		return
	}
	s := loadTrimState(repo)
	now := time.Now()
	if now.Sub(s.TrimmedAt) < cadence.Interval(s.IntervalSecs) {
		return
	}
	// The cached cadence says a trim is due — now the config gets its
	// say, and whatever it says is cached so the next command is back to
	// one file read.
	iv, enabled := configTrimInterval(repo)
	s.IntervalSecs = cadence.Encode(iv, enabled)
	if !enabled {
		// Disabled: stamp so this config re-read itself runs at most on
		// the default cadence.
		s.TrimmedAt = now
		saveTrimState(repo, s)
		return
	}
	if now.Sub(s.TrimmedAt) < iv {
		// The live cadence is longer than the cached one — not due after
		// all.
		saveTrimState(repo, s)
		return
	}
	s.TrimmedAt = now
	saveTrimState(repo, s)
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "trim")
	cmd.Dir = repo.WorkDir
	if err := cmd.Start(); err == nil {
		cmd.Process.Release()
	}
}

// stampTrim records that a trim ran now — every completed `jog trim`
// resets the clock, so a manual run buys a full quiet interval.
func stampTrim(repo *gitx.Repo) {
	s := loadTrimState(repo)
	s.TrimmedAt = time.Now()
	saveTrimState(repo, s)
}

// syncTrimInterval re-reads jog.autoTrim into the current repo's stamp.
// `jog config` calls this after changing the setting so a new cadence
// (or a re-enable) applies now, not at the next trim under the old one.
// Outside a repo (a --global set, say) there is nothing to stamp; other
// repos converge as their own cached cadences fire.
func syncTrimInterval() {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	repo, err := gitx.Discover(wd)
	if err != nil {
		return
	}
	s := loadTrimState(repo)
	iv, enabled := configTrimInterval(repo)
	s.IntervalSecs = cadence.Encode(iv, enabled)
	saveTrimState(repo, s)
}
