//go:build !linux && !windows

package opencode

import "syscall"

// setPdeathsig does nothing where the kernel has no parent-death signal
// (macOS): there a sign-in's server outlives a dashboard that dies in the
// middle of the sign-in.
func setPdeathsig(*syscall.SysProcAttr) {}
