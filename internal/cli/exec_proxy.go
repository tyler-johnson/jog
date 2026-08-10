package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

// runGitChild proxies git as a child with jog's own stdio handles — a
// real console (pager, colors, credential prompts) — waits, and mirrors
// git's exit code. Windows always hands off this way (no execve); unix
// uses it only when an update notice must print after git exits.
// Ctrl-C reaches the whole console process group: jog ignores it so git
// handles the interrupt and jog lives to report git's exit code instead
// of dying first and orphaning it.
func runGitChild(gitArgs []string) int {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "jog: git not found on PATH")
		return 127
	}
	cmd := exec.Command(gitPath, gitArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "jog: run git: %v\n", err)
		return 126
	}
	return 0
}
