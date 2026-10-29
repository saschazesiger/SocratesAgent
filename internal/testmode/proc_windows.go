package testmode

import "os/exec"

// setProcessGroup leaves the command as it is: Windows has no process groups
// to end a hook's children with.
func setProcessGroup(*exec.Cmd) {}
