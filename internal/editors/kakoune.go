package editors

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// Kakoune has a real drop-in directory — but a trap: kak loads
// ~/.config/kak/autoload INSTEAD OF its runtime autoload when the user
// dir exists, so creating it would silently disable every built-in
// script (filetypes, highlighting, tools). jog never creates it: the
// hook drops into autoload/ only when the user already has one,
// otherwise it lands at ~/.config/kak/jog.kak with one marked source
// line appended to kakrc. kakrc is line-oriented command text, so adding
// and removing exactly the marked line is as surgical as the agents'
// JSON wiring.
//
//go:embed jog.kak
var kakouneAsset []byte

// kakrcMarker identifies the one line jog owns in kakrc; uninstall
// removes exactly the lines carrying it and nothing else.
const kakrcMarker = "# jog — added by `jog editors install`"

const kakrcLine = `source "%val{config}/jog.kak" ` + kakrcMarker

func kakouneAutoloadDir() (string, error) { return xdgConfig("kak", "autoload") }

func kakouneInstall() (string, bool, error) {
	if auto, err := kakouneAutoloadDir(); err == nil && install.FileExists(auto) {
		return install.ManagedFile(filepath.Join(auto, "jog.kak"), kakouneAsset)
	}
	path, err := xdgConfig("kak", "jog.kak")
	if err != nil {
		return "", false, err
	}
	msg, did, err := install.ManagedFile(path, kakouneAsset)
	if err != nil {
		return "", false, err
	}
	kakrc, err := xdgConfig("kak", "kakrc")
	if err != nil {
		return "", false, err
	}
	added, err := ensureKakrcLine(kakrc)
	if err != nil {
		return "", false, err
	}
	if added {
		return msg + ", sourced from " + kakrc, true, nil
	}
	return msg, did, nil
}

// ensureKakrcLine appends jog's source line unless kakrc already carries
// it (marker or path — however the user reshaped the line).
func ensureKakrcLine(kakrc string) (bool, error) {
	b, err := os.ReadFile(kakrc)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if strings.Contains(string(b), kakrcMarker) || strings.Contains(string(b), "%val{config}/jog.kak") {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(kakrc), 0o755); err != nil {
		return false, err
	}
	content := string(b)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += kakrcLine + "\n"
	return true, os.WriteFile(kakrc, []byte(content), 0o644)
}

func kakouneUninstall() (string, bool, error) {
	// Whichever mode installed it — and the kakrc line regardless, since
	// the user may have moved the file between modes.
	var msgs []string
	did := false
	if auto, err := kakouneAutoloadDir(); err == nil {
		p := filepath.Join(auto, "jog.kak")
		if install.FileExists(p) {
			msg, d, err := install.RemoveManagedFile(p, kakouneAsset)
			if err != nil {
				return "", false, err
			}
			msgs, did = append(msgs, msg), did || d
		}
	}
	if p, err := xdgConfig("kak", "jog.kak"); err == nil && install.FileExists(p) {
		msg, d, err := install.RemoveManagedFile(p, kakouneAsset)
		if err != nil {
			return "", false, err
		}
		msgs, did = append(msgs, msg), did || d
	}
	if kakrc, err := xdgConfig("kak", "kakrc"); err == nil {
		removed, err := removeKakrcLine(kakrc)
		if err != nil {
			return "", false, err
		}
		if removed {
			msgs, did = append(msgs, "removed the source line from "+kakrc), true
		}
	}
	if len(msgs) == 0 {
		return "not installed — nothing to remove", false, nil
	}
	return strings.Join(msgs, "; "), did, nil
}

func removeKakrcLine(kakrc string) (bool, error) {
	b, err := os.ReadFile(kakrc)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	kept := lines[:0]
	removed := false
	for _, l := range lines {
		if strings.Contains(l, kakrcMarker) {
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	if !removed {
		return false, nil
	}
	return true, os.WriteFile(kakrc, []byte(strings.Join(kept, "\n")), 0o644)
}

func kakouneLocation() string {
	if auto, err := kakouneAutoloadDir(); err == nil {
		if p := filepath.Join(auto, "jog.kak"); install.FileExists(p) {
			return install.TildePath(p)
		}
	}
	if p, err := xdgConfig("kak", "jog.kak"); err == nil && install.FileExists(p) {
		return install.TildePath(p)
	}
	return ""
}

var kakouneEditor = editor{
	name:  "kakoune",
	title: "Kakoune",
	detect: func() bool {
		return onPath("kak") || exists(xdgConfig("kak"))
	},
	hookInstall:   kakouneInstall,
	hookUninstall: kakouneUninstall,
	location:      kakouneLocation,
	notes: func() []string {
		n := []string{"takes effect in new kak sessions"}
		if auto, err := kakouneAutoloadDir(); err == nil && !install.FileExists(auto) {
			n = append(n, "jog did not create ~/.config/kak/autoload — an autoload dir replaces kakoune's built-in scripts, so the hook is sourced from kakrc instead")
		}
		return n
	},
}
