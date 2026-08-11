// jog — a memory for your working tree.
//
// Two disjoint namespaces (docs/PLAN-V0.md D10/D11):
//
//   - `jog git …` (what `alias git='jog git'` produces, jj-style): pure
//     passthrough, forever — snapshot, then exec real git with the rest of
//     the args, zero verb matching. Collision with any git subcommand, user
//     alias, or future git addition is structurally impossible.
//   - Every other `jog` verb is jog's own; unknown verbs are an error with
//     a `jog git …` hint — never an implicit passthrough.
package main

import (
	"embed"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/tyler-johnson/jog/internal/agents"
	"github.com/tyler-johnson/jog/internal/cli"
	"github.com/tyler-johnson/jog/internal/editors"
	"github.com/tyler-johnson/jog/internal/selfupdate"
	"github.com/tyler-johnson/jog/internal/setup"
	"github.com/tyler-johnson/jog/internal/shell"
)

// usageText appends the per-shell alias hint: the point is a line the
// reader can paste, so each OS sees its own shell's spelling.
func usageText() string {
	alias := "  alias git='jog git'"
	if runtime.GOOS == "windows" {
		alias = "  function git { jog git @args }   # in your PowerShell profile"
	}
	return helpTexts["jog"] + "\nInstall the alias so every git command snapshots first:\n" + alias + "\n"
}

//go:embed help/*.txt
var helpFS embed.FS

// helpTexts maps a help key ("restore", "agents install") to its page.
// Pages are plain text files under help/, embedded at build time; an
// underscore in a filename stands for a space in the key, so hyphens
// stay real (editor-hook.txt). The root page is jog.txt.
var helpTexts = map[string]string{}

func init() {
	entries, err := helpFS.ReadDir("help")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		b, err := helpFS.ReadFile("help/" + e.Name())
		if err != nil {
			panic(err)
		}
		helpTexts[strings.ReplaceAll(strings.TrimSuffix(e.Name(), ".txt"), "_", " ")] = string(b)
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// wantsHelp reports a -h/--help before any "--" separator, mirroring the
// convention that everything after -- is payload, not flags.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// helpAlias folds every command alias onto its canonical spelling, so
// aliased verbs and nested pages resolve under either name
// (`jog help snaps`, `jog help agent list`).
var helpAlias = map[string]string{
	"agent": "agents", "editor": "editors",
	"snaps": "log", "pick": "log", "back": "restore",
}

// resolveHelp finds the most specific help page for a word path:
// "agents list" beats "agents"; unknown trailing words fall back to the
// group page rather than an error.
func resolveHelp(words []string) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	first := words[0]
	if c, ok := helpAlias[first]; ok {
		first = c
	}
	if len(words) >= 2 {
		if h, ok := helpTexts[first+" "+words[1]]; ok {
			return h, true
		}
	}
	h, ok := helpTexts[first]
	return h, ok
}

func printHelp(words ...string) int {
	if h, ok := resolveHelp(words); ok {
		fmt.Print(h)
		return 0
	}
	fmt.Fprintf(os.Stderr, "jog: no help for %q — commands are listed in `jog --help`\n", strings.Join(words, " "))
	return 2
}

