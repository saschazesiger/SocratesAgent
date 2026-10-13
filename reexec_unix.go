//go:build !windows

package main

import (
	"os"
	"syscall"
)

// reexec replaces the process image with a fresh start of the same binary and
// arguments. The process id stays, so a container's PID 1 or a supervisor
// never sees Socrates exit, and nothing outside it has to restart it.
func reexec() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
