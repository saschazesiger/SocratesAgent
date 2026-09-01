//go:build !windows

package agenthost

import (
	"os/exec"
	"syscall"
)

// detach makes the agent host survive the death of Socrates: a new session
// and process group means it is not in Socrates' job control, and no signal
// sent to the Socrates process group reaches it.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
