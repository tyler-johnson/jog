// Package shell implements `jog shell` — install, uninstall, and list
// jog's two lines in shell rc files (bash, zsh, fish, PowerShell).
//
// Two surfaces, one marked line each, installed together by default:
//
//   - The alias — `alias git='jog git'` — snapshots before every git
//     command the user types.
//   - The preexec hook — one line calling `jog shell-hook` from the
//     shell's before-each-command mechanism (bash PS0, zsh
//     preexec_functions, fish's fish_preexec event) — snapshots before
//     every interactive command, so `rm -rf`, `sed -i`, and `make
//     clean` are covered too. PowerShell has no preexec mechanism, so
//     it gets the alias only. On a git command both fire: the preexec
//     snapshot mints and the alias-path snapshot no-ops.
//
// Each line lives as one marked line appended to the shell's rc file,
// kakrc-style (see internal/editors/kakoune.go): adding and removing
// exactly that line is surgical, and a line the user wrote by hand is
// recognized and never touched. All logic lives in the jog binary — the
// rc lines are thin shims that guard on jog existing and call it.
// Scripts, IDEs, and CI are unaffected — they resolve git on PATH, get
// real git, and never source an interactive rc file.
//
// With no names, install targets the login shell only ($SHELL;
// PowerShell on Windows) — wiring shells the user doesn't use would
// touch files for no reason. Naming shells forces them, same as
// `jog agents install <name>`. uninstall with no names sweeps every rc
// file carrying jog's markers. --no-alias / --no-preexec scope either
// verb to one surface.
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

// marker identifies the alias line jog owns in an rc file; uninstall
// removes exactly the lines carrying it and nothing else. `#` starts a
// comment in all four supported shells.
const marker = "# jog — added by `jog shell install`"

// preexecMarker identifies jog's preexec line — distinct from the alias
// marker by construction, so each surface installs and removes without
// disturbing the other.
const preexecMarker = "# jog preexec — added by `jog shell install`"

// handSign is the loose fingerprint of an alias the user added
// themselves — any line invoking `jog git` without jog's marker. Same
// heuristic doctor uses; jog never edits such a line.
const handSign = "jog git"

// preexecHandSign is the same fingerprint for a hand-wired preexec
// hook: any unmarked line invoking `jog shell-hook`.
const preexecHandSign = "jog shell-hook"

const headerTitle = "the git alias and preexec hook"

// sh declares one supported shell: its alias and preexec spellings,
// where its rc file lives, how it is detected, and how to activate new
// lines without restarting.
type sh struct {
	name    string
	line    string                 // the alias line; marker is appended at install
	preexec string                 // the preexec line; "" = the shell has no preexec mechanism
	rcPath  func() (string, error) // the rc file the lines live in
	detect  func() bool            // plausibly in use on this machine
	source  string                 // command that activates the lines in the current session
}

