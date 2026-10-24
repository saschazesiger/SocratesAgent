package proc

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP: the child leads a group
// of its own, so that a console signal meant for Socrates does not reach it.
const createNewProcessGroup = 0x00000200

// Configure puts the child into a process group of its own. Windows has no
// signal for a whole group, so the group only keeps Socrates' own console
// signals away from it.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// Terminate ends the child. There is no polite stop to ask a Windows process
// for from outside, so it is killed.
func Terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
