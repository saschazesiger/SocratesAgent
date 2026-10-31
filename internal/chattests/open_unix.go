//go:build unix

package chattests

import (
	"os"
	"syscall"
)

// openChecked opens a file to copy without following a link in its last
// element and without waiting on a FIFO swapped in after it was checked:
// the open would block with the chat's lock held.
func openChecked(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