var registry = []sh{
	{
		name: "bash",
		line: `alias git='jog git'`,
		// PS0 expands after a command is read, before it executes —
		// interactive only, bash 4.4+; older bash leaves the variable
		// unused. Prepended to any existing PS0. HISTTIMEFORMAT is
		// cleared so `history 1` prints no timestamp; shell-hook
		// --history strips the leading index.
		preexec: `PS0='$(command -v jog >/dev/null && jog shell-hook --history -- "$(HISTTIMEFORMAT= builtin history 1)")'"$PS0"`,
		rcPath:  func() (string, error) { return install.HomePath(".bashrc") },
		detect:  func() bool { return onPath("bash") },
		source:  "source ~/.bashrc",
	},
	{
		name: "zsh",
		line: `alias git='jog git'`,
		// preexec_functions is zsh's native hook array; $1 is the typed
		// command line.
		preexec: `__jog_preexec() { command -v jog >/dev/null && jog shell-hook -- "$1"; }; preexec_functions+=(__jog_preexec)`,
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
		name: "fish",
		line: `alias git 'jog git'`, // fish spells alias without the =
		// "$argv" joins the event's words to one string; redefining the
		// function on re-source is naturally idempotent.
		preexec: `function __jog_preexec --on-event fish_preexec; type -q jog; and jog shell-hook -- "$argv"; end`,
		rcPath:  func() (string, error) { return xdgConfig("fish", "config.fish") },
		detect:  func() bool { return onPath("fish") },
		source:  "source ~/.config/fish/config.fish",
	},
	{
		name:    "powershell",
		line:    `function git { jog git @args }`, // PowerShell has no alias-with-args; a function shims it
		preexec: "",                               // no preexec mechanism — alias only
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

// surface is one of the two lines jog manages in an rc file. The
// mechanics — append one marked line, remove exactly the marked lines,
// recognize a hand-written equivalent — are identical; only the line,
// the marker, and the words differ.
type surface struct {
	key    string            // row label: "alias" or "preexec"
	what   string            // message noun: "the alias" / "the preexec hook"
	byHand string            // install's by-hand verb: "aliased" / "wired"
	marker string            // the marker install appends and uninstall removes by
	sign   string            // hand-written fingerprint (a line jog never touches)
	line   func(s sh) string // the line itself; "" = unsupported on this shell
}

var aliasSurface = surface{
	key: "alias", what: "the alias", byHand: "aliased",
	marker: marker, sign: handSign,
	line: func(s sh) string { return s.line },
}

var preexecSurface = surface{
	key: "preexec", what: "the preexec hook", byHand: "wired",
	marker: preexecMarker, sign: preexecHandSign,
	line: func(s sh) string { return s.preexec },
}

// surfaces resolves the --no-alias/--no-preexec scoping into the list
// of surfaces a verb acts on.
func surfaces(doAlias, doPreexec bool) []surface {
	var out []surface
	if doAlias {
		out = append(out, aliasSurface)
	}
	if doPreexec {
		out = append(out, preexecSurface)
	}
	return out
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
	Name             string
	Line             string // the alias line this shell gets, marker aside
	Preexec          string // the preexec line; "" = the shell has no preexec mechanism
	RC               string // the rc file, tilde-rendered
	Login            bool   // the user's login shell
	RCExists         bool
	Installed        bool // jog's marked alias line is present
	ByHand           bool // a `jog git` alias is present without the marker
	PreexecInstalled bool // jog's marked preexec line is present
	PreexecByHand    bool // a `jog shell-hook` line is present without the marker
}

// Statuses reports every supported shell's wiring — both surfaces from
// one rc read.
func Statuses() []Status {
	login, _ := Login()
	out := make([]Status, len(registry))
	for i, s := range registry {
		st := Status{Name: s.name, Line: s.line, Preexec: s.preexec, Login: s.name == login}
		if rc, err := s.rcPath(); err == nil {
			st.RC = install.TildePath(rc)
			if b, err := os.ReadFile(rc); err == nil {
				content := string(b)
				st.RCExists = true
				st.Installed = strings.Contains(content, marker)
				st.ByHand = !st.Installed && strings.Contains(content, handSign)
				st.PreexecInstalled = strings.Contains(content, preexecMarker)
				st.PreexecByHand = !st.PreexecInstalled && strings.Contains(content, preexecHandSign)
			}
		}
		out[i] = st
	}
	return out
}

// installSurface appends one marked line unless the rc file already
// carries it — jog's own or the user's hand-written equivalent.
func (s sh) installSurface(f surface) (string, bool, error) {
	line := f.line(s)
	if line == "" {
		return "not supported — " + s.name + " has no preexec mechanism", false, nil
	}
	rc, err := s.rcPath()
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(rc)
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	content := string(b)
	if strings.Contains(content, f.marker) {
		return "already installed — " + install.TildePath(rc), false, nil
	}
	if strings.Contains(content, f.sign) {
		return "already " + f.byHand + " by hand in " + install.TildePath(rc) + " — leaving it alone", false, nil
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return "", false, err
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += line + " " + f.marker + "\n"
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		return "", false, err
	}
	return "installed — " + install.TildePath(rc), true, nil
}

// uninstallSurface removes exactly the lines carrying one surface's
// marker. A line without the marker is the user's own — reported, never
// touched.
func (s sh) uninstallSurface(f surface) (string, bool, error) {
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
	if !strings.Contains(content, f.marker) {
		if strings.Contains(content, f.sign) {
			return f.what + " in " + install.TildePath(rc) + " was not added by jog — remove it yourself if you mean to", false, nil
		}
		return "not installed — nothing to remove", false, nil
	}
	lines := strings.Split(content, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.Contains(l, f.marker) {
			continue
		}
		kept = append(kept, l)
	}
	if err := os.WriteFile(rc, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		return "", false, err
	}
	return "removed " + f.what + " from " + install.TildePath(rc), true, nil
}

func shellNames() string {
	names := make([]string, len(registry))
	for i, s := range registry {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

const usage = "jog: usage: jog shell install|uninstall|list [--no-alias|--no-preexec] [<shell>…]"

// Run is the `jog shell` command: parse the action, surface flags, and
// shell names, then dispatch.
func Run(args []string) int {
	action := ""
	doAlias, doPreexec := true, true
	var names []string
	for _, a := range args {
		switch a {
		case "install", "uninstall", "list":
			if action != "" {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
			action = a
		case "--no-alias":
			doAlias = false
		case "--no-preexec":
			doPreexec = false
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
			names = append(names, a)
		}
	}
	if action == "" || (!doAlias && !doPreexec) {
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
		return list() // always shows both surfaces — the flags scope actions, not sight
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
		return runInstall(targets, surfaces(doAlias, doPreexec))
	default:
		if len(targets) == 0 {
			// Sweep: every rc file carrying a jog marker the flags put in
			// scope, wherever install (or the user, moving files) left it.
			for _, st := range Statuses() {
				if (doAlias && st.Installed) || (doPreexec && st.PreexecInstalled) {
					targets = append(targets, byName(st.Name))
				}
			}
			if len(targets) == 0 {
				install.Header("shell", headerTitle, 0)
				fmt.Println(install.StyleDim.Render("  · no jog lines in any shell rc file — nothing to remove"))
				return 0
			}
		}
		return runUninstall(targets, surfaces(doAlias, doPreexec))
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

// surfaceRow is the two-column row label: shell name plus which of its
// lines the row is about.
func surfaceRow(name, key string) string { return fmt.Sprintf("%-10s %-7s", name, key) }

func runInstall(targets []sh, fs []surface) int {
	install.Header("shell", headerTitle, 0)
	code := 0
	var activated []sh
	bashPreexec, preexecNew := false, false
	for _, s := range targets {
		did := false
		for _, f := range fs {
			msg, ok, err := s.installSurface(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s: %v\n", s.name, err)
				code = 1
				continue
			}
			install.Row(surfaceRow(s.name, f.key), msg, ok)
			if ok {
				did = true
				if f.key == preexecSurface.key {
					preexecNew = true
					if s.name == "bash" {
						bashPreexec = true
					}
				}
			}
		}
		if did {
			activated = append(activated, s)
		}
	}
	note := func(n string) { fmt.Println(install.StyleDim.Render("  · " + n)) }
	for _, s := range activated {
		note("takes effect in new " + s.name + " sessions — or `" + s.source + "` now")
	}
	if preexecNew {
		note("the preexec hook snapshots before every command, not just git — `jog shell uninstall --no-alias` removes just it")
	}
	if bashPreexec {
		note("the bash line uses PS0 (bash 4.4+) — older bash ignores it harmlessly")
	}
	note("scripts, IDEs, and CI still get real git — they resolve it on PATH")
	if _, err := exec.LookPath("jog"); err != nil {
		note("jog is not on PATH — the wiring will not work until it is")
	}
	if len(activated) > 0 {
		fmt.Println()
		fmt.Println("`jog shell uninstall` removes it; `jog doctor` verifies the wiring.")
	}
	return code
}

func runUninstall(targets []sh, fs []surface) int {
	install.Header("shell", headerTitle, 0)
	code := 0
	for _, s := range targets {
		for _, f := range fs {
			msg, did, err := s.uninstallSurface(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "jog: %s: %v\n", s.name, err)
				code = 1
				continue
			}
			install.Row(surfaceRow(s.name, f.key), msg, did)
		}
	}
	return code
}

// list shows all four shells, editors-list style: one row per surface,
// and undetected shells with no wiring collapse to a quiet line.
func list() int {
	install.Header("shell", headerTitle, 0)
	for i, st := range Statuses() {
		s := registry[i]
		if !s.detect() && !st.RCExists {
			fmt.Printf("  %s %s\n", pad(s.name), install.StyleDim.Render("not found on this machine — `jog shell install "+s.name+"` forces it"))
			continue
		}
		switch {
		case st.Installed:
			fmt.Printf("  %s %s  %s\n", surfaceRow(s.name, "alias"), install.StyleGood.Render("✓ installed"), st.RC)
		case st.ByHand:
			fmt.Printf("  %s %s  %s\n", surfaceRow(s.name, "alias"), install.StyleGood.Render("✓ aliased by hand"), st.RC)
		default:
			fmt.Printf("  %s %s\n", surfaceRow(s.name, "alias"), install.StyleDim.Render("· not installed"))
		}
		switch {
		case st.Preexec == "":
			fmt.Printf("  %s %s\n", surfaceRow(s.name, "preexec"), install.StyleDim.Render("· not supported — "+s.name+" has no preexec mechanism"))
		case st.PreexecInstalled:
			fmt.Printf("  %s %s  %s\n", surfaceRow(s.name, "preexec"), install.StyleGood.Render("✓ installed"), st.RC)
		case st.PreexecByHand:
			fmt.Printf("  %s %s  %s\n", surfaceRow(s.name, "preexec"), install.StyleGood.Render("✓ wired by hand"), st.RC)
		default:
			fmt.Printf("  %s %s\n", surfaceRow(s.name, "preexec"), install.StyleDim.Render("· not installed"))
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
