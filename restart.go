package main

import (
	"errors"
	"os"
	"os/exec"
)

// executable is the binary to start again: the running one, or - when it has
// been replaced on disk since, which an upgrade does - whatever the command
// line names.
func executable() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}
	return exec.LookPath(os.Args[0])
}

// errLocked is a data directory another Socrates is using.
var errLocked = errors.New("another Socrates is running on this data directory")
