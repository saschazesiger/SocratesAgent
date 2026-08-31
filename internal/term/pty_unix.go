//go:build !windows

package term

import (
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// HasPTY reports whether this build can give a child process a real terminal.
// Everything in this package works either way, but only a real terminal makes
// interactive CLIs such as Claude Code render their full interface.
const HasPTY = true

// terminal is the master side of a pseudo terminal plus the child attached to
// its slave side.
type terminal struct {
	master *os.File
	cmd    *exec.Cmd
}

// startTerminal launches cmd with its own pseudo terminal of the given size.
// The child gets a new session and becomes the foreground process group of
// that terminal, which is what makes job control and Ctrl+C behave normally.
func startTerminal(cmd *exec.Cmd, cols, rows int) (*terminal, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &terminal{master: master, cmd: cmd}, nil
}

func (t *terminal) Read(p []byte) (int, error)  { return t.master.Read(p) }
func (t *terminal) Write(p []byte) (int, error) { return t.master.Write(p) }
func (t *terminal) Close() error                { return t.master.Close() }

// Resize tells the child that its window changed, which makes TUIs redraw.
func (t *terminal) Resize(cols, rows int) error {
	return pty.Setsize(t.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Signal sends a signal to the child's whole process group, so that a CLI
// which spawned helpers of its own goes away with it.
func (t *terminal) Signal(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok || t.cmd.Process == nil {
		return nil
	}
	if pgid, err := syscall.Getpgid(t.cmd.Process.Pid); err == nil {
		return syscall.Kill(-pgid, s)
	}
	return t.cmd.Process.Signal(sig)
}

var _ io.ReadWriteCloser = (*terminal)(nil)
