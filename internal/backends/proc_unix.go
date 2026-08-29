//go:build !windows

package backends

import (
	"os"
	"os/exec"
	"syscall"
)

func osEnviron() []string { return os.Environ() }

// configureProcess puts the child into its own process group so that the whole
// tree (agent CLI plus the tools it spawns) can be killed at once.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
