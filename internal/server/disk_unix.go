//go:build linux || darwin

package server

import "syscall"

// diskFree is how much room is left where Socrates writes: the journals, the
// generated per-session files and the database itself.
func diskFree(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), nil
}
