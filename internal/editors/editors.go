// Package editors implements `jog editors` — install, uninstall, and list
// jog's post-save hooks for text editors (vim, emacs, VS Code, …).
//
// One surface per editor: a hook that runs `jog editor-hook <name>` after
// every save, so each save inside a git repo becomes a restorable
// snapshot. Unlike jog's other triggers this snapshots after the save —
// the saved state is the checkpoint; pre-save state is the editor's own
// undo. The mechanics are shared with `jog agents` (internal/install);
// per-editor facts are declarations, one file each — a new editor is one
// more such file plus its entry in the registry.
//
// install and uninstall take exactly one editor per invocation: every
// editor's integration has its own gotchas, and the install output is
// where they are taught — fanning out would bury them.
package editors

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// editor declares one supported text editor: how it is detected, where
// its jog save hook lives, and what the user must know after installing.
// The hook asset may carry install.JogSlot, filled with this machine's
// jog path at install time (GUI editors don't inherit the shell's PATH).
// hookInstall/hookUninstall/location default to managed-file mechanics on
// hookPath; editors whose hook is a config merge rather than a jog-owned
// file (jetbrains, kakoune's kakrc mode) override them wholesale.
type editor struct {
	name          string
	title         string      // display name for list output
	detect        func() bool // plausibly on this machine
	hookPath      func() (string, error)
	asset         []byte // managed hook file content (template)
	hookInstall   func() (string, bool, error)
	hookUninstall func() (string, bool, error)
	location      func() string   // "" when not installed
	notes         func() []string // how-it-works + caveats, printed on install
}

var registry = []editor{vimEditor, nvimEditor, emacsEditor, sublimeEditor,
	kakouneEditor, microEditor, vscodeEditor, jetbrainsEditor}

// Status is one editor's wiring, as doctor reports it.
type Status struct {
	Name     string
	Location string // "" when not installed
}

// Statuses reports every supported editor's wiring.
func Statuses() []Status {
	out := make([]Status, len(registry))
	for i, e := range registry {
		out[i] = Status{Name: e.name, Location: e.where()}
	}
	return out
}

func (e editor) doInstall() (string, bool, error) {
	if e.hookInstall != nil {
		return e.hookInstall()
	}
	path, err := e.hookPath()
	if err != nil {
		return "", false, err
	}
	return install.ManagedFile(path, install.Render(e.asset, install.JogPath()))
}

func (e editor) doUninstall() (string, bool, error) {
	if e.hookUninstall != nil {
		return e.hookUninstall()
	}
	path, err := e.hookPath()
	if err != nil {
		return "", false, err
	}
	return install.RemoveRenderedFile(path, e.asset)
}

func (e editor) where() string {
	if e.location != nil {
		return e.location()
	}
	if p, err := e.hookPath(); err == nil && install.FileExists(p) {
		return install.TildePath(p)
	}
	return ""
}

func (e editor) noteLines() []string {
	if e.notes == nil {
		return nil
	}
	return e.notes()
}

func editorNames() string {
	names := make([]string, len(registry))
	for i, e := range registry {
		names[i] = e.name
	}
	return strings.Join(names, ", ")
}

const usage = "jog: usage: jog editors install|uninstall|list [<editor>]"

// Run is the `jog editors` command: parse the action and editor names,
// then dispatch. install and uninstall take exactly one editor —
// list takes none (all) or any number to narrow.
func Run(args []string) int {
	action := ""
	var names []string
	for _, a := range args {
		switch a {
		case "install", "uninstall", "list":
			if action != "" {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
			action = a
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
			names = append(names, a)
		}
	}
	if action == "" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	var targets []editor
	for _, want := range names {
		found := false
		for _, e := range registry {
			if e.name == want {
				targets = append(targets, e)
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "jog: unknown editor %q (supported: %s)\n", want, editorNames())
			return 2
		}
	}

	switch action {
	case "list":
		if len(targets) == 0 {
			targets = registry
		}
		return list(targets)
	default:
		if len(targets) == 0 {
			fmt.Fprintf(os.Stderr, "jog: editors %s takes exactly one editor name — `jog editors list` shows the supported names\n", action)
			return 2
		}
		if len(targets) > 1 {
			fmt.Fprintf(os.Stderr, "jog: editors %s takes one editor at a time — run it once per editor\n", action)
			return 2
		}
		if action == "install" {
			return installEditor(targets[0])
		}
		return uninstallEditor(targets[0])
	}
}

// list groups output per editor, mirroring `jog agents list`. Editors
// with no binary, no config, and no jog hook collapse to a single quiet
// line — their absence is unremarkable.
func list(targets []editor) int {
	for i, e := range targets {
		install.Header(e.name, e.title, i)
		loc := e.where()
		if !e.detect() && loc == "" {
			fmt.Println(install.StyleDim.Render("  not found on this machine — `jog editors install " + e.name + "` forces it"))
			continue
		}
		if loc != "" {
			fmt.Printf("  %-6s %s  %s\n", "hook", install.StyleGood.Render("✓ installed"), loc)
		} else {
			fmt.Printf("  %-6s %s\n", "hook", install.StyleDim.Render("· not installed"))
		}
	}
	return 0
}

// installEditor wires one editor and teaches its integration: the notes
// print on every run — including the already-up-to-date one — because
// re-running install is how you re-read the caveats. Naming the editor
// was explicit, so detection never gates an install.
func installEditor(e editor) int {
	install.Header(e.name, e.title, 0)
	msg, did, err := e.doInstall()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %s: %v\n", e.name, err)
		return 1
	}
	install.Row("hook", msg, did)
	note := func(n string) { fmt.Println(install.StyleDim.Render("  · " + n)) }
	note("every save inside a git repo becomes a snapshot — `jog log <file>` lists them")
	for _, n := range e.noteLines() {
		note(n)
	}
	if _, err := exec.LookPath("jog"); err != nil {
		note("jog is not on PATH — the hook will not fire until it is")
	}
	if did {
		fmt.Println()
		fmt.Printf("`jog editors uninstall %s` removes it; `jog doctor` verifies the wiring.\n", e.name)
	}
	return 0
}

func uninstallEditor(e editor) int {
	install.Header(e.name, e.title, 0)
	msg, did, err := e.doUninstall()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %s: %v\n", e.name, err)
		return 1
	}
	install.Row("hook", msg, did)
	return 0
}

// onPath reports whether a binary resolves on PATH.
func onPath(bins ...string) bool {
	for _, b := range bins {
		if _, err := exec.LookPath(b); err == nil {
			return true
		}
	}
	return false
}

// xdgConfig joins elems under $XDG_CONFIG_HOME, defaulting to ~/.config.
func xdgConfig(elems ...string) (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(append([]string{x}, elems...)...), nil
	}
	return install.HomePath(append([]string{".config"}, elems...)...)
}

func exists(path string, err error) bool {
	return err == nil && install.FileExists(path)
}
