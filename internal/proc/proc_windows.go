//go:build windows

package proc

import "os/exec"

// Configure is a no-op on Windows, which has no process groups of this kind.
func Configure(cmd *exec.Cmd) {}

// Terminate stops the process.
func Terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
