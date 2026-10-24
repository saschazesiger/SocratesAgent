package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// reexec starts the same binary again and exits, which is the closest
// Windows has to exec: the new process retries the port for a moment while
// this one lets go of it (see listen).
func reexec() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// lockDataDir writes run/socrates.lock on Windows without locking it: there
// is no flock, and a data directory is not shared there the way a server's is,
// so the lock is the one guard Windows goes without.
func lockDataDir(dataDir string) (func(), error) {
	file := paths.New(dataDir).Path(paths.Lock)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return nil, err
	}
	return func() {}, nil
}
