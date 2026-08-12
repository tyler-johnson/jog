package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
)

// EditorHook handles `jog editor-hook <editor> [file]` — the runtime entry
// `jog editors install` wires into an editor's on-save hook. Unlike `jog
// hook` there is no stdin protocol: the saved file's path arrives as an
// argument, and the repo is discovered from the file's own directory, so
// the editor's working directory never matters.
//
// Iron rule: ALWAYS exit 0, and NEVER write to stdout or stderr — hook
// output lands in the editor's UI (vim's cmdline, sublime's console), and
// a jog failure must never disturb a save. The editor name is provenance
// only, never validated: hooks wired by a newer jog keep snapshotting
// under an older one. Diagnostics only under JOG_DEBUG=1.
func EditorHook(editorName string, fileArgs []string) int {
	// One path is the only thing the extra args could be — rejoin them so
	// an editor that forgot to quote a path with spaces still works.
	file := strings.TrimSpace(strings.Join(fileArgs, " "))

	var abs, dir string
	if file != "" {
		a, err := filepath.Abs(file) // relative paths resolve against the editor's cwd — this process's cwd
		if err != nil {
			return hookDone("abs", err)
		}
		abs, dir = a, filepath.Dir(a)
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return hookDone("getwd", err)
		}
		dir = wd
	}

	repo, err := gitx.Discover(dir)
	if err != nil {
		return hookDone("discover", err) // outside a repo: the common, silent case
	}

	// Relativize against the toplevel for a readable timeline; a file
	// somehow outside the repo stays absolute, labeled honestly.
	rel := abs
	if abs != "" && repo.Top != "" {
		rel = relPath(repo.Top, abs)
	}

	res, err := snap.Take(repo, provenance.Save(editorName, rel))
	if err != nil {
		return hookDone("snapshot", err)
	}
	debugf("editor-hook: %+v", res)
	return 0
}
