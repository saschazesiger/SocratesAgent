//go:build !windows

package testmode

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the command in a process group of its own and has a
// cancel end the whole group: a hook is a shell, and what it started (a
// daemon closing a device session, npm) goes with it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
