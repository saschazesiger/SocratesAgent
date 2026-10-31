//go:build !unix

package chattests

import "os"

// openChecked opens a file to copy; the caller checks it is the file it
// looked at.
func openChecked(path string) (*os.File, error) { return os.Open(path) }
