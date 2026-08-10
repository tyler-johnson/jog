//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// execGit replaces this process with real git — real TTY (pager, colors,
// interactive rebase), real exit codes. Returns only on failure.
func execGit(gitArgs []string) int {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog: git not found on PATH")
		return 127
	}
	// jog is installed as a shell alias, never a git-named binary on PATH
	// (DESIGN §5), so LookPath cannot resolve back to jog itself.
	argv := append([]string{"git"}, gitArgs...)
	err = syscall.Exec(gitPath, argv, os.Environ())
	fmt.Fprintf(os.Stderr, "jog: exec git: %v\n", err)
	return 126
}