func run(args []string) int {
	// Human-facing cli commands use the version to decide whether an
	// update notice is due; resolving it here keeps cli free of
	// runtime/debug.
	cli.SetVersion(moduleVersion())
	if len(args) == 0 {
		return cli.Snapshot("")
	}
	// Per-command help — for every verb except git, where all arguments
	// (including --help) belong to real git, always. The word path picks
	// the most specific page: `jog agents list --help` gets list's page.
	if args[0] != "git" && wantsHelp(args[1:]) {
		if h, ok := resolveHelp(args); ok {
			fmt.Print(h)
			return 0
		}
	}
	switch args[0] {
	case "git":
		return cli.Passthrough(args[1:])
	case "-m":
		// Not a git global flag (`git -m` is an error), so safe to reserve.
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, `jog: -m requires a message (jog -m "before surgery")`)
			return 2
		}
		return cli.Snapshot(args[1])
	case "log", "snaps", "pick":
		return cli.Log(args[0], args[1:])
	case "since":
		return cli.Since(args[1:])
	case "restore", "back":
		return cli.Restore(args[0], args[1:])
	case "hook":
		// Pure runtime entries (`jog hook <client>`, JSON on stdin) are the
		// exact commands `jog agents install` wires into settings, so they
		// exit 0 always, even on misconfiguration: a non-zero exit from a
		// hook blocks the user's tool call or prompt. Management lives
		// under `jog agents`; humans reaching for it here get a pointer.
		if len(args) == 2 {
			return cli.Hook(args[1], os.Stdin, os.Stdout)
		}
		if len(args) > 2 {
			fmt.Fprintln(os.Stderr, "jog: hook management lives under `jog agents` — try `jog agents install`")
			return 2
		}
		fmt.Fprintln(os.Stderr, "jog: hook wants a client name — it is wired by `jog agents install`, not run by hand")
		return 0
	case "agents", "agent":
		// Bare command groups print their help — the group itself does
		// nothing, so the command list is the answer, not a usage error.
		if len(args) == 1 {
			return printHelp("agents")
		}
		return agents.Run(args[1:])
	case "editors", "editor":
		if len(args) == 1 {
			return printHelp("editors")
		}
		return editors.Run(args[1:])
	case "shell":
		if len(args) == 1 {
			return printHelp("shell")
		}
		return shell.Run(args[1:])
	case "install":
		return setup.Install(args[1:])
	case "uninstall":
		return setup.Uninstall(args[1:])
	case "editor-hook":
		// Runtime entry wired by `jog editors install`: exit 0 always,
		// print nothing — output lands in the editor's UI. (A saved file
		// literally named --help is caught by the help interception above;
		// it still exits 0 there, so a save is never disturbed.)
		if len(args) >= 2 {
			return cli.EditorHook(args[1], args[2:])
		}
		fmt.Fprintln(os.Stderr, "jog: editor-hook wants an editor name — it is wired by `jog editors install`, not run by hand")
		return 0
	case "hooks", "skill", "skills":
		fmt.Fprintf(os.Stderr, "jog: %q moved — `jog agents install|uninstall|list` manages hooks and skills\n", args[0])
		return 2
	case "trim":
		return cli.Trim(args[1:])
	case "config":
		return cli.Config(args[1:])
	case "doctor":
		return cli.Doctor(args[1:])
	case "mcp":
		fmt.Fprintf(os.Stderr, "jog: %q is not available yet — it is reserved for a future release\n", args[0])
		return 1
	case "-h", "--help", "help":
		if len(args) >= 2 {
			return printHelp(args[1:]...)
		}
		fmt.Print(usageText())
		return 0
	case "update":
		return selfupdate.Run(args[1:], moduleVersion())
	case "-v", "--version", "version":
		fmt.Println(versionString())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "jog: unknown command %q — git commands go through: jog git %s\n",
			args[0], strings.Join(args, " "))
		return 1
	}
}

// moduleVersion is the version Go embedded at build time: "vX.Y.Z" for
// tagged builds, "(devel)" or a pseudo-version otherwise, "" when the
// build carries no info at all. jog update uses it to tell release
// installs from source builds.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

// versionString reads the version Go embeds at build time: the module
// version for tagged `go install` builds, the VCS revision for builds from
// a checkout. No ldflags to keep in sync.
func versionString() string {
	v := "jog version "
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v + "unknown"
	}
	v += info.Main.Version
	// Pseudo-versions already embed the revision; only plain "(devel)"
	// builds (e.g. go build from a checkout) need it appended.
	if info.Main.Version == "(devel)" {
		var rev, dirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = ", dirty"
				}
			}
		}
		if len(rev) >= 12 {
			v += " (" + rev[:12] + dirty + ")"
		}
	}
	return v
}
