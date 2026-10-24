package server

import "errors"

// diskFree is not measured on Windows, which has no statfs; the admin page
// then shows the disk as unknown.
func diskFree(path string) (free, total uint64, err error) {
	return 0, 0, errors.ErrUnsupported
}
