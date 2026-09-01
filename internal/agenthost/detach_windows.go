//go:build windows

package agenthost

import (
	"os/exec"
	"syscall"
)

// detach starts the agent host in its own process group so a Ctrl+C in the
// console Socrates was started from does not take it down as well.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x00000200 // CREATE_NEW_PROCESS_GROUP
}
