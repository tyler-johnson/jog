package editors

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

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

// The folder name must be publisher.name-version.
func vscodeExtDir() (string, error) {
	return install.HomePath(".vscode", "extensions", "jog.jog-0.0.1")
}

func vscodeInstall() (string, bool, error) {
	dir, err := vscodeExtDir()
	if err != nil {
		return "", false, err
	}
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
		return "already up to date — " + dir, false, nil
	case existed:
		return "updated — " + dir, true, nil
	default:
		return "installed — " + dir, true, nil
	}
}

// vscodeUninstall verifies both files before touching either, so an
// edited extension is refused whole rather than half-removed.
func vscodeUninstall() (string, bool, error) {
	dir, err := vscodeExtDir()
	if err != nil {
		return "", false, err
	}
	pj, ej := filepath.Join(dir, "package.json"), filepath.Join(dir, "extension.js")
	if !install.FileExists(pj) && !install.FileExists(ej) {
		return "not installed — nothing to remove", false, nil
	}
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
	if err := os.Remove(pj); err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	if err := os.Remove(ej); err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	os.Remove(dir) // best-effort: fails harmlessly if not empty
	return "removed — " + dir, true, nil
}

var vscodeEditor = editor{
	name:  "vscode",
	title: "VS Code",
	detect: func() bool {
		return onPath("code") || exists(install.HomePath(".vscode"))
	},
	hookInstall:   vscodeInstall,
	hookUninstall: vscodeUninstall,
	location: func() string {
		if dir, err := vscodeExtDir(); err == nil && install.FileExists(filepath.Join(dir, "extension.js")) {
			return install.TildePath(dir)
		}
		return ""
	},
	notes: func() []string {
		return []string{
			"restart VS Code fully, then check the Extensions view for 'jog'",
			"jog's absolute path is baked in — re-run install if you move jog",
			"remote windows (SSH, WSL, containers) are not covered — the extension host there is remote",
		}
	},
}
