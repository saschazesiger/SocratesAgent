//go:build windows

package backends

import (
	"os"
	"os/exec"
)

func osEnviron() []string { return os.Environ() }

func configureProcess(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
