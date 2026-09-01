//go:build !windows

// Package proc holds the small platform specific bits of child process
// handling that both the delegate agents and the Cloudflare tunnel need.
package proc

import (
	"os/exec"
	"syscall"
)

// Configure puts the child into its own process group so that the whole tree
// (the CLI plus everything it spawns) can be signalled at once.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Terminate asks the process group to shut down cleanly.
func Terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}
