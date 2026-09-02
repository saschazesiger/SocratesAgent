//go:build windows

package server

// removeFakeBin has nothing to remove here: the tests that build the fake CLI
// are all //go:build !windows, because a session needs a tmux this platform
// does not have.
func removeFakeBin() {}
