//go:build !windows

package termux

import "syscall"

// signalChildEnded sends pid the signal the kernel sends a process when one of
// its children ends.
func signalChildEnded(pid int) error {
	return syscall.Kill(pid, syscall.SIGCHLD)
}
