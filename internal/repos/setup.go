package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SetupTimeout is how long a repository's setup may take (REPOS.md §4.3).
const SetupTimeout = 10 * time.Minute

// SetupFile is the setup a repository brings itself, run when Admin has none
// for it and the file is executable.
const SetupFile = ".socrates/setup"

// Setup is one run of a repository's setup in a chat's new worktree.
type Setup struct {
	// Script is Admin's setup for the repository, run with `sh -e`; empty
	// runs the worktree's own SetupFile if it is executable.
	Script string
	// Dir is the worktree, where it runs.
	Dir string
	// Log is the file its output goes to (<data>/chats/<id>/setup.log).
	Log string
	// Env is its whole environment.
	Env []string
	// Timeout overrides SetupTimeout, for the tests.
	Timeout time.Duration
}

// ErrSetupTimeout is a setup that did not finish in time.
var ErrSetupTimeout = errors.New("the setup did not finish in time")

// RunSetup runs the setup, if the repository has one, and says whether it
// did. Its output, and how it ended, are in the log either way; a failure is
// the error.
func RunSetup(ctx context.Context, s Setup) (ran bool, err error) {
	var cmd *exec.Cmd
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = SetupTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	what := ""
	switch {
	case strings.TrimSpace(s.Script) != "":
		cmd = exec.CommandContext(ctx, "sh", "-e", "-c", s.Script)
		what = "the repository's setup script (Admin)"
	default:
		file := filepath.Join(s.Dir, SetupFile)
		fi, err := os.Stat(file)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			return false, nil
		}
		cmd = exec.CommandContext(ctx, file)
		what = SetupFile
	}
	if err := os.MkdirAll(filepath.Dir(s.Log), 0o700); err != nil {
		return true, err
	}
	log, err := os.OpenFile(s.Log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return true, err
	}
	defer log.Close()
	start := time.Now()
	fmt.Fprintf(log, "# %s: %s in %s\n", start.Format(time.RFC3339), what, s.Dir)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	// The output goes to the file, not through a pipe, so that a process
	// the setup leaves running cannot hold the wait up.
	cmd.Stdout, cmd.Stderr = log, log
	killGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	took := time.Since(start).Round(time.Second)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		fmt.Fprintf(log, "\n# stopped after %s: the setup may take %s at most\n", took, timeout)
		return true, fmt.Errorf("%w (%s)", ErrSetupTimeout, timeout)
	case err != nil:
		fmt.Fprintf(log, "\n# failed after %s: %v\n", took, err)
		return true, err
	}
	fmt.Fprintf(log, "\n# done in %s\n", took)
	return true, nil
}

// LogTail is the last n lines of a log, for a notice.
func LogTail(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return lastLines(strings.TrimRight(string(raw), "\n"), n)
}
