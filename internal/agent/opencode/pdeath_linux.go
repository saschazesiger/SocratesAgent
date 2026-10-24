package opencode

import "syscall"

// setPdeathsig has the kernel kill the child when the thread that forked it
// ends: a sign-in's server, which the dashboard's process starts, does not
// outlive a dashboard that dies in the middle of the sign-in.
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
