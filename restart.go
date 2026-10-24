package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"time"
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

// listen opens the listener, trying again for a moment: the process before a
// restart may still be letting go of the port.
func listen(addr string) (net.Listener, error) {
	var err error
	for i := 0; i < 10; i++ {
		var ln net.Listener
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, err
}

// errLocked is a data directory another Socrates is using.
var errLocked = errors.New("another Socrates is running on this data directory")
