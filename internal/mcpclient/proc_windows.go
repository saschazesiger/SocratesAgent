package mcpclient

import (
	"os/exec"
	"strconv"
	"syscall"
)

const (
	// createNewProcessGroup keeps Socrates' own console signals away from
	// the server.
	createNewProcessGroup = 0x00000200
	// createNoWindow keeps a console server from opening a window.
	createNoWindow = 0x08000000
)

// configureProcess starts a stdio server in a process group of its own and
// without a console window.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow, HideWindow: true}
}

// terminateTree does nothing and says so: Windows has no polite stop to ask
// a process for from outside, so killTree does the work.
func terminateTree(*exec.Cmd) bool { return false }

// killTree ends the server and every process it started (taskkill /T) while
// it still runs. Once it ended, Windows no longer knows its children.
func killTree(cmd *exec.Cmd, running bool) {
	if cmd.Process == nil || !running {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	_ = kill.Run()
	_ = cmd.Process.Kill()
}
