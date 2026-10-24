//go:build !windows

package termux

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
)

// startPTY runs cmd on a new pseudo terminal of the given size and returns the
// master side of it together with the name of the slave.
//
// The slave name matters: it is what `list-clients` calls the tmux client we
// just started, and therefore how a viewer detaches exactly its own client
// rather than everybody's.
func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, string, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, "", err
	}
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		master.Close()
		return nil, "", err
	}
	polled, err := pollable(master)
	if err != nil {
		return nil, "", err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	if err := cmd.Start(); err != nil {
		polled.Close()
		return nil, "", err
	}
	return polled, slave.Name(), nil
}

// pollable turns the master into a file the runtime's poller watches, and
// closes the one it was given.
//
// pty does its ioctls through Fd, which puts the descriptor in blocking mode,
// and a read blocked on a blocking descriptor is not ended by Close. Whenever
// some process still held the slave open - the tmux server keeps a client's
// terminal until it has dealt with the detach - closing the viewer then waited
// for its pump for ever. A non-blocking copy registered with the poller lets
// Close end the read at once.
func pollable(master *os.File) (*os.File, error) {
	// The fork lock keeps a child started meanwhile from inheriting the copy
	// before it is marked close-on-exec, the idiom os/exec relies on too.
	syscall.ForkLock.RLock()
	fd, err := syscall.Dup(int(master.Fd()))
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		master.Close()
		return nil, err
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		master.Close()
		return nil, err
	}
	name := master.Name()
	master.Close()
	return os.NewFile(uintptr(fd), name), nil
}

// setPTYSize changes the window size of a pseudo terminal.
//
// Nothing sends SIGWINCH here on purpose: writing the size on the master makes
// the kernel signal the foreground process group of the slave, which is the
// `tmux attach` client, which tells the server itself.
//
// The ioctl goes through SyscallConn rather than pty.Setsize, which would put
// the descriptor back in blocking mode (see pollable).
func setPTYSize(f *os.File, cols, rows int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	ws := pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
