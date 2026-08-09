package editors

import (
	_ "embed"
	"path/filepath"
	"runtime"

	"github.com/tyler-johnson/jog/internal/install"
)

// Sublime Text loads top-level .py files in Packages/User live — the
// plugin is active the moment the file lands, no restart. The user dir
// is per-OS; on linux ST4's directory is preferred unless only ST3's
// exists.
//
//go:embed jog.py
var sublimeAsset []byte

func sublimeUserDir() (string, error) {
	if runtime.GOOS == "darwin" {
		return install.HomePath("Library", "Application Support", "Sublime Text", "Packages", "User")
	}
	st4, err := xdgConfig("sublime-text")
	if err != nil {
		return "", err
	}
	if !install.FileExists(st4) {
		if st3, err := xdgConfig("sublime-text-3"); err == nil && install.FileExists(st3) {
			return filepath.Join(st3, "Packages", "User"), nil
		}
	}
	return filepath.Join(st4, "Packages", "User"), nil
}

var sublimeEditor = editor{
	name:  "sublime",
	title: "Sublime Text",
	detect: func() bool {
		return onPath("subl") || exists(sublimeUserDir())
	},
	hookPath: func() (string, error) {
		dir, err := sublimeUserDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "jog.py"), nil
	},
	asset: sublimeAsset,
	notes: func() []string {
		return []string{
			"active immediately — Sublime hot-reloads User plugins",
			"jog's absolute path is baked in — re-run install if you move jog",
		}
	},
}
