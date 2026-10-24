//go:build !linux && !windows

package agenthost

import "syscall"

// setPdeathsig does nothing where the kernel has no parent-death signal. A
// host that dies there leaves its CLI to end on the closed pipe.
func setPdeathsig(*syscall.SysProcAttr) {}
