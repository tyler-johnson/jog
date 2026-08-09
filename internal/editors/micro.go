package editors

import (
	_ "embed"
)

// Micro loads every .lua file under plug/<name>/ at startup; a manually
// dropped local plugin needs no manifest (repo.json is plugin-channel
// machinery). onSave is micro's post-save action callback.
//
//go:embed jog.lua
var microAsset []byte

var microEditor = editor{
	name:  "micro",
	title: "Micro",
	detect: func() bool {
		return onPath("micro") || exists(xdgConfig("micro"))
	},
	hookPath: func() (string, error) { return xdgConfig("micro", "plug", "jog", "jog.lua") },
	asset:    microAsset,
	notes: func() []string {
		return []string{
			"takes effect in new micro sessions",
		}
	},
}
