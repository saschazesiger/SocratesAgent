package repos

import "os/exec"

// killGroup leaves the command as it is: Windows has no process groups to
// end, and the cancellation kills the process itself.
func killGroup(*exec.Cmd) {}
