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
  jog hook claude           Claude Code hook entry point (reads JSON on stdin)
  jog git <args>            snapshot, then run the real git command
  jog pick [--all] <path>   scrub through a file's versions and restore one
  jog trim [--dry-run]      apply the retention taper; drop thinned snapshots
  jog doctor [--fix]        verify invariants, wiring, and liveness
  jog version               print jog's version

reserved for future releases: mcp

Install the alias so every git command snapshots first:
  alias git='jog git'
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return cli.Snapshot("")
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
		// Everything under `jog hook` exits 0, even misconfiguration — a
		// non-zero exit from a hook blocks the user's tool call or prompt.
		if len(args) >= 2 && args[1] == "claude" {
			return cli.HookClaude(os.Stdin)
		}
		fmt.Fprintln(os.Stderr, "jog: unknown hook adapter (want: jog hook claude)")
		return 0
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
