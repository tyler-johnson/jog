// Package shell implements `jog shell` — install, uninstall, and list
// the git alias in shell rc files (bash, zsh, fish, PowerShell).
//
// The alias — `alias git='jog git'` — is the interactive trigger: every
// git command the user types snapshots first. It lives as one marked
// line appended to the shell's rc file, kakrc-style (see
// internal/editors/kakoune.go): adding and removing exactly that line
// is surgical, and an alias the user wrote by hand is recognized and
// never touched. Scripts, IDEs, and CI are unaffected — they resolve
// git on PATH and get real git.
//
// With no names, install targets the login shell only ($SHELL;
// PowerShell on Windows) — wiring shells the user doesn't use would
// touch files for no reason. Naming shells forces them, same as
// `jog agents install <name>`. uninstall with no names sweeps every rc
// file carrying jog's marker.
package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// marker identifies the one line jog owns in an rc file; uninstall
// removes exactly the lines carrying it and nothing else. `#` starts a
// comment in all four supported shells.
const marker = "# jog — added by `jog shell install`"

// handSign is the loose fingerprint of an alias the user added
// themselves — any line invoking `jog git` without jog's marker. Same
// heuristic doctor uses; jog never edits such a line.
const handSign = "jog git"

// sh declares one supported shell: its alias spelling, where its rc
// file lives, how it is detected, and how to activate the alias without
// restarting.
type sh struct {
	name   string
	line   string                 // the alias line; marker is appended at install
	rcPath func() (string, error) // the rc file the line lives in
	detect func() bool            // plausibly in use on this machine
	source string                 // command that activates the alias in the current session
}

var registry = []sh{
	{
		name:   "bash",
		line:   `alias git='jog git'`,
		rcPath: func() (string, error) { return install.HomePath(".bashrc") },
		detect: func() bool { return onPath("bash") },
		source: "source ~/.bashrc",
	},
	{
		name: "zsh",
		line: `alias git='jog git'`,
		rcPath: func() (string, error) {
			// zsh reads $ZDOTDIR/.zshrc when the variable is set.
			if z := os.Getenv("ZDOTDIR"); z != "" {
				return filepath.Join(z, ".zshrc"), nil
			}
			return install.HomePath(".zshrc")
		},
		detect: func() bool { return onPath("zsh") },
		source: "source ~/.zshrc",
	},
	{
		name:   "fish",
		line:   `alias git 'jog git'`, // fish spells alias without the =
		rcPath: func() (string, error) { return xdgConfig("fish", "config.fish") },
		detect: func() bool { return onPath("fish") },
		source: "source ~/.config/fish/config.fish",
	},
	{
		name: "powershell",
		line: `function git { jog git @args }`, // PowerShell has no alias-with-args; a function shims it
		rcPath: func() (string, error) {
			// The modern PowerShell (pwsh) profile: Documents on Windows,
			// XDG config on unix.
			if runtime.GOOS == "windows" {
				return install.HomePath("Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1")
			}
			return xdgConfig("powershell", "Microsoft.PowerShell_profile.ps1")
		},
		detect: func() bool { return runtime.GOOS == "windows" || onPath("pwsh") },
		source: ". $PROFILE",
	},
}

// Login names the user's login shell: PowerShell on Windows (there is
// no $SHELL), else the basename of $SHELL when it is a shell jog knows.
func Login() (string, bool) {
	if runtime.GOOS == "windows" {
		return "powershell", true
	}
	base := filepath.Base(os.Getenv("SHELL"))
	for _, s := range registry {
		if s.name == base {
			return s.name, true
		}
	}
	return "", false
}

// Status is one shell's wiring, as doctor and `jog install` consume it.
type Status struct {
	Name      string
	Line      string // the alias line this shell gets, marker aside
	RC        string // the rc file, tilde-rendered
	Login     bool   // the user's login shell
	RCExists  bool
	Installed bool // jog's marked line is present
	ByHand    bool // a `jog git` alias is present without the marker
}

// Statuses reports every supported shell's wiring.
func Statuses() []Status {
	login, _ := Login()
	out := make([]Status, len(registry))
	for i, s := range registry {
		st := Status{Name: s.name, Line: s.line, Login: s.name == login}
		if rc, err := s.rcPath(); err == nil {
			st.RC = install.TildePath(rc)
			if b, err := os.ReadFile(rc); err == nil {
				st.RCExists = true
				st.Installed = strings.Contains(string(b), marker)
				st.ByHand = !st.Installed && strings.Contains(string(b), handSign)
			}
		}
		out[i] = st
	}
	return out
}

// doInstall appends the marked alias line unless the rc file already
// carries it — jog's own or the user's hand-written one.
func (s sh) doInstall() (string, bool, error) {
	rc, err := s.rcPath()
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(rc)
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	content := string(b)
	if strings.Contains(content, marker) {
		return "already installed — " + install.TildePath(rc), false, nil
	}
	if strings.Contains(content, handSign) {
		return "already aliased by hand in " + install.TildePath(rc) + " — leaving it alone", false, nil
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return "", false, err
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += s.line + " " + marker + "\n"
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		return "", false, err
	}
	return "installed — " + install.TildePath(rc), true, nil
}

