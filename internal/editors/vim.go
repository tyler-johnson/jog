package editors

import (
	_ "embed"
	"os"
	"runtime"

	"github.com/tyler-johnson/jog/internal/install"
)

// vimDir is vim's user runtime root: ~/.vim everywhere except Windows,
// where stock vim reads ~/vimfiles instead.
func vimDir(elems ...string) (string, error) {
	root := ".vim"
	if runtime.GOOS == "windows" {
		root = "vimfiles"
	}
	return install.HomePath(append([]string{root}, elems...)...)
}

// nvimConfig is nvim's config root: $XDG_CONFIG_HOME/nvim when the
// variable is set (nvim honors it on every OS), else %LOCALAPPDATA%\nvim
// on Windows, else ~/.config/nvim.
func nvimConfig(elems ...string) (string, error) {
	if runtime.GOOS == "windows" && os.Getenv("XDG_CONFIG_HOME") == "" {
		return localAppData(append([]string{"nvim"}, elems...)...)
	}
	return xdgConfig(append([]string{"nvim"}, elems...)...)
}

// Vim and Neovim share one plugin file — the VimL is a strict common
// subset, and the has('nvim') switch inside it reports the right editor
// name to jog. Each install lands its own copy in that editor's stock
// plugin directory (loaded automatically at startup, plugin managers
// notwithstanding), so either uninstalls cleanly without the other.
//
//go:embed jog.vim
var vimAsset []byte

var vimEditor = editor{
	name:  "vim",
	title: "Vim",
	detect: func() bool {
		return onPath("vim") || exists(vimDir())
	},
	hookPath: func() (string, error) { return vimDir("plugin", "jog.vim") },
	asset:    vimAsset,
	notes: func() []string {
		return []string{
			"takes effect in new vim sessions (`:runtime plugin/jog.vim` arms this one)",
			"pre-8.0 vim has no jobs — the hook falls back to a backgrounded shell, still non-blocking",
		}
	},
}

var nvimEditor = editor{
	name:  "nvim",
	title: "Neovim",
	detect: func() bool {
		return onPath("nvim") || exists(nvimConfig())
	},
	hookPath: func() (string, error) { return nvimConfig("plugin", "jog.vim") },
	asset:    vimAsset,
	notes: func() []string {
		return []string{
			"takes effect in new nvim sessions (`:runtime plugin/jog.vim` arms this one)",
		}
	},
}
