//go:build unix

package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/tyler-johnson/jog/internal/gitx"
)

// execGit replaces this process with real git — real TTY (pager, colors,
// interactive rebase), real exit codes. Returns only on failure.
func execGit(gitArgs []string) int {
	gitPath, err := gitx.Look()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog: "+err.Error())
		return 127
	}
	// jog is installed as a shell alias, never a git-named binary on PATH
	// (DESIGN §5), so the lookup cannot resolve back to jog itself.
	argv := append([]string{"git"}, gitArgs...)
	err = syscall.Exec(gitPath, argv, os.Environ())
	fmt.Fprintf(os.Stderr, "jog: exec git: %v\n", err)
	return 126
}
