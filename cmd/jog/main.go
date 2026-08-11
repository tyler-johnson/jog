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

const usage = `jog — a memory for your working tree

usage:
  jog                          snapshot now
  jog -m "msg"                 snapshot with a message
  jog log [path]               browse snapshots on this branch (piped: plain list; -p, -n, --all, --json, --format)
  jog since [T] [path]         what changed since a snapshot (default: last command boundary)
  jog restore <path> [--at T]  restore files from a snapshot
  jog restore --all [--at T]   restore the whole working tree
  jog install [--yes]          guided setup: the alias, agent hooks, editor hooks (uninstall reverses it)
  jog shell install            the git alias in your shell's rc file (uninstall, list)
  jog agents install           hooks + skill for every agent client on this machine (uninstall, list; --project)
  jog editors install <name>   post-save snapshots for one text editor (uninstall, list)
  jog hook <client>            agent hook entry point (reads JSON on stdin)
  jog editor-hook <editor>     editor save hook entry point (file path as argument)
  jog git <args>               snapshot, then run the real git command
  jog trim [--dry-run]         drop snapshots older than the keep setting (default 90 days)
  jog config [key [value]]     list jog's settings — or get, set (--unset, --global)
  jog doctor [--fix]           verify invariants, wiring, and liveness
  jog update                   update jog to the latest release
  jog version                  print jog's version

reserved for future releases: mcp

"jog help <command>" (or jog <command> --help) has the details.
`

// usageText appends the per-shell alias hint: the point is a line the
// reader can paste, so each OS sees its own shell's spelling.
func usageText() string {
	alias := "  alias git='jog git'"
	if runtime.GOOS == "windows" {
		alias = "  function git { jog git @args }   # in your PowerShell profile"
	}
	return usage + "\nInstall the alias so every git command snapshots first:\n" + alias + "\n"
}

const agentsHelp = `jog agents — agent client integrations

usage:
  jog agents install [hooks|skill] [client…] [--project]
  jog agents uninstall [hooks|skill] [client…] [--project]
  jog agents list

Two surfaces per client: hooks (snapshot before every prompt and tool
call) and a skill (teaches the agent the recovery workflow — find
versions, restore, checkpoint before risk). install covers both for
every client detected on this machine and skips the rest; name a
surface or a client to narrow it. list shows every supported client
and what is installed.

The default scope is the home directory, so the wiring covers every
repo; --project scopes it to the current repo instead. Existing JSON
fields are preserved, malformed JSON is never rewritten, and uninstall
refuses to delete a skill file carrying local edits.
`

// editorsHelp is shared by editors and its alias editor.
const editorsHelp = `jog editors — editor save hooks

usage:
  jog editors install <editor>
  jog editors uninstall <editor>
  jog editors list

Installs a post-save hook into one text editor, so every save inside a
git repo becomes a restorable snapshot — "vim: save src/main.go" in the
timeline. Unlike jog's other triggers this snapshots after the save: the
saved state is the checkpoint (pre-save state is your editor's undo).

install and uninstall take exactly one editor name per invocation, and
print how that editor's integration works and its gotchas; jog editors
list shows the supported names and what is installed. Everything
installs into your user configuration, covering every repo — except
jetbrains, whose hook can only live in a project's .idea directory:
re-run it in each project you want covered.

The wired hook runs ` + "`jog editor-hook <editor>`" + `, always exits 0, prints
nothing, and is a fast no-op outside git repos. uninstall removes
exactly what install wrote and refuses to delete a file carrying your
edits.

supported: vim, nvim, emacs, sublime, kakoune, micro, vscode, jetbrains
`

// logHelp is shared by log and its aliases snaps and pick.
const logHelp = `jog log — browse the timeline of snapshots on this branch

usage:
  jog log [-p] [-n <count>] [--all] [--json] [--format=<fmt>] [path…]

Snapshots first (running jog is itself a command boundary), then opens
the timeline in an interactive browser: every snapshot with its id, age,
and provenance, the diff previewed as you move. Two frames, one focused
at a time: ↑/↓ (or j/k) move the list, enter focuses the diff so ↑/↓
scroll it, esc returns to the list. From either frame, r restores the
tree to the selected snapshot after a y/n confirmation, and q leaves
everything untouched. Restores go through jog restore, so they are
snapshotted first and undoable. On short windows (a phone SSH session)
the frames show one at a time with the same keys.

Provenance names the command a snapshot ran ahead of — "pre: git status",
"claude[…]: Bash(…)", "manual: msg" — never who made the changes.

options:
  -p, --patch     print full patches via git log instead of browsing
  -n <count>      only the newest <count> snapshots
  --all           every branch's chain, interleaved, with a chain column
  --json          the timeline as JSON: id, sha, ISO time, age, chain,
                  provenance, and each snapshot's files with statuses —
                  the same bytes on a terminal, piped, or in a script
  --format=<fmt>  a git log format for the plain printout; nothing extra
                  is appended, so --format=%h is one line per snapshot
  path…           only snapshots that touched these paths (a restore then
                  touches only those paths)

Piped output (and -p) prints the plain git log rendering: id, age,
provenance, files changed. --json and --format never open the browser,
so scripts and agents get everything without touching git's refs
themselves. Ids feed jog restore --at <id> and jog since <id>.

snaps and pick are aliases of jog log.
`

