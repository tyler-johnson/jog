package editors

import (
	_ "embed"
	"path/filepath"

	"github.com/tyler-johnson/jog/internal/install"
)

// Emacs is the one manual-step editor: it has no drop-in autoload
// directory, and jog will not edit init files it cannot merge surgically
// — elisp is a program (and under Doom or a tangled literate config,
// init.el isn't even the user's file). So install writes jog.el and the
// notes print the one load line to add; its noerror flag keeps the line
// inert after uninstall.
//
//go:embed jog.el
var emacsAsset []byte

// emacsHookPath prefers an existing ~/.emacs.d, then an existing XDG
// config dir (Emacs 27+), and defaults to creating ~/.emacs.d — every
// Emacs reads that.
func emacsHookPath() (string, error) {
	dotd, err := install.HomePath(".emacs.d")
	if err != nil {
		return "", err
	}
	if install.FileExists(dotd) {
		return filepath.Join(dotd, "jog.el"), nil
	}
	if x, err := xdgConfig("emacs"); err == nil && install.FileExists(x) {
		return filepath.Join(x, "jog.el"), nil
	}
	return filepath.Join(dotd, "jog.el"), nil
}

var emacsEditor = editor{
	name:  "emacs",
	title: "Emacs",
	detect: func() bool {
		return onPath("emacs") || exists(install.HomePath(".emacs.d")) || exists(xdgConfig("emacs"))
	},
	hookPath: emacsHookPath,
	asset:    emacsAsset,
	notes: func() []string {
		loadPath := "~/.emacs.d/jog.el"
		if p, err := emacsHookPath(); err == nil {
			loadPath = install.TildePath(p)
		}
		return []string{
			`one manual step — add this line to your init file: (load "` + loadPath + `" t)`,
			"Doom or Spacemacs: put it in config.el / dotspacemacs/user-config instead",
			"takes effect after a restart (the t keeps emacs quiet if the file is ever removed)",
			"jog's absolute path is baked in — re-run install if you move jog",
		}
	},
}
