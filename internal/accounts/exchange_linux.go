//go:build linux

package accounts

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// exchange swaps the names a and b in one step (renameat2 with
// RENAME_EXCHANGE): there is no moment in which either is missing. Two
// names on different file systems are syscall.EXDEV, and a file system
// that cannot exchange is errNoExchange.
func exchange(a, b string) error {
	err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EXDEV):
		return syscall.EXDEV
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP):
		return errNoExchange
	}
	return &os.LinkError{Op: "exchange", Old: a, New: b, Err: err}
}
