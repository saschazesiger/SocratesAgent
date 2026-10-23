package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// reexec replaces the process image with a fresh start of the same binary and
// arguments, after a restore was staged: the new image moves it into place
// before it opens the database. The process id stays, so systemd never sees
// Socrates exit, and nothing outside it has to restart it.
func reexec() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

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

// lockDataDir takes the data directory for this process: run/socrates.lock,
// locked for as long as it runs. A second server on the same directory, or
// a restore into one that is being served, is refused instead of writing
// under the first. The lock goes with the process, so a crash never leaves
// it held; the file is closed on exec, and the new image takes it again.
func lockDataDir(dataDir string) (func(), error) {
	file := paths.New(dataDir).Path(paths.Lock)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", errLocked, dataDir)
		}
		return nil, fmt.Errorf("lock %s: %w", file, err)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { f.Close() }, nil
}