// doUninstall removes exactly the lines carrying jog's marker. An alias
// without the marker is the user's own — reported, never touched.
func (s sh) doUninstall() (string, bool, error) {
	rc, err := s.rcPath()
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(rc)
	if os.IsNotExist(err) {
		return "not installed — nothing to remove", false, nil
	}
	if err != nil {
		return "", false, err
	}
	content := string(b)
	if !strings.Contains(content, marker) {
		if strings.Contains(content, handSign) {
			return "the alias in " + install.TildePath(rc) + " was not added by jog — remove it yourself if you mean to", false, nil
		}
		return "not installed — nothing to remove", false, nil
	}
	lines := strings.Split(content, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.Contains(l, marker) {
			continue
		}
		kept = append(kept, l)
	}
	if err := os.WriteFile(rc, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		return "", false, err
	}
	return "removed the alias from " + install.TildePath(rc), true, nil
}

func shellNames() string {
	names := make([]string, len(registry))
	for i, s := range registry {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

const usage = "jog: usage: jog shell install|uninstall|list [<shell>…]"

// Run is the `jog shell` command: parse the action and shell names,
// then dispatch.
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
	var targets []sh
	for _, want := range names {
		found := false
		for _, s := range registry {
			if s.name == want {
				targets = append(targets, s)
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "jog: unknown shell %q (supported: %s)\n", want, shellNames())
			return 2
		}
	}

	switch action {
	case "list":
		return list()
	case "install":
		if len(targets) == 0 {
			name, ok := Login()
			if !ok {
				fmt.Fprintf(os.Stderr, "jog: cannot tell your shell from $SHELL — name one: jog shell install %s\n",
					strings.ReplaceAll(shellNames(), ", ", "|"))
				return 2
			}
			targets = []sh{byName(name)}
		}
		return runInstall(targets)
	default:
		if len(targets) == 0 {
			// Sweep: every rc file carrying jog's marker, wherever install
			// (or the user, moving files) left it.
			for _, st := range Statuses() {
				if st.Installed {
					targets = append(targets, byName(st.Name))
				}
			}
			if len(targets) == 0 {
				install.Header("shell", "the git alias", 0)
				fmt.Println(install.StyleDim.Render("  · no jog alias in any shell rc file — nothing to remove"))
				return 0
			}
		}
		return runUninstall(targets)
	}
}

func byName(name string) sh {
	for _, s := range registry {
		if s.name == name {
			return s
		}
	}
	return sh{}
}

// pad widens a shell name to the registry's longest ("powershell"), so
// rows align — install.Row's own column stops at six characters.
func pad(name string) string { return fmt.Sprintf("%-10s", name) }

func runInstall(targets []sh) int {
	install.Header("shell", "the git alias", 0)
	code := 0
	var activated []sh
	for _, s := range targets {
		msg, did, err := s.doInstall()
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", s.name, err)
			code = 1
			continue
		}
		install.Row(pad(s.name), msg, did)
		if did {
			activated = append(activated, s)
		}
	}
	note := func(n string) { fmt.Println(install.StyleDim.Render("  · " + n)) }
	for _, s := range activated {
		note("takes effect in new " + s.name + " sessions — or `" + s.source + "` now")
	}
	note("scripts, IDEs, and CI still get real git — they resolve it on PATH")
	if _, err := exec.LookPath("jog"); err != nil {
		note("jog is not on PATH — the alias will not work until it is")
	}
	if len(activated) > 0 {
		fmt.Println()
		fmt.Println("`jog shell uninstall` removes it; `jog doctor` verifies the wiring.")
	}
	return code
}

func runUninstall(targets []sh) int {
	install.Header("shell", "the git alias", 0)
	code := 0
	for _, s := range targets {
		msg, did, err := s.doUninstall()
		if err != nil {
			fmt.Fprintf(os.Stderr, "jog: %s: %v\n", s.name, err)
			code = 1
			continue
		}
		install.Row(pad(s.name), msg, did)
	}
	return code
}

// list shows all four shells, editors-list style: undetected shells
// with no wiring collapse to a quiet line.
func list() int {
	install.Header("shell", "the git alias", 0)
	for i, st := range Statuses() {
		s := registry[i]
		switch {
		case st.Installed:
			fmt.Printf("  %s %s  %s\n", pad(s.name), install.StyleGood.Render("✓ installed"), st.RC)
		case st.ByHand:
			fmt.Printf("  %s %s  %s\n", pad(s.name), install.StyleGood.Render("✓ aliased by hand"), st.RC)
		case !s.detect() && !st.RCExists:
			fmt.Printf("  %s %s\n", pad(s.name), install.StyleDim.Render("not found on this machine — `jog shell install "+s.name+"` forces it"))
		default:
			fmt.Printf("  %s %s\n", pad(s.name), install.StyleDim.Render("· not installed"))
		}
	}
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

// xdgConfig joins elems under $XDG_CONFIG_HOME, defaulting to ~/.config
// — same rule internal/editors applies.
func xdgConfig(elems ...string) (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(append([]string{x}, elems...)...), nil
	}
	return install.HomePath(append([]string{".config"}, elems...)...)
}
