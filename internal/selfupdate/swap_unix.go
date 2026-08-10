//go:build unix

package selfupdate

import "os"

// swap replaces the installed binary with the freshly extracted one.
// Same-directory rename: atomic, and legal while the old binary is
// running — the process keeps its inode, the path gets the new file.
func swap(newPath, exe string) error {
	return os.Rename(newPath, exe)
}
