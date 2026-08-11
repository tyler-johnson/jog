package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testCacheDir points os.UserCacheDir at a scratch directory for the
// duration of the test, per-OS, and blinds git config so the machine's
// real jog.* settings can't leak into config-reading paths.
func testCacheDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LocalAppData", dir)
	case "darwin":
		t.Setenv("HOME", dir)
	default:
		t.Setenv("XDG_CACHE_HOME", dir)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func TestStateRoundTrip(t *testing.T) {
	testCacheDir(t)
	if s := loadState(); !s.CheckedAt.IsZero() || s.Latest != "" || s.Notified != "" {
		t.Errorf("empty cache should load the zero state, got %+v", s)
	}
	want := checkState{
		CheckedAt:    time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Latest:       "v1.5.0",
		Notified:     "v1.4.0",
		AutoTriedAt:  time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
		IntervalSecs: 7 * 24 * 3600,
	}
	if err := saveState(want); err != nil {
		t.Fatal(err)
	}
	got := loadState()
	if !got.CheckedAt.Equal(want.CheckedAt) || got.Latest != want.Latest ||
		got.Notified != want.Notified || !got.AutoTriedAt.Equal(want.AutoTriedAt) ||
		got.IntervalSecs != want.IntervalSecs {
		t.Errorf("round trip: got %+v want %+v", got, want)
	}

	// The write is temp+rename: nothing but the state file may remain.
	p, err := statePath()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "update.json" {
		t.Errorf("state dir not clean: %v", entries)
	}

	// A corrupt cache reads as "never checked", never an error.
	if err := os.WriteFile(p, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := loadState(); s.Latest != "" || !s.CheckedAt.IsZero() {
		t.Errorf("corrupt cache should load the zero state, got %+v", s)
	}
}

func TestNeedsCheck(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		s    checkState
		want bool
	}{
		{"never checked", checkState{}, true},
		{"fresh", checkState{CheckedAt: now.Add(-time.Hour)}, false},
		{"twenty hours", checkState{CheckedAt: now.Add(-20 * time.Hour)}, false},
		{"two days", checkState{CheckedAt: now.Add(-2 * 24 * time.Hour)}, true},
		{"weekly cadence, two days", checkState{CheckedAt: now.Add(-2 * 24 * time.Hour), IntervalSecs: 7 * 24 * 3600}, false},
		{"weekly cadence, eight days", checkState{CheckedAt: now.Add(-8 * 24 * time.Hour), IntervalSecs: 7 * 24 * 3600}, true},
		{"disabled, fresh", checkState{CheckedAt: now.Add(-time.Hour), IntervalSecs: -1}, false},
		// Disabled re-verifies the config on the default cadence, so a
		// re-enable is noticed within a day.
		{"disabled, two days", checkState{CheckedAt: now.Add(-2 * 24 * time.Hour), IntervalSecs: -1}, true},
	} {
		if got := needsCheck(tt.s, now); got != tt.want {
			t.Errorf("%s: needsCheck = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestConfigCheckInterval(t *testing.T) {
	testCacheDir(t)
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	set := func(v string) {
		t.Helper()
		if err := exec.Command("git", "config", "--file", cfg, "jog.updateCheck", v).Run(); err != nil {
			t.Fatal(err)
		}
	}

	if iv, enabled := configCheckInterval(); !enabled || iv != defaultCheckInterval {
		t.Errorf("unset: got (%v, %v), want default daily", iv, enabled)
	}
	set("true")
	if iv, enabled := configCheckInterval(); !enabled || iv != defaultCheckInterval {
		t.Errorf("true: got (%v, %v), want default daily", iv, enabled)
	}
	for _, off := range []string{"false", "never", "off"} {
		set(off)
		if _, enabled := configCheckInterval(); enabled {
			t.Errorf("%s: still enabled", off)
		}
	}
	set("2.weeks")
	if iv, enabled := configCheckInterval(); !enabled || iv < 13*24*time.Hour || iv > 15*24*time.Hour {
		t.Errorf("2.weeks: got (%v, %v), want ~14 days", iv, enabled)
	}
	// Bare numbers are seconds, never approxidate's idea of a date.
	set("3600")
	if iv, enabled := configCheckInterval(); !enabled || iv != time.Hour {
		t.Errorf("3600: got (%v, %v), want hourly", iv, enabled)
	}
	set("30")
	if iv, enabled := configCheckInterval(); !enabled || iv != minCheckInterval {
		t.Errorf("30: got (%v, %v), want the floor", iv, enabled)
	}
	set("0")
	if _, enabled := configCheckInterval(); enabled {
		t.Error("0: still enabled")
	}
	// Approxidate reads almost anything as a date; a value that lands at
	// "now" must floor to the minimum, never a per-command check.
	set("now")
	if iv, enabled := configCheckInterval(); !enabled || iv < minCheckInterval {
		t.Errorf("now: got (%v, %v), want the floor", iv, enabled)
	}
}

func TestPendingNotice(t *testing.T) {
	newer := checkState{Latest: "v1.5.0"}
	for _, tt := range []struct {
		name    string
		s       checkState
		version string
		exe     string
		tty     bool
		enabled bool
		want    string
	}{
		{"newer", newer, "v1.4.0", "/usr/local/bin/jog", true, true,
			"jog: v1.5.0 is available (running v1.4.0) — update with: jog update"},
		{"equal", checkState{Latest: "v1.4.0"}, "v1.4.0", "/usr/local/bin/jog", true, true, ""},
		{"older cache", checkState{Latest: "v1.3.0"}, "v1.4.0", "/usr/local/bin/jog", true, true, ""},
		{"already notified", checkState{Latest: "v1.5.0", Notified: "v1.5.0"}, "v1.4.0", "/usr/local/bin/jog", true, true, ""},
		{"notified an older release", checkState{Latest: "v1.5.0", Notified: "v1.4.1"}, "v1.4.0", "/usr/local/bin/jog", true, true,
			"jog: v1.5.0 is available (running v1.4.0) — update with: jog update"},
		{"not a tty", newer, "v1.4.0", "/usr/local/bin/jog", false, true, ""},
		{"disabled", newer, "v1.4.0", "/usr/local/bin/jog", true, false, ""},
		{"source build", newer, "(devel)", "/usr/local/bin/jog", true, true, ""},
		{"empty cache", checkState{}, "v1.4.0", "/usr/local/bin/jog", true, true, ""},
		{"garbage latest", checkState{Latest: "nonsense"}, "v1.4.0", "/usr/local/bin/jog", true, true, ""},
		{"brew install", newer, "v1.4.0", "/opt/homebrew/Cellar/jog/1.4.0/bin/jog", true, true,
			"jog: v1.5.0 is available (running v1.4.0) — update with: brew upgrade jog"},
	} {
		if got := pendingNotice(tt.s, tt.version, tt.exe, tt.tty, tt.enabled); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestAutoUpdateDue(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	newer := checkState{Latest: "v1.5.0"}
	for _, tt := range []struct {
		name    string
		s       checkState
		version string
		exe     string
		want    bool
	}{
		{"newer, never tried", newer, "v1.4.0", "/usr/local/bin/jog", true},
		{"equal", checkState{Latest: "v1.4.0"}, "v1.4.0", "/usr/local/bin/jog", false},
		{"older cache", checkState{Latest: "v1.3.0"}, "v1.4.0", "/usr/local/bin/jog", false},
		{"tried an hour ago", checkState{Latest: "v1.5.0", AutoTriedAt: now.Add(-time.Hour)}, "v1.4.0", "/usr/local/bin/jog", false},
		{"tried two days ago", checkState{Latest: "v1.5.0", AutoTriedAt: now.Add(-48 * time.Hour)}, "v1.4.0", "/usr/local/bin/jog", true},
		{"notified is irrelevant", checkState{Latest: "v1.5.0", Notified: "v1.5.0"}, "v1.4.0", "/usr/local/bin/jog", true},
		{"brew install", newer, "v1.4.0", "/opt/homebrew/Cellar/jog/1.4.0/bin/jog", false},
		{"source build", newer, "(devel)", "/usr/local/bin/jog", false},
		{"empty cache", checkState{}, "v1.4.0", "/usr/local/bin/jog", false},
		{"garbage latest", checkState{Latest: "nonsense"}, "v1.4.0", "/usr/local/bin/jog", false},
	} {
		if got := autoUpdateDue(tt.s, tt.version, tt.exe, now); got != tt.want {
			t.Errorf("%s: autoUpdateDue = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMarkNotified(t *testing.T) {
	testCacheDir(t)
	MarkNotified() // empty cache: a no-op, not a crash
	if err := saveState(checkState{Latest: "v1.5.0"}); err != nil {
		t.Fatal(err)
	}
	MarkNotified()
	if s := loadState(); s.Notified != "v1.5.0" {
		t.Errorf("Notified = %q, want v1.5.0", s.Notified)
	}
}

func TestRefreshCache(t *testing.T) {
	testCacheDir(t)
	srv := serveRelease(t, "v9.9.9", "jog_9.9.9_linux_amd64.tar.gz", []byte("x"))
	u, _, _ := testUpdater(t, srv, "/nonexistent/jog", "linux", "v1.0.0")
	u.RefreshCache()
	s := loadState()
	if s.Latest != "v9.9.9" || s.CheckedAt.IsZero() {
		t.Errorf("after a good check: %+v", s)
	}

	// A failed check advances CheckedAt but keeps the old latest, so an
	// offline machine doesn't spawn a checker on every command.
	old := s.CheckedAt.Add(-30 * 24 * time.Hour)
	if err := saveState(checkState{CheckedAt: old, Latest: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	u2, _, _ := testUpdater(t, broken, "/nonexistent/jog", "linux", "v1.0.0")
	u2.RefreshCache()
	s = loadState()
	if s.Latest != "v9.9.9" || !s.CheckedAt.After(old) {
		t.Errorf("after a failed check: %+v (old checked_at %v)", s, old)
	}
}

func TestCheckStatus(t *testing.T) {
	testCacheDir(t)
	if got := CheckStatus("(devel)"); !strings.Contains(got, "source build") {
		t.Errorf("source build: %q", got)
	}
	if got := CheckStatus("v1.4.0"); got != "no check yet" {
		t.Errorf("no cache: %q", got)
	}
	if err := saveState(checkState{CheckedAt: time.Now(), Latest: "v1.5.0"}); err != nil {
		t.Fatal(err)
	}
	if got := CheckStatus("v1.4.0"); got != "v1.5.0 available — jog update" {
		t.Errorf("newer cached: %q", got)
	}
	if got := CheckStatus("v1.5.0"); got != "up to date (v1.5.0)" {
		t.Errorf("current: %q", got)
	}
}
