package termux

import (
	"errors"
	"os"
	"os/exec"
)

// errNoPTY is what a terminal attach answers on Windows: a terminal session is
// a tmux pane reached through a pseudo terminal, and Windows has neither.
var errNoPTY = errors.New("terminal sessions need tmux and a pseudo terminal, which Windows does not have")

func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, string, error) {
	return nil, "", errNoPTY
}

func setPTYSize(f *os.File, cols, rows int) error {
	return errNoPTY
}
