//go:build !windows

package termux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// scopeCall is one systemd-run the fake runner was asked for.
type scopeCall struct {
	bin  string
	args []string
}

// fakeScopes stands in for Tmux.run: each call takes the next answer, as the
// standard error of a failure, or success when the answer is empty.
func fakeScopes(answers ...string) (runner, *[]scopeCall) {
	var calls []scopeCall
	run := func(_ context.Context, bin string, args []string) (string, error) {
		calls = append(calls, scopeCall{bin: bin, args: slices.Clone(args)})
		if len(answers) == 0 {
			return "", errors.New("no answer left for this call")
		}
		answer := answers[0]
		answers = answers[1:]
		if answer == "" {
			return "ok", nil
		}
		return "", &Error{Args: args, Stderr: answer + "\n", Err: errors.New("exit status 1")}
	}
	return run, &calls
}

// unitOf is the --unit a systemd-run command line names.
func unitOf(t *testing.T, args []string) string {
	t.Helper()
	for _, a := range args {
		if u, ok := strings.CutPrefix(a, "--unit="); ok {
			return u
		}
	}
	t.Fatalf("no --unit in %q", args)
	return ""
}

// hasOOMPolicy reports whether the command line sets OOMPolicy=continue as one
// -p property, rather than merely mentioning it.
func hasOOMPolicy(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-p" && args[i+1] == "OOMPolicy=continue" {
			return true
		}
	}
	return false
}

// TestScopeArgs: the scope is the one the README names, is collected when it
// ends badly, and keeps going when the OOM killer takes one of its processes.
func TestScopeArgs(t *testing.T) {
	args := scopeArgs(scopeUnit, true)
	for _, want := range []string{"--scope", "--collect", "--unit=socrates-tmux", "--description=Socrates sessions"} {
		if !slices.Contains(args, want) {
			t.Errorf("scope args %q are missing %q", args, want)
		}
	}
	if !hasOOMPolicy(args) {
		t.Errorf("scope args %q do not set OOMPolicy=continue", args)
	}
	for _, a := range scopeArgs(scopeUnit, false) {
		if strings.Contains(a, "OOMPolicy") {
			t.Errorf("scope args without the OOM policy still carry %q", a)
		}
	}
}

// TestStartScopedUsesPrimaryUnit: the normal case is one systemd-run, under
// the name the README tells people to stop, with tmux's own command line
// intact after it.
func TestStartScopedUsesPrimaryUnit(t *testing.T) {
	tm := &Tmux{Bin: "/usr/bin/tmux"}
	run, calls := fakeScopes("")
	tmuxArgs := []string{"-f", "conf", "-S", "sock", "-u", "start-server"}
	if _, err := tm.startScoped(context.Background(), run, "/usr/bin/systemd-run", tmuxArgs, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("%d calls, want 1: %v", len(*calls), *calls)
	}
	c := (*calls)[0]
	if c.bin != "/usr/bin/systemd-run" {
		t.Errorf("ran %q, want systemd-run", c.bin)
	}
	if u := unitOf(t, c.args); u != "socrates-tmux" {
		t.Errorf("unit %q, want socrates-tmux", u)
	}
	want := append([]string{"/usr/bin/tmux"}, tmuxArgs...)
	if got := c.args[len(c.args)-len(want):]; !slices.Equal(got, want) {
		t.Errorf("command ends with %q, want %q", got, want)
	}
}

// TestStartScopedRetriesUnderUniqueName: a stale socrates-tmux.scope, kept
// alive by a program a dead server's session left behind, must not push the
// new server into the service. The second try gets a name of its own, with
// the OOM policy still on.
func TestStartScopedRetriesUnderUniqueName(t *testing.T) {
	var logged []string
	tm := &Tmux{Bin: "tmux", Logf: func(format string, args ...any) { logged = append(logged, format) }}
	run, calls := fakeScopes("Failed to start transient scope unit: Unit socrates-tmux.scope already exists.", "")
	if _, err := tm.startScoped(context.Background(), run, "systemd-run", []string{"start-server"}, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("%d calls, want 2: %v", len(*calls), *calls)
	}
	if u := unitOf(t, (*calls)[1].args); u != "socrates-tmux-1700000000" {
		t.Errorf("retry unit %q, want socrates-tmux-1700000000", u)
	}
	if !hasOOMPolicy((*calls)[1].args) {
		t.Errorf("retry %q dropped OOMPolicy=continue", (*calls)[1].args)
	}
	if len(logged) == 0 {
		t.Error("the retry under another name was not logged")
	}
}

// TestStartScopedGivesUpAfterSecondName: two refusals are the end of it, and
// the error goes back to RunStart for the plain start.
func TestStartScopedGivesUpAfterSecondName(t *testing.T) {
	tm := &Tmux{Bin: "tmux"}
	run, calls := fakeScopes("Unit socrates-tmux.scope already exists.", "Unit socrates-tmux-1700000000.scope already exists.")
	_, err := tm.startScoped(context.Background(), run, "systemd-run", []string{"start-server"}, time.Unix(1700000000, 0))
	if err == nil {
		t.Fatal("two refused scopes reported success")
	}
	if !strings.Contains(Stderr(err), "socrates-tmux-1700000000") {
		t.Errorf("error %q is not the second refusal", Stderr(err))
	}
	if len(*calls) != 2 {
		t.Errorf("%d calls, want 2: %v", len(*calls), *calls)
	}
}

// TestStartScopedDropsUnknownOOMPolicy: a systemd too old to take OOMPolicy
// for a scope still gets a scope, under the primary name, without it.
func TestStartScopedDropsUnknownOOMPolicy(t *testing.T) {
	tm := &Tmux{Bin: "tmux"}
	run, calls := fakeScopes("Unknown assignment: OOMPolicy=continue", "")
	if _, err := tm.startScoped(context.Background(), run, "systemd-run", []string{"start-server"}, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("%d calls, want 2: %v", len(*calls), *calls)
	}
	second := (*calls)[1].args
	if hasOOMPolicy(second) {
		t.Errorf("retry %q still sets OOMPolicy", second)
	}
	if u := unitOf(t, second); u != "socrates-tmux" {
		t.Errorf("retry unit %q, want socrates-tmux", u)
	}
}

// TestRunNoStartDoesNotStartServer: the create relies on -N refusing, rather
// than starting a server outside the scope, on every tmux Socrates accepts,
// and on the refusal reading as a server that is gone.
func TestRunNoStartDoesNotStartServer(t *testing.T) {
	bin := requireTmux(t)
	dir := t.TempDir()
	tm := &Tmux{Sock: filepath.Join(dir, "t.sock"), Conf: os.DevNull, Bin: bin}
	_, err := tm.RunNoStart(context.Background(), "new-session", "-d", "-s", "x", "--", "/bin/sh")
	if err == nil {
		_, _ = tm.Run(context.Background(), "kill-server")
		t.Fatal("new-session with -N started a server")
	}
	if !serverGone(err) {
		t.Errorf("refusal %q does not read as a server that is gone", Stderr(err))
	}
	if _, statErr := os.Stat(tm.Sock); statErr == nil {
		t.Error("a socket was made")
	}
}
