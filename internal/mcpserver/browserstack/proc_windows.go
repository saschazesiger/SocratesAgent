package browserstack

import (
	"context"
	"os/exec"
	"time"
)

// shell is a command line run by cmd.exe.
func shell(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd", "/c", line)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
