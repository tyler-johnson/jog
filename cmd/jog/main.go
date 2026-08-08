// jog — a memory for your working tree.
//
// Two entry modes (docs/PLAN-V0.md D10):
//
//   - `jog git …` (what `alias git='jog git'` produces, jj-style): pure
//     passthrough, forever — snapshot, then exec real git with the rest of
//     the args, zero verb matching. Collision with any git subcommand, user
//     alias, or future git addition is structurally impossible.
//   - Other `jog` verbs are jog's own; anything unreserved also snapshots
//     and execs real git, so jog works without the alias too. Verbs beyond
//     the v0 set are reserved now so adding them later changes nothing.
package main

import (
	"fmt"
	"os"

	"github.com/tyler-johnson/jog/internal/cli"
)

const usage = `jog — a memory for your working tree

usage:
  jog                       snapshot now
  jog -m "msg"              snapshot with a message
  jog snaps [path]          timeline of snapshots on this branch (-p: diffs)
  jog back <path> [--at T]  restore one file from a snapshot
  jog back --all --at T     restore the whole working tree
  jog hook claude           Claude Code hook entry point (reads JSON on stdin)
  jog git <args>            snapshot, then run the real git command
  jog <any git command>     same, for anything jog doesn't reserve

reserved for future releases: since, pick, trim, mcp, doctor

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
	case "back":
		return notImplemented("back", "M6")
	case "hook":
		// Everything under `jog hook` exits 0, even misconfiguration — a
		// non-zero exit from a hook blocks the user's tool call or prompt.
		if len(args) >= 2 && args[1] == "claude" {
			return cli.HookClaude(os.Stdin)
		}
		fmt.Fprintln(os.Stderr, "jog: unknown hook adapter (want: jog hook claude)")
		return 0
	case "since", "pick", "trim", "mcp", "doctor":
		fmt.Fprintf(os.Stderr, "jog: %q is reserved for a future release (see docs/PLAN-V0.md)\n", args[0])
		return 1
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		return cli.Passthrough(args)
	}
}

func notImplemented(what, milestone string) int {
	fmt.Fprintf(os.Stderr, "jog: %s is not implemented yet (%s)\n", what, milestone)
	return 1
}
