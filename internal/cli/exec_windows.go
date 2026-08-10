package cli

// execGit hands the terminal to real git. Windows has no execve, so git
// always runs as a proxied child (runGitChild).
func execGit(gitArgs []string) int { return runGitChild(gitArgs) }
