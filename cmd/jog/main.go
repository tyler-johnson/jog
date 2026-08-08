// jog — a memory for your working tree.
//
// Reserved verbs are handled by jog; anything else snapshots and then execs
// the real git binary (see docs/DESIGN.md §5). Verbs beyond the v0 set are
// reserved now so adding them later never changes passthrough semantics.
package main

import (
	"fmt"
	"os"
)

const usage = `jog — a memory for your working tree

  jog                       snapshot now (also: jog -m "msg")
  jog snaps [path]          timeline of snapshots on this branch
  jog back <path> [--at T]  restore a file (or --all --at T for the whole tree)
  jog hook claude           Claude Code hook entry point
  jog <anything else>       snapshot, then run the real git command
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return notImplemented("snapshot", "M2")
	}
	switch args[0] {
	case "-m":
		return notImplemented("snapshot", "M2")
	case "snaps":
		return notImplemented("snaps", "M5")
	case "back":
		return notImplemented("back", "M6")
	case "hook":
		return notImplemented("hook", "M4")
	case "since", "pick", "trim", "mcp", "doctor":
		fmt.Fprintf(os.Stderr, "jog: %q is reserved for a future release (see docs/PLAN-V0.md)\n", args[0])
		return 1
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		return notImplemented("git passthrough", "M3")
	}
}

func notImplemented(what, milestone string) int {
	fmt.Fprintf(os.Stderr, "jog: %s is not implemented yet (%s)\n", what, milestone)
	return 1
}
