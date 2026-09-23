package harnesses

import (
	"os"
	"path/filepath"
	"testing"
)

// The host shell is named only when it is shipped, runnable and reachable, and
// the three "no" answers are the load-bearing ones: naming a wrapper that
// cannot be entered would turn every Shell session into a pane that dies on a
// permission error instead of a shell in the container.
func TestHostShellNeedsAnExecutableAndAWayThrough(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "socrates-host-shell")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	unrunnable := filepath.Join(dir, "no-exec-bit")
	if err := os.WriteFile(unrunnable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := func() bool { return true }
	no := func() bool { return false }

	for _, tc := range []struct {
		name      string
		path      string
		reachable func() bool
		want      string
	}{
		{"shipped and reachable", script, yes, script},
		{"nothing to enter", script, no, ""},
		{"not runnable", unrunnable, yes, ""},
		{"not in the image", filepath.Join(dir, "nothing"), yes, ""},
		{"a directory", dir, yes, ""},
	} {
		if got := findHostShell(tc.path, tc.reachable); got != tc.want {
			t.Errorf("%s: findHostShell = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An installation that already shares PID 1's mount namespace - a host
// installation, and this test suite on any machine that is not a --pid=host
// container - has nowhere to jump to and must not try.
func TestHostIsNotReachableFromWhereInitAlreadyIs(t *testing.T) {
	if !initElsewhere() && hostReachable() {
		t.Error("PID 1 is in this very mount namespace and the host was still reported reachable")
	}
}

// Whichever machine this is, the default is a file that exists and can be run.
// That is the promise the fallback chain makes: a shell is never "not
// installed".
func TestShellDefaultBinaryIsSomethingToRun(t *testing.T) {
	bin := Shell{}.DefaultBinary()
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("the default shell %q is not there: %v", bin, err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the default shell %q is not something to run: %v", bin, info.Mode())
	}
}

func TestShellExecutionSelectsTheRequestedMachine(t *testing.T) {
	for _, tc := range []struct {
		name, execution, configured, host, want string
		fails                                   bool
	}{
		{"old default on host", "", "", hostShellPath, hostShellPath, false},
		{"old configured binary", "", "/opt/custom-shell", hostShellPath, "/opt/custom-shell", false},
		{"host overrides configured binary", ShellExecutionHost, "/opt/custom-shell", hostShellPath, hostShellPath, false},
		{"container bypasses redirect", ShellExecutionContainer, "", hostShellPath, "/bin/sh", false},
		{"container uses configured binary", ShellExecutionContainer, "/opt/custom-shell", hostShellPath, "/opt/custom-shell", false},
		{"container bypasses configured wrapper", ShellExecutionContainer, hostShellPath, hostShellPath, "/bin/sh", false},
		{"unavailable host is rejected", ShellExecutionHost, "", "", "", true},
		{"unknown choice is rejected", "remote", "", hostShellPath, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shellBinaryFor(tc.execution, tc.configured, tc.host, "/bin/sh")
			if (err != nil) != tc.fails || got != tc.want {
				t.Fatalf("shellBinaryFor(%q, %q, %q) = %q, %v; want %q, error=%v",
					tc.execution, tc.configured, tc.host, got, err, tc.want, tc.fails)
			}
		})
	}
}
