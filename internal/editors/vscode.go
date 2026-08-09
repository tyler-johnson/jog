package editors

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// VS Code's marketplace is skipped entirely: a correctly-named folder
// dropped into ~/.vscode/extensions is picked up at the next full
// restart. The extension is two files of plain JavaScript — no build
// step, no publisher account — and its only logic is one save listener
// spawning jog; everything real stays in the binary, so the shim almost
// never changes.
//
//go:embed vscode_package.json
var vscodePackage []byte

//go:embed vscode_extension.js
var vscodeJS []byte

// vscodeExtDirs returns the extension directories this machine's VS
// Codes actually read (folder name must be publisher.name-version): the
// desktop app's ~/.vscode/extensions, and the Remote-SSH server's
// ~/.vscode-server/extensions — a remote window's extension host loads
// ONLY from the latter, so a machine that agents SSH into needs the hook
// there. Existing roots are used; a machine with neither gets the
// desktop default.
func vscodeExtDirs() ([]string, error) {
	var dirs []string
	for _, root := range []string{".vscode", ".vscode-server"} {
		p, err := install.HomePath(root)
		if err != nil {
			return nil, err
		}
		if install.FileExists(p) {
			dirs = append(dirs, filepath.Join(p, "extensions", "jog.jog-0.0.1"))
		}
	}
	if len(dirs) == 0 {
		p, err := install.HomePath(".vscode", "extensions", "jog.jog-0.0.1")
		if err != nil {
			return nil, err
		}
		dirs = []string{p}
	}
	return dirs, nil
}

func vscodeInstall() (string, bool, error) {
	dirs, err := vscodeExtDirs()
	if err != nil {
		return "", false, err
	}
	var msgs []string
	did := false
	for _, dir := range dirs {
		existed := install.FileExists(filepath.Join(dir, "package.json"))
		_, d1, err := install.ManagedFile(filepath.Join(dir, "package.json"), vscodePackage)
		if err != nil {
			return "", false, err
		}
		_, d2, err := install.ManagedFile(filepath.Join(dir, "extension.js"), install.Render(vscodeJS, install.JogPath()))
		if err != nil {
			return "", false, err
		}
		switch {
		case !d1 && !d2:
			msgs = append(msgs, "already up to date — "+dir)
		case existed:
			msgs, did = append(msgs, "updated — "+dir), true
		default:
			msgs, did = append(msgs, "installed — "+dir), true
		}
	}
	return strings.Join(msgs, "; "), did, nil
}

// vscodeUninstall verifies each root's files before touching any, so an
// edited extension is refused whole rather than half-removed. A copy
// VS Code itself placed on a remote (its install-on-remote flow) carries
// another machine's baked path — RenderedMatch accepts any rendering, so
// those are jog's to delete too.
func vscodeUninstall() (string, bool, error) {
	dirs, err := vscodeExtDirs()
	if err != nil {
		return "", false, err
	}
	var present []string
	for _, dir := range dirs {
		pj, ej := filepath.Join(dir, "package.json"), filepath.Join(dir, "extension.js")
		if !install.FileExists(pj) && !install.FileExists(ej) {
			continue
		}
		present = append(present, dir)
		for path, tmpl := range map[string][]byte{pj: vscodePackage, ej: vscodeJS} {
			b, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", false, err
			}
			if !install.RenderedMatch(b, tmpl) {
				return "", false, fmt.Errorf("%s differs from what jog installs — it may carry your edits, so jog won't remove it (delete the directory yourself if you mean to)", path)
			}
		}
	}
	if len(present) == 0 {
		return "not installed — nothing to remove", false, nil
	}
	for _, dir := range present {
		for _, f := range []string{"package.json", "extension.js"} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
				return "", false, err
			}
		}
		os.Remove(dir) // best-effort: fails harmlessly if not empty
	}
	return "removed — " + strings.Join(present, "; "), true, nil
}

var vscodeEditor = editor{
	name:  "vscode",
	title: "VS Code",
	detect: func() bool {
		return onPath("code") || exists(install.HomePath(".vscode")) || exists(install.HomePath(".vscode-server"))
	},
	hookInstall:   vscodeInstall,
	hookUninstall: vscodeUninstall,
	location: func() string {
		dirs, err := vscodeExtDirs()
		if err != nil {
			return ""
		}
		var found []string
		for _, dir := range dirs {
			if install.FileExists(filepath.Join(dir, "extension.js")) {
				found = append(found, install.TildePath(dir))
			}
		}
		return strings.Join(found, ", ")
	},
	notes: func() []string {
		n := []string{
			"restart VS Code fully (remote windows: reload), then check the Extensions view for 'jog'",
			"jog's absolute path is baked in, with a PATH fallback — re-run install if you move jog",
		}
		if server, err := install.HomePath(".vscode-server"); err == nil && install.FileExists(server) {
			n = append(n, "Remote-SSH covered: the hook also landed in ~/.vscode-server/extensions — always run this install on the machine the window is connected to")
		} else {
			n = append(n, "Remote-SSH windows run their extensions on the remote machine — run this install there too")
		}
		return n
	},
}
