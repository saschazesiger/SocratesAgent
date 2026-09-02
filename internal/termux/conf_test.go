//go:build !windows

package termux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfIsApplied reads every generated option back out of a running server
// in the scope it was written in. Getting a scope wrong is the usual reason a
// tmux configuration silently does nothing, and three of these are not in the
// scope their name suggests.
func TestConfIsApplied(t *testing.T) {
	l := newLab(t, func(c *Config) {
		c.Conf = ConfOptions{HistoryLimit: 12345, Mouse: true, ExtendedKeys: false}
	})
	dir := t.TempDir()
	l.create(shellSpec(dir))

	for _, tc := range []struct {
		what string
		args []string
		want string
	}{
		{"escape-time is a server option", []string{"show", "-sv", "escape-time"}, "0"},
		{"default-terminal is a server option", []string{"show", "-sv", "default-terminal"}, DefaultTerminal()},
		{"remain-on-exit is a window option", []string{"show", "-gwv", "remain-on-exit"}, "on"},
		{"history-limit is a session option", []string{"show", "-gv", "history-limit"}, "12345"},
		{"mouse is a session option", []string{"show", "-gv", "mouse"}, "on"},
		{"the window style is the white background", []string{"show", "-gv", "window-style"}, "fg=#17181b,bg=#ffffff"},
		{"the active window style matches it", []string{"show", "-gv", "window-active-style"}, "fg=#17181b,bg=#ffffff"},
		{"a dead pane keeps the screen", []string{"show", "-gv", "remain-on-exit-format"}, ""},
		{"passthrough is on for Claude Code", []string{"show", "-gv", "allow-passthrough"}, "on"},
		{"the status line is off", []string{"show", "-gv", "status"}, "off"},
		{"the server does not exit when empty", []string{"show", "-sv", "exit-empty"}, "off"},
	} {
		if got := l.tmuxOut(tc.args...); got != tc.want {
			t.Errorf("%s: %v = %q, want %q", tc.what, tc.args, got, tc.want)
		}
	}
}

