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
	"runtime/debug"
	"strings"

	"github.com/tyler-johnson/jog/internal/agents"
	"github.com/tyler-johnson/jog/internal/cli"
)

const usage = `jog — a memory for your working tree

usage:
  jog                       snapshot now
  jog -m "msg"              snapshot with a message
  jog snaps [path]          timeline of snapshots on this branch (-p: diffs, --all: every branch)
  jog since [T] [path]      what changed since a snapshot (default: last command boundary)
  jog back <path> [--at T]  restore one file from a snapshot
  jog back --all --at T     restore the whole working tree
  jog agents install        hooks + skill for every agent client on this machine (uninstall, list; --project)
  jog hook claude|codex     agent hook entry point (reads JSON on stdin)
  jog git <args>            snapshot, then run the real git command
  jog pick [--all] <path>   scrub through a file's versions and restore one
  jog trim [--dry-run]      apply the retention taper; drop thinned snapshots
  jog doctor [--fix]        verify invariants, wiring, and liveness
  jog version               print jog's version

reserved for future releases: mcp

"jog help <command>" (or jog <command> --help) has the details.

Install the alias so every git command snapshots first:
  alias git='jog git'
`

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

// helpTexts is the per-command help behind `jog <cmd> --help` and
// `jog help <cmd>`. Self-contained by design: someone who has never seen
// the README should leave each text knowing what the command does and
// what it will never do.
var helpTexts = map[string]string{
	"snaps": `jog snaps — the timeline of snapshots on this branch

usage:
  jog snaps [-p] [--all] [path…]

Snapshots first (running jog is itself a command boundary), then lists
the chain: id, age, provenance, and the files each snapshot changed.
Provenance names the command a snapshot ran ahead of — "pre: git status",
"claude[…]: Bash(…)", "manual: msg" — never who made the changes.

options:
  -p, --patch   full patches instead of per-file summaries
  --all         every branch's chain, interleaved, with a chain column
  path…         only snapshots that touched these paths

Ids from the first column feed jog back --at <id> and jog since <id>.
`,
	"since": `jog since — what changed since a snapshot

usage:
  jog since [T] [-p] [path…]
  jog since --at T [-p] [-- path…]

Diffs a snapshot against the working tree as it is right now (a fresh
snapshot is taken first, so untracked files count). Without T the
baseline is your last command boundary — "what did that change". T is a
snap id from jog snaps, or a time: 30m, 1h, 2d, 1w — or anything git's
date syntax accepts (yesterday, 2.hours.ago).

options:
  -p, --patch   full patches instead of the per-file summary
  --at T        the baseline, spelled explicitly
  --            everything after it is a path

A first argument naming an existing file is treated as a path, not a
time; use --at (or --) when a path could be mistaken for a target.
`,
	"back": `jog back — restore from a snapshot (worktree only)

usage:
  jog back <path>… [--at T]
  jog back --all [--at T]

Restores files as they were in a snapshot — by default the newest one;
--at takes a snap id from jog snaps or a time: --at 30m, --at 1h,
--at 2d (or any git date, like yesterday). --all restores the whole
tree, including deleting files created since the snapshot.

Only the worktree is written: index, HEAD, branches, and staged changes
stay exactly as they are. Every restore snapshots first, so any jog back
is undone by another jog back.
`,
	"pick": `jog pick — scrub through one file's versions

usage:
  jog pick [--all] <path>

Interactive: every snapshot that changed the file, newest first, with
the diff previewed as you move. ↑/↓ or j/k to scrub, enter to restore
that version, q to leave everything untouched. Restoring goes through
jog back, so it is snapshotted and undoable like any other restore.

options:
  --all   search every branch's chain, not just this branch's

Without a terminal (piped output), prints the version list instead.
`,
	"trim": `jog trim — apply the retention taper

usage:
  jog trim [--dry-run]

Thins the timeline: everything kept for 24 hours, then one snapshot per
hour up to 7 days, then one per day up to 90 — configurable via git
config (jog.keepAll, jog.keepHourly, jog.keepDaily; "never" disables a
tier). This is the only jog command that discards snapshots, and it
never runs on its own. The pre-trim state survives at
refs/jog/@trash/<branch> until the trim after next.

options:
  -n, --dry-run   print the plan, touch nothing
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
	"hook": `jog hook claude|codex — agent hook entry points

usage:
  jog hook claude    (Claude Code invokes this; JSON payload on stdin)
  jog hook codex     (Codex invokes this; JSON payload on stdin)

Snapshots before every prompt and tool call, and always exits 0 — a
failing hook must never block the user's action. It also introduces jog
to the agent once per session, one line of context. These are the commands
` + "`jog agents install`" + ` wires into agent settings; they are not
meant to be run by hand.
`,
	"agents": agentsHelp,
	"agent":  agentsHelp,
	"git": `jog git — snapshot, then run the real git command

usage:
  jog git <args…>

Pure passthrough: take a snapshot, then hand every argument to real git
exactly as typed. This is what alias git='jog git' expands to. jog never
matches or reinterprets git verbs, so no git command, alias, or future
git feature can ever collide with it.

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
	case "snaps":
		return cli.Snaps(args[1:])
	case "since":
		return cli.Since(args[1:])
	case "back":
		return cli.Back(args[1:])
	case "hook":
		// Pure runtime entries (`jog hook claude|codex`, JSON on stdin) are
		// the exact commands `jog agents install` wires into settings, so they
		// exit 0 always, even on misconfiguration: a non-zero exit from a
		// hook blocks the user's tool call or prompt. Management lives
		// under `jog agents`; humans reaching for it here get a pointer.
		if len(args) == 2 {
			switch args[1] {
			case "claude":
				return cli.HookClaude(os.Stdin, os.Stdout)
			case "codex":
				return cli.HookCodex(os.Stdin, os.Stdout)
			}
		}
		if len(args) > 2 {
			fmt.Fprintln(os.Stderr, "jog: hook management lives under `jog agents` — try `jog agents install`")
			return 2
		}
		fmt.Fprintln(os.Stderr, "jog: unknown hook adapter (want: jog hook claude or jog hook codex)")
		return 0
	case "agents", "agent":
		return agents.Run(args[1:])
	case "hooks", "skill", "skills":
		fmt.Fprintf(os.Stderr, "jog: %q moved — `jog agents install|uninstall|list` manages hooks and skills\n", args[0])
		return 2
	case "pick":
		return cli.Pick(args[1:])
	case "trim":
		return cli.Trim(args[1:])
	case "doctor":
		return cli.Doctor(args[1:])
	case "mcp":
		fmt.Fprintf(os.Stderr, "jog: %q is not available yet — it is reserved for a future release\n", args[0])
		return 1
	case "-h", "--help", "help":
		if len(args) >= 2 {
			return printHelp(args[1])
		}
		fmt.Print(usage)
		return 0
	case "-v", "--version", "version":
		fmt.Println(versionString())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "jog: unknown command %q — git commands go through: jog git %s\n",
			args[0], strings.Join(args, " "))
		return 1
	}
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
