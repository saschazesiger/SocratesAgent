//go:build !windows

package agenthost

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// errLocked is a lock another process holds.
var errLocked = errors.New("held by another process")

// lockFile takes the exclusive lock on path without waiting, and returns the
// open file that holds it. The kernel lets go of it when the process ends,
// however it ends, which is what makes it the evidence of a live host.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

// lockHeld reports whether some process holds the lock on path. It takes the
// lock for an instant to find out, so it is only ever asked by the one
// process that decides whether to launch a host for the chat.
func lockHeld(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	f, err := lockFile(path)
	if errors.Is(err, errLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	f.Close()
	return false, nil
}

// childAttr is how a CLI child is started: in a process group of its own, so
// that it and everything it starts can be signalled together, and with a
// parent-death signal where the kernel has one.
func childAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	setPdeathsig(cmd.SysProcAttr)
}

// detachAttr is how a host is started: in a session of its own, out of the
// server's job control, so no signal meant for the server reaches it.
func detachAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processAlive reports whether pid is a process that has not been reaped.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// supported reports whether a host can run on this system at all.
func supported() error { return nil }
