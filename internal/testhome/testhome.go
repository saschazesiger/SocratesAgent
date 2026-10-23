// Package testhome gives a test binary a home of its own.
//
// Code under test finds a person's folders through the environment: HOME
// (and through it ~/.claude, ~/.claude.json, ~/.codex, ~/.config/opencode and
// ~/.local/share/opencode), CLAUDE_CONFIG_DIR, CODEX_HOME, the XDG variables,
// OpenCode's own, and SOCRATES_DATA_DIR, which is /var/lib/socrates when it is
// unset. A test that forgets to point one of them elsewhere reads, or writes,
// the real one. That is not a theoretical risk: a usage test once wrote a fake
// .credentials.json into the real ~/.claude on every run of the server's tests,
// and every Claude Code on the machine was logged out each time.
//
// So every test package runs its tests through Main, which points all of them
// at a fresh temporary directory before the first test runs and removes it
// after the last. A test that needs a folder of its own still sets it with
// t.Setenv. TestEveryTestPackageRunsThroughMain keeps it that way for packages
// added later.
//
// Go's own caches are pinned where they were, so that the fakes a TestMain
// builds and the toolchain go.mod asks for are not fetched and compiled again
// under the new home.
//
// A long temporary directory (macOS gives every user one under /var/folders)
// is replaced by /tmp: the tests put tmux and agent-host sockets under
// t.TempDir(), and a Unix socket path has a limit of about 104 bytes.
package testhome

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cleared are the variables that name a folder of the person's, or a
// credential, and that are removed so that the defaults derived from the new
// HOME apply. The prefixes clear every variable that starts with them.
var (
	cleared = []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"SOCRATES_WORKSPACE_ROOT", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
	}
	clearedPrefixes = []string{"OPENCODE_", "SOCRATES_S3_", "CLAUDE_CODE_", "ANTHROPIC_"}
)

// shortTemp is the temporary directory a long one is replaced by.
const shortTemp = "/tmp"

// goVars are the variables of Go's own that are derived from HOME and are
// pinned to the values they resolve to now.
var goVars = []string{"GOENV", "GOCACHE", "GOMODCACHE", "GOPATH"}

// Main runs a package's tests with a home of their own and returns the exit
// code for os.Exit. A TestMain that dispatches subcommands of the test binary
// does that first and calls Main in place of m.Run.
func Main(m *testing.M) int {
	cleanup, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		return 1
	}
	defer cleanup()
	return m.Run()
}

// Isolate points the process's environment at a new temporary home and data
// folder, and returns the function that removes them. It is for a TestMain
// that has more to do around m.Run than Main allows.
func Isolate() (func(), error) {
	pinned := map[string]string{}
	if out, err := exec.Command("go", append([]string{"env"}, goVars...)...).Output(); err == nil {
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		for i, name := range goVars {
			if i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				pinned[name] = strings.TrimSpace(lines[i])
			}
		}
	}
	if runtime.GOOS != "windows" && len(os.TempDir()) > len(shortTemp) {
		if info, err := os.Stat(shortTemp); err == nil && info.IsDir() {
			_ = os.Setenv("TMPDIR", shortTemp)
		}
	}
	root, err := os.MkdirTemp("", "socrates-testhome-")
	if err != nil {
		return nil, err
	}
	home, data := filepath.Join(root, "home"), filepath.Join(root, "data")
	for _, dir := range []string{home, data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, prefix := range clearedPrefixes {
			if strings.HasPrefix(name, prefix) {
				_ = os.Unsetenv(name)
			}
		}
	}
	for _, name := range cleared {
		_ = os.Unsetenv(name)
	}
	for name, value := range pinned {
		_ = os.Setenv(name, value)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("SOCRATES_DATA_DIR", data)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("the home directory is still %q after it was set to %q", got, home)
	}
	return func() { _ = os.RemoveAll(root) }, nil
}
