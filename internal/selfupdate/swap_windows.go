package selfupdate

import "os"

// swap replaces the installed binary with the freshly extracted one.
// Windows can't delete or overwrite a running executable, but it CAN
// rename it: park the old binary at .old, move the new one in, and try
// to clean up. The final remove usually fails while the old binary is
// still the running process — harmless; the next update clears it.
func swap(newPath, exe string) error {
	old := exe + ".old"
	os.Remove(old) // a leftover from the previous update, no longer running
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, exe); err != nil {
		// Roll the old binary back — never leave the user binary-less.
		os.Rename(old, exe)
		return err
	}
	os.Remove(old)
	return nil
}
