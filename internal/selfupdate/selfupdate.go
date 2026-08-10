// Package selfupdate implements `jog update`: replace the running binary
// with the latest GitHub release, sha256-verified against the release's
// checksums.txt. Only installs that came from a release archive update
// this way — source builds and Homebrew installs are recognized and
// pointed at their own update commands instead of being overwritten.
package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const usageLine = "jog: usage: jog update"

// Run is the `jog update` entry point. version is the module version Go
// embedded at build time ("vX.Y.Z" for tagged builds, "(devel)" or a
// pseudo-version otherwise).
func Run(args []string, version string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, usageLine)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: cannot locate the running binary: %v\n", err)
		return 1
	}
	// A symlinked ~/.local/bin/jog must update the binary it points at,
	// not turn the symlink into a regular file.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	u := &Updater{
		Client:  &http.Client{Timeout: 3 * time.Minute},
		APIBase: "https://api.github.com",
		Exe:     exe,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Version: version,
		Out:     os.Stdout,
		Err:     os.Stderr,
	}
	return u.Run()
}

// Updater carries everything Run needs, injectable so tests can point it
// at an httptest server and a scratch "executable".
type Updater struct {
	Client  *http.Client
	APIBase string // "https://api.github.com" in production
	Exe     string // resolved path of the binary to replace
	GOOS    string
	GOARCH  string
	Version string // current version, "vX.Y.Z"
	Out     io.Writer
	Err     io.Writer
}

func (u *Updater) Run() int {
	if msg := classifyInstall(u.Version, u.Exe); msg != "" {
		fmt.Fprintln(u.Err, "jog: "+msg)
		return 1
	}
	rel, err := u.latest()
	if err != nil {
		fmt.Fprintf(u.Err, "jog: %v\n", err)
		return 1
	}
	if !releaseVersion.MatchString(rel.TagName) {
		fmt.Fprintf(u.Err, "jog: unexpected release tag %q\n", rel.TagName)
		return 1
	}
	if !isNewer(u.Version, rel.TagName) {
		fmt.Fprintf(u.Out, "already up to date (%s)\n", u.Version)
		return 0
	}
	bin, sums, err := pickAsset(rel.Assets, rel.TagName, u.GOOS, u.GOARCH)
	if err != nil {
		fmt.Fprintf(u.Err, "jog: %v\n", err)
		return 1
	}
	sumsBody, err := u.fetchAll(sums.URL)
	if err != nil {
		fmt.Fprintf(u.Err, "jog: %v\n", err)
		return 1
	}
	want := parseChecksums(string(sumsBody))[bin.Name]
	if want == "" {
		fmt.Fprintf(u.Err, "jog: checksums.txt has no entry for %s\n", bin.Name)
		return 1
	}

	// The temp files live next to the binary: creating them doubles as
	// the writability check, and the final rename stays on one filesystem
	// so it is atomic.
	dir := filepath.Dir(u.Exe)
	archive, got, err := u.download(bin.URL, dir)
	if err != nil {
		if os.IsPermission(err) {
			fmt.Fprintf(u.Err, "jog: cannot write %s — re-run with the needed permissions (sudo), or reinstall with the install script\n", dir)
		} else {
			fmt.Fprintf(u.Err, "jog: %v\n", err)
		}
		return 1
	}
	defer os.Remove(archive)
	if got != want {
		fmt.Fprintf(u.Err, "jog: checksum mismatch for %s — refusing to install (corrupt or tampered download)\n", bin.Name)
		return 1
	}

	binName := "jog"
	if u.GOOS == "windows" {
		binName += ".exe"
	}
	newBin := archive + ".bin"
	if err := extractBinary(archive, bin.Name, binName, newBin); err != nil {
		fmt.Fprintf(u.Err, "jog: %v\n", err)
		return 1
	}
	if err := swap(newBin, u.Exe); err != nil {
		os.Remove(newBin)
		fmt.Fprintf(u.Err, "jog: installing the new binary: %v\n", err)
		return 1
	}
	fmt.Fprintf(u.Out, "updated %s → %s\n", u.Version, rel.TagName)
	return 0
}

// releaseVersion is the shape of every jog release tag. Anything else —
// "(devel)", pseudo-versions, an empty string — is not an archive
// install and must not be overwritten.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// classifyInstall reports why this install cannot self-update ("" when it
// can): the message names the update command that owns it instead.
func classifyInstall(version, exe string) string {
	if !releaseVersion.MatchString(version) {
		return "jog was built from source — update with: go install github.com/tyler-johnson/jog/cmd/jog@latest"
	}
	p := filepath.ToSlash(exe)
	for _, brew := range []string{"/Cellar/", "/opt/homebrew/", "/home/linuxbrew/"} {
		if strings.Contains(p, brew) {
			return "jog was installed with Homebrew — update with: brew upgrade jog"
		}
	}
	return ""
}

// isNewer reports whether latest is strictly newer than current; both
// must match releaseVersion.
func isNewer(current, latest string) bool {
	c, l := verFields(current), verFields(latest)
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func verFields(v string) [3]int {
	var f [3]int
	for i, s := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		f[i], _ = strconv.Atoi(s)
	}
	return f
}

// pickAsset finds this platform's archive and the checksums file among a
// release's assets, by the exact names goreleaser produces.
func pickAsset(assets []asset, tag, goos, goarch string) (bin, sums asset, err error) {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	name := fmt.Sprintf("jog_%s_%s_%s.%s", strings.TrimPrefix(tag, "v"), goos, goarch, ext)
	for _, a := range assets {
		switch a.Name {
		case name:
			bin = a
		case "checksums.txt":
			sums = a
		}
	}
	if bin.Name == "" {
		return bin, sums, fmt.Errorf("release %s has no binary for %s/%s (wanted %s)", tag, goos, goarch, name)
	}
	if sums.Name == "" {
		return bin, sums, fmt.Errorf("release %s has no checksums.txt", tag)
	}
	return bin, sums, nil
}

// parseChecksums reads the `<hex>  <filename>` lines goreleaser writes.
func parseChecksums(body string) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			m[fields[1]] = fields[0]
		}
	}
	return m
}
