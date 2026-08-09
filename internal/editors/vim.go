package editors

import (
	_ "embed"

	"github.com/tyler-johnson/jog/internal/install"
)

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
		return onPath("vim") || exists(install.HomePath(".vim"))
	},
	hookPath: func() (string, error) { return install.HomePath(".vim", "plugin", "jog.vim") },
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
		return onPath("nvim") || exists(xdgConfig("nvim"))
	},
	hookPath: func() (string, error) { return xdgConfig("nvim", "plugin", "jog.vim") },
	asset:    vimAsset,
	notes: func() []string {
		return []string{
			"takes effect in new nvim sessions (`:runtime plugin/jog.vim` arms this one)",
		}
	},
}