// TestNoGlobalWindowSizeAnywhere forbids the command that takes the whole
// server down, in the generated configuration and in the source.
//
// A global `window-size manual` segfaults tmux on the *next* new-session,
// whether it came from a configuration file or was set live. Set live it is
// worse than it sounds: the first session created afterwards works, and the
// second one kills the server and every session on it.
func TestNoGlobalWindowSizeAnywhere(t *testing.T) {
	for _, policy := range []string{"manual", "latest", "largest"} {
		conf := string(Conf(ConfOptions{HistoryLimit: 20000, Mouse: true}))
		if strings.Contains(conf, "window-size") {
			t.Fatalf("the generated configuration mentions window-size (policy %q):\n%s", policy, conf)
		}
	}
	if strings.Contains(string(MinimalConf(ConfOptions{})), "window-size") {
		t.Fatal("the fallback configuration mentions window-size")
	}

	// The source itself must never issue the global form. The needle is built
	// here so that this test does not trip over its own text.
	global := "window-size"
	forbidden := []string{"setw -g " + global, "set -g " + global, "set-window-option -g " + global,
		`"-g", "` + global, `"setw", "-g", "` + global}
	root := repoRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range forbidden {
			if strings.Contains(string(data), needle) {
				t.Errorf("%s contains %q, which crashes the tmux server", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("not running inside the repository, so there is no tree to grep")
		}
		dir = parent
	}
}

// TestBadConfFallsBack proves the start-up guard with the configuration that
// actually kills tmux, rather than with a made-up syntax error: a bad
// generated option can never permanently stop sessions from being created.
func TestBadConfFallsBack(t *testing.T) {
	exits := make(chan int, 4)
	l := newLab(t, func(c *Config) {
		c.OnExit = func(id string, status int) { exits <- status }
	})
	poison := "# poisoned by the test\nset -g window-size manual\n"
	if err := os.WriteFile(l.ConfPath(), []byte(poison), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	row := l.create(shellSpec(dir))
	if row.State != "running" {
		t.Fatalf("the session is %q with %q; the guard should have retried", row.State, row.FailReason)
	}
	conf, err := os.ReadFile(l.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "window-size") {
		t.Fatalf("the poisoned configuration is still in place:\n%s", conf)
	}
	if l.tmuxOut("show", "-gv", "window-style") != "fg=#17181b,bg=#ffffff" {
		t.Fatal("the fallback configuration must keep the white background")
	}

	// The fallback must keep the server alive when the last session goes.
	// Socrates starts the server before the first session precisely so that
	// the hooks exist before a pane can die, and a server with no session and
	// exit-empty on is gone before they are set.
	if got := l.tmuxOut("show", "-sv", "exit-empty"); got != "off" {
		t.Fatalf("the fallback leaves exit-empty %q; the server dies with its last session", got)
	}
	// pane-died is listed by show-hooks -gw and session-closed by -g: they
	// are set globally with the same command but live in different option
	// sets, which is one more reason not to try to scope them per session.
	if hooks := l.tmuxOut("show-hooks", "-gw"); !strings.Contains(hooks, "pane-died") {
		t.Fatalf("the fallback lost the global pane-died hook:\n%s", hooks)
	}
	if hooks := l.tmuxOut("show-hooks", "-g"); !strings.Contains(hooks, "session-closed") {
		t.Fatalf("the fallback lost the global session-closed hook:\n%s", hooks)
	}

	// And they work: nothing polls in this test, so the status can only have
	// come from the hook.
	l.create(shellSpec(dir, "/bin/sh", "-c", "exit 4"))
	select {
	case status := <-exits:
		if status != 4 {
			t.Fatalf("the hook reported %d, want 4", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no pane-died hook under the fallback configuration")
	}
}

// TestConfFallbackWhenTheServerItselfIsRefused covers the other half of the
// guard. The poisoned option that kills tmux does it on new-session; a
// configuration the server will not come up with at all has to reach the
// fallback too, and it only does if the retry starts from the server rather
// than from the session.
func TestConfFallbackWhenTheServerItselfIsRefused(t *testing.T) {
	real := requireTmux(t)
	dir := t.TempDir()
	fail := filepath.Join(dir, "fail")
	wrapper := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = start-server ] && [ -f " + fail + " ]; then\n" +
		"    rm -f " + fail + "\n" +
		"    echo 'server exited unexpectedly' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + real + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLab(t, func(c *Config) { c.TmuxBin = wrapper })

	row := l.create(shellSpec(t.TempDir()))
	if row.State != "running" {
		t.Fatalf("the session is %q with %q; the guard should have retried", row.State, row.FailReason)
	}
	conf, err := os.ReadFile(l.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "was refused") {
		t.Fatalf("the fallback configuration was never written:\n%s", conf)
	}
}

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		in    string
		major int
		minor int
		ok    bool
	}{
		{"tmux 3.6\n", 3, 6, true},
		{"tmux 3.3a", 3, 3, true},
		{"tmux next-3.4", 3, 4, true},
		{"tmux 2.9a", 2, 9, true},
		{"nonsense", 0, 0, false},
	} {
		v, ok := ParseVersion(tc.in)
		if ok != tc.ok || (ok && (v.Major != tc.major || v.Minor != tc.minor)) {
			t.Fatalf("ParseVersion(%q) = %v, %v", tc.in, v, ok)
		}
	}
	// The floor is 3.3, not 3.0: allow-passthrough and remain-on-exit-format
	// arrived in 3.3, and on 3.2a the generated configuration errors at load
	// while `new-session -e` is refused outright.
	if v, _ := ParseVersion("tmux 3.2a"); v.OK() {
		t.Fatal("tmux 3.2a must not pass as new enough")
	}
	if v, _ := ParseVersion("tmux 3.3a"); !v.OK() {
		t.Fatal("tmux 3.3a is the oldest version that works, and must pass")
	}
}
