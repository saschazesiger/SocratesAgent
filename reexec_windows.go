//go:build windows

package main

import (
	"os"
	"os/exec"
)

// reexec starts the same binary again and exits, which is the closest
// Windows has to exec: the new process retries the port for a moment while
// this one lets go of it (see listen).
func reexec() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
