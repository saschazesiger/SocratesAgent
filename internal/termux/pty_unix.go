//go:build !windows

package termux

import (
	"os"
	"os/exec"
	"syscall"

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
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, "", err
	}
	return master, slave.Name(), nil
}

// setPTYSize changes the window size of a pseudo terminal.
//
// Nothing sends SIGWINCH here on purpose: writing the size on the master makes
// the kernel signal the foreground process group of the slave, which is the
// `tmux attach` client, which tells the server itself.
func setPTYSize(f *os.File, cols, rows int) error {
	return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
