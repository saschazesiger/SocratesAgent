package agenthost

import (
	"errors"
	"os"
	"os/exec"
)

// errUnsupported is every host operation on Windows: agent hosts are Unix
// processes behind a Unix socket, and run where systemd and tmux do.
var errUnsupported = errors.New("agent hosts are not supported on Windows")

var errLocked = errors.New("held by another process")

func lockFile(string) (*os.File, error) { return nil, errUnsupported }

func lockHeld(string) (bool, error) { return false, errUnsupported }

func childAttr(*exec.Cmd) {}

func detachAttr(*exec.Cmd) {}

func processAlive(int) bool { return false }

func supported() error { return errUnsupported }
