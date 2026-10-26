package harnesses

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

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

// A shell that signs an account in runs the agent first, in the account's
// environment, and is a login shell after it; every word reaches the
// command as it was given.
func TestAShellThatSignsInRunsTheSignInFirst(t *testing.T) {
	dir := t.TempDir()
	req := PlanRequest{Cwd: dir, SignIn: &SignIn{
		Env:     map[string]string{"CLAUDE_CONFIG_DIR": dir + "/acct"},
		Command: []string{"printf", "%s|%s", "it's", "$HOME"},
	}}
	plan, err := Shell{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["CLAUDE_CONFIG_DIR"] != dir+"/acct" {
		t.Fatalf("env %v", plan.Env)
	}
	if len(plan.Argv) != 4 || plan.Argv[1] != "-l" || plan.Argv[2] != "-c" {
		t.Fatalf("argv %q", plan.Argv)
	}
	// The command line, without the shell it hands over to.
	script, _, _ := strings.Cut(plan.Argv[3], "; exec ")
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	if err != nil || string(out) != "it's|$HOME" {
		t.Fatalf("the sign-in ran as %q (%v)", out, err)
	}
	plain, err := Shell{}.Plan(context.Background(), PlanRequest{Cwd: dir})
	if err != nil || len(plain.Argv) != 2 {
		t.Fatalf("a plain shell: %q %v", plain.Argv, err)
	}
}
