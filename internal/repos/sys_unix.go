//go:build !windows

package repos

import (
	"os/exec"
	"syscall"
)

// killGroup makes the command a process group of its own, and its
// cancellation end the whole group: a setup's `npm ci` runs children that a
// kill of the shell alone would leave running.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
