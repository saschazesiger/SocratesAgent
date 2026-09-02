//go:build !linux && !darwin

package server

import "errors"

// diskFree has no portable answer off Linux and macOS, and the dashboard says
// so rather than guessing one. Socrates runs its sessions there; the rest of
// it builds everywhere.
func diskFree(string) (free, total uint64, err error) {
	return 0, 0, errors.New("free space is only reported on Linux and macOS")
}
