package term

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Environ builds the environment for a session. The important part is what it
// does *not* do: the old headless runner announced itself with CI=1, TERM=dumb
// and NO_COLOR, which is exactly how a coding agent decides to drop its
// interactive interface. A session is meant to look like a person's terminal,
// so it says so.
func Environ(extra []string, cols, rows int) []string {
	env := map[string]string{}
	order := []string{}
	add := func(kv string) {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		if _, seen := env[key]; !seen {
			order = append(order, key)
		}
		env[key] = value
	}
	for _, kv := range os.Environ() {
		add(kv)
	}
	// Leftovers from a previous headless run would put the agent CLIs back
	// into non interactive mode.
	for _, key := range []string{"CI", "NO_COLOR", "FORCE_COLOR"} {
		delete(env, key)
	}
	add("TERM=xterm-256color")
	add("COLORTERM=truecolor")
	add("SOCRATES=1")
	add("COLUMNS=" + strconv.Itoa(cols))
	add("LINES=" + strconv.Itoa(rows))
	for _, kv := range extra {
		add(kv)
	}

	out := make([]string, 0, len(order))
	for _, key := range order {
		if value, ok := env[key]; ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// loginShell is the shell a session starts when no command was given.
func loginShell() string {
	if v := strings.TrimSpace(os.Getenv("SOCRATES_SHELL")); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if v := strings.TrimSpace(os.Getenv("COMSPEC")); v != "" {
			return v
		}
		return "cmd.exe"
	}
	if v := strings.TrimSpace(os.Getenv("SHELL")); v != "" {
		return v
	}
	for _, candidate := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "sh"
}

// loginShellArgs starts an interactive login shell, so that a session sees the
// same PATH and aliases the user has in their own terminal - and, importantly,
// the same environment a one shot command gets, which is also run with -lc.
// A tool that one can find must not be missing from the other.
func loginShellArgs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"-i", "-l"}
}

// Describe renders a session's command for a log line.
func Describe(spec Spec) string {
	if strings.TrimSpace(spec.Command) == "" {
		return fmt.Sprintf("%s (interactive shell)", loginShell())
	}
	return strings.TrimSpace(spec.Command + " " + strings.Join(spec.Args, " "))
}
