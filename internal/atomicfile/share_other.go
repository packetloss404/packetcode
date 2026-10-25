//go:build !windows

package atomicfile

import "os"

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func renameFile(from, to string) error { return os.Rename(from, to) }

// isShareViolation is always false away from Windows.
//
// POSIX renames succeed with the destination open, and a reader holding an
// unlinked inode keeps reading it, so there is no transient state to retry
// through. The retry loops below therefore run exactly once here: same syscall
// count, same behaviour, no sleep.
func isShareViolation(error) bool { return false }
