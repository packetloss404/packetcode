//go:build windows

package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Go's os.Open uses FILE_SHARE_READ|FILE_SHARE_WRITE on Windows, omitting
// FILE_SHARE_DELETE. A succession of ordinary readers can therefore starve
// atomic replacement for the entire retry budget. These immutable snapshots
// grant delete-sharing: an existing reader keeps its old file, while new
// readers open the complete replacement at the original name.
func openReadFile(path string) (*os.File, error) {
	name, err := windowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	encoded, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(encoded, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func windowsPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, `\\?\`) || strings.HasPrefix(name, `\\.\`) {
		return name, nil
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	if len(abs) < 248 {
		// Keep ordinary paths verbatim, preserving the platform's handling
		// of relative names and trailing separators.
		return name, nil
	}
	if os.IsPathSeparator(name[len(name)-1]) && !os.IsPathSeparator(abs[len(abs)-1]) {
		abs += `\`
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:], nil
	}
	return `\\?\` + abs, nil
}

func readFile(path string) ([]byte, error) {
	f, err := openReadFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// MoveFileEx cannot replace a destination while an existing reader still has
// it open, even when that reader shares delete access. POSIX rename semantics
// preserve that reader's old snapshot and immediately publish the new name.
// os.Root.Rename implements those semantics on supported Windows filesystems,
// including the standard library's fallback when they are unavailable. Write
// always creates its temp file beside the destination; never reinterpret a
// source basename from a different directory.
func renameFile(from, to string) error {
	renameErr := os.Rename(from, to)
	if renameErr == nil || !isShareViolation(renameErr) {
		return renameErr
	}
	fromDir, err := filepath.Abs(filepath.Dir(from))
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	toDir, err := filepath.Abs(filepath.Dir(to))
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if fromDir != toDir {
		return renameErr
	}
	dir, err := os.OpenRoot(toDir)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	defer dir.Close()
	if err := dir.Rename(filepath.Base(from), filepath.Base(to)); err != nil {
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) {
			err = linkErr.Err
		}
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// isShareViolation reports whether err is Windows refusing an operation only
// because someone else has the file open at this instant.
//
// Our readers grant FILE_SHARE_DELETE, but external programs and older builds
// can still hold handles that deny replacement. Retry those transient leases;
// a reader that remains open must still produce a visible bounded failure.
func isShareViolation(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.Errno(windows.ERROR_ACCESS_DENIED) ||
		errno == syscall.Errno(windows.ERROR_SHARING_VIOLATION)
}
