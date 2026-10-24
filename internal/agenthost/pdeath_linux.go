package agenthost

import "syscall"

// setPdeathsig has the kernel kill the child when the thread that forked it
// ends. The host forks from its locked main thread, which lives exactly as
// long as the host (spike S4: without this an orphaned CLI finishes its turn
// into a dead pipe).
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