// restoreHelp is shared by restore and its alias back.
const restoreHelp = `jog restore — restore from a snapshot (worktree only)

usage:
  jog restore <path>… [--at T]
  jog restore --all [--at T]

Restores files as they were in a snapshot — by default the newest one;
--at takes a snap id from jog log or a time: --at 30m, --at 1h,
--at 2d (or any git date, like yesterday). --all restores the whole
tree, including deleting files created since the snapshot.

Only the worktree is written: index, HEAD, branches, and staged changes
stay exactly as they are. Every restore snapshots first, so any jog
restore is undone by another jog restore.

back is an alias of jog restore.
`

// helpTexts is the per-command help behind `jog <cmd> --help` and
// `jog help <cmd>`. Self-contained by design: someone who has never seen
// the README should leave each text knowing what the command does and
// what it will never do.
var helpTexts = map[string]string{
	"log":   logHelp,
	"snaps": logHelp,
	"pick":  logHelp,
	"since": `jog since — what changed since a snapshot

usage:
  jog since [T] [-p] [path…]
  jog since --at T [-p] [-- path…]

Diffs a snapshot against the working tree as it is right now (a fresh
snapshot is taken first, so untracked files count). Without T the
baseline is your last command boundary — "what did that change". T is a
snap id from jog log, or a time: 30m, 1h, 2d, 1w — or anything git's
date syntax accepts (yesterday, 2.hours.ago).

options:
  -p, --patch   full patches instead of the per-file summary
  --at T        the baseline, spelled explicitly
  --            everything after it is a path

A first argument naming an existing file is treated as a path, not a
time; use --at (or --) when a path could be mistaken for a target.
`,
	"restore": restoreHelp,
	"back":    restoreHelp,
	"trim": `jog trim — drop old snapshots

usage:
  jog trim [--dry-run] [--gone]

Drops every snapshot older than the keep setting, which defaults to
90 days ("jog config keep 30.days" changes it; "never" keeps
everything). A chain whose snapshots have all aged out is removed
whole — deleted branches' timelines eventually vanish on their own, and
--gone removes them right away. With the maxSize setting, trim also
drops oldest snapshots beyond a total disk budget; the budget is one
snapshot lenient — the snapshot that crosses it survives — so even a
tiny maxSize leaves the newest snapshot. This is the only jog command
that discards snapshots, and it never runs on its own. The pre-trim
state survives at refs/jog/@trash/<branch> until the trim after next.

options:
  -n, --dry-run   print the plan, touch nothing
  --gone          drop chains whose branch no longer exists, whatever their age
`,
	"config": `jog config — jog's settings, all of them

usage:
  jog config                      list every setting, its value, and what it does
  jog config <key>                print the effective value
  jog config <key> <value>        set it for this repo
  jog config --unset <key>        back to the default
  (--global with set/unset applies to every repo)

Keys are short names like maxFileSize — case-insensitive, and the
git-config spelling (jog.maxFileSize) is accepted too. Settings are
stored as plain git config under jog.*, so git config reads and writes
them identically; jog config just knows the full list, the defaults,
and what each one means. Values are validated through git's own
parsers before anything is written.
`,
	"doctor": `jog doctor — verify the net is under you

usage:
  jog doctor [--fix]

Checks the engine (chains, snapshot identity, reflogs, gc config), the
wiring (shell alias, agent hooks and skills), and liveness (age of
the newest snapshot). Read-only by default; --fix repairs exactly the
two gc config keys that keep git's gc off jog's history, and nothing
else. Exits 0 when healthy, 1 with findings.
`,
	"hook": `jog hook <client> — agent hook entry points

usage:
  jog hook <client>    (the agent client invokes this; JSON payload on stdin)

Snapshots before every prompt and tool call, and always exits 0 — a
failing hook must never block the user's action. Where the client allows
it, jog also introduces itself once per session, one line of context.
These are the commands ` + "`jog agents install`" + ` wires into each
client's settings; they are not meant to be run by hand. The client
names are the ones ` + "`jog agents list`" + ` shows.
`,
	"agents":  agentsHelp,
	"agent":   agentsHelp,
	"editors": editorsHelp,
	"editor":  editorsHelp,
	"shell": `jog shell — the git alias in your shell

usage:
  jog shell install [<shell>…]
  jog shell uninstall [<shell>…]
  jog shell list

Adds ` + "`alias git='jog git'`" + ` to your shell's rc file — the
interactive trigger: every git command you type snapshots first.
Scripts, IDEs, and CI are untouched; they resolve git on PATH and get
real git.

The alias is one marked line appended to the rc file; uninstall removes
exactly that line and nothing else. An alias you added by hand is
recognized and never touched. With no names, install targets your login
shell ($SHELL; PowerShell on Windows) and uninstall sweeps every rc
file carrying jog's line; naming shells overrides both.

supported: bash, zsh, fish, powershell
`,
	"install": `jog install — guided setup

usage:
  jog install [--yes]

Walks through jog's three wiring surfaces, one question each: the git
alias for your shell (` + "`jog shell install`" + `), hooks + skill for
the agent clients on this machine (` + "`jog agents install`" + `), and
a post-save hook per detected editor (` + "`jog editors install`" + `).
Each yes runs the same installer the standalone command runs — nothing
here is reachable only through the wizard, and everything is additive
and idempotent: re-running is always safe.

--yes takes every default without asking (login-shell alias, detected
agents, detected editors) — for scripts and dotfiles.

` + "`jog uninstall`" + ` reverses all of it; ` + "`jog doctor`" + ` verifies the wiring.
`,
	"uninstall": `jog uninstall — remove jog's wiring

usage:
  jog uninstall [--yes]

Shows what is currently wired — the shell alias, agent hooks and
skills, editor save hooks — and removes all of it after one
confirmation (--yes skips the question). Only jog's own lines and files
are touched: a hand-written alias, your settings, and anything carrying
your edits are left alone, with a note.

Snapshots are not deleted — they live in each repo at refs/jog/* and
` + "`jog trim`" + ` manages them. The jog binary itself is also left in
place; the closing line names the right way to remove it for how it was
installed.
`,
	"editor-hook": `jog editor-hook — editor save hook entry point

usage:
  jog editor-hook <editor> [file]    (the editor invokes this on save)

Snapshots the repository containing the saved file, and always exits 0
with no output — a failing hook must never disturb a save. The repo is
discovered from the file's own directory, so the editor's working
directory does not matter; outside a git repo it is a fast no-op. These
are the commands ` + "`jog editors install`" + ` wires; they are not
meant to be run by hand.
`,
	"update": `jog update — update jog to the latest release

usage:
  jog update           install the latest release
  jog update --check   refresh the update cache only, silently

Checks GitHub for the newest jog release, downloads this platform's
binary, verifies its sha256 against the release's checksums.txt, and
replaces the running executable in place. Prints "already up to date"
when there is nothing newer. Nothing else changes — settings, hooks,
and snapshots are untouched by an update.

jog also checks on its own: at most weekly, a background
` + "`jog update --check`" + ` refreshes a small cache in your user cache
directory, and when it finds a newer release, one line prints after a
git command — once per release, only on a terminal, never from hooks.
` + "`jog config autoUpdate true`" + ` goes one further: no notice — jog
installs the release in the background and your next command runs it.
` + "`jog config updateCheck false`" + ` turns all of it off.
--check only rewrites the cache; it never installs anything.

Installs that belong to another tool are recognized and left alone:
a jog built from source is updated with
go install github.com/tyler-johnson/jog/cmd/jog@latest, and a
Homebrew install with brew upgrade jog — jog update says which. If the
install directory isn't writable, re-run with the needed permissions
(e.g. sudo).

Set GITHUB_TOKEN to authenticate the release lookup if the anonymous
GitHub API rate limit ever bites.
`,
	"git": `jog git — snapshot, then run the real git command

usage:
  jog git <args…>

Pure passthrough: take a snapshot, then hand every argument to real git
exactly as typed. This is what alias git='jog git' expands to (PowerShell:
function git { jog git @args } in your profile). jog never matches or
reinterprets git verbs, so no git command, alias, or future git feature
can ever collide with it.

Note: jog git --help reaches real git, as any git argument must. This
text lives at jog help git.
`,
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

func printHelp(verb string) int {
	if h, ok := helpTexts[verb]; ok {
		fmt.Print(h)
		return 0
	}
	fmt.Fprintf(os.Stderr, "jog: no help for %q — commands are listed in `jog --help`\n", verb)
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
	// (including --help) belong to real git, always.
	if _, ok := helpTexts[args[0]]; ok && args[0] != "git" && wantsHelp(args[1:]) {
		return printHelp(args[0])
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
		return agents.Run(args[1:])
	case "editors", "editor":
		return editors.Run(args[1:])
	case "shell":
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
			return printHelp(args[1])
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
