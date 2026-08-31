//go:build windows

package term

import (
	"io"
	"os"
	"os/exec"
)

// HasPTY is false on Windows: the child gets plain pipes instead of a console.
// Ordinary commands work, but a full screen CLI will notice that it is not
// talking to a terminal and fall back to its non interactive mode.
const HasPTY = false

type terminal struct {
	in  io.WriteCloser
	out io.ReadCloser
	cmd *exec.Cmd
}

func startTerminal(cmd *exec.Cmd, cols, rows int) (*terminal, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &terminal{in: in, out: out, cmd: cmd}, nil
}

func (t *terminal) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *terminal) Write(p []byte) (int, error) { return t.in.Write(p) }

func (t *terminal) Close() error {
	t.in.Close()
	return t.out.Close()
}

// Resize has no meaning without a console.
func (t *terminal) Resize(cols, rows int) error { return nil }

func (t *terminal) Signal(sig os.Signal) error {
	if t.cmd.Process == nil {
		return nil
	}
	return t.cmd.Process.Signal(sig)
}

var _ io.ReadWriteCloser = (*terminal)(nil)
