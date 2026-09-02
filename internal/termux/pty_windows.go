//go:build windows

package termux

import (
	"os"
	"os/exec"
)

// Terminal sessions need a pseudo terminal and a tmux server, and Socrates
// supports neither on Windows. The build stays green so that the rest of the
// program can be worked on there; a request for a session is refused with a
// sentence that says so.

func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, string, error) {
	return nil, "", ErrUnsupported
}

func setPTYSize(f *os.File, cols, rows int) error {
	return ErrUnsupported
}
