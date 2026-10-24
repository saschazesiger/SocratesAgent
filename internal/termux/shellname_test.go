//go:build !windows

package termux

import (
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// TestShellIsKnownUnderAnotherShellsName: macOS's /bin/sh execs bash, and
// tmux there names the foreground process by its executable, so a /bin/sh at
// its prompt is "bash". A shell's plan whose foreground command is a shell is
// the shell; anything else in the foreground is not.
func TestShellIsKnownUnderAnotherShellsName(t *testing.T) {
	plan := func(argv ...string) harnesses.LaunchPlan { return harnesses.LaunchPlan{Argv: argv} }
	cases := []struct {
		cmd  string
		plan harnesses.LaunchPlan
		want bool
	}{
		{"sh", plan("/bin/sh", "-l"), true},
		{"bash", plan("/bin/sh", "-l"), true}, // macOS
		{"zsh", plan("/bin/sh"), true},        // macOS, with /private/var/select/sh changed
		{"-bash", plan("/bin/bash", "-l"), true},
		{"sleep", plan("/bin/sh", "-l"), false},
		{"vim", plan("/bin/bash"), false},
		// A program somebody configured as their shell is only ever itself.
		{"bash", plan("/usr/local/bin/xonsh"), false},
		{"xonsh", plan("/usr/local/bin/xonsh"), true},
		// No plan to read: the names alone.
		{"bash", plan(), true},
		{"python3", plan(), false},
	}
	for _, c := range cases {
		if got := isTheShell(c.cmd, c.plan); got != c.want {
			t.Errorf("isTheShell(%q, %q) = %t, want %t", c.cmd, c.plan.Argv, got, c.want)
		}
	}
}
