//go:build !windows

package mcpclient

import (
	"os/exec"
	"syscall"
)

// configureProcess puts a stdio server into a process group of its own, so
// that stopping it stops everything it started.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateTree asks the server's process group to stop, and reports
// whether it could ask.
func terminateTree(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	if syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) != nil {
		return cmd.Process.Signal(syscall.SIGTERM) == nil
	}
	return true
}

// killTree kills the server's process group, and with it what the server
// left behind, even when the server itself already ended.
func killTree(cmd *exec.Cmd, _ bool) {
	if cmd.Process == nil {
		return
	}
	if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
		_ = cmd.Process.Kill()
	}
}
