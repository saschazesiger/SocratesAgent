// Package termux owns the terminal substrate: the Socrates tmux server, the
// sessions on it, and the pseudo terminals the browser watches them through.
// It knows nothing about HTTP.
package termux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ErrUnsupported is what the pseudo terminal helpers return on a platform
// Socrates cannot run terminal sessions on. The rest of the program still
// builds and serves there; sessions do not.
var ErrUnsupported = errors.New("terminal sessions are not supported on this platform")

// MinMajor and MinMinor are the oldest tmux Socrates works with.
//
// The floor is 3.3 rather than 3.0 because the generated configuration needs
// it: allow-passthrough and remain-on-exit-format arrived in 3.3, and
// extended-keys, terminal-features and `new-session -e` in 3.2. On 3.2a the
// configuration errors at load and -e is rejected, so a session would start
// without the environment it was planned with while everything looked healthy.
const (
	MinMajor = 3
	MinMinor = 3
)

// Version is a tmux version as far as we care about it.
type Version struct {
	Major, Minor int
	// Raw is what `tmux -V` printed, suffix letters included.
	Raw string
}

func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// Less reports whether the version is older than major.minor.
func (v Version) Less(major, minor int) bool {
	if v.Major != major {
		return v.Major < major
	}
	return v.Minor < minor
}

// OK reports whether this version is new enough for the generated conf.
func (v Version) OK() bool { return !v.Less(MinMajor, MinMinor) }

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// ParseVersion reads the output of `tmux -V`. "tmux 3.6" and "tmux 3.3a" both
// parse; "tmux next-3.4" parses as 3.4.
func ParseVersion(out string) (Version, bool) {
	out = strings.TrimSpace(out)
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return Version{}, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return Version{}, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(out, "tmux"))
	return Version{Major: major, Minor: minor, Raw: raw}, true
}

// BinaryVersion asks a tmux binary what it is.
func BinaryVersion(ctx context.Context, bin string) (Version, error) {
	out, err := exec.CommandContext(ctx, bin, "-V").Output()
	if err != nil {
		return Version{}, err
	}
	v, ok := ParseVersion(string(out))
	if !ok {
		return Version{}, fmt.Errorf("could not read a version out of %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

// Error is a tmux command that failed, with what it said about it. The stderr
// is kept verbatim because it is what a failed session shows the user.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("tmux %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Stderr returns what tmux printed on stderr for a failed command, or the
// error's own text when it was not a tmux command that failed.
func Stderr(err error) string {
	var te *Error
	if errors.As(err, &te) {
		if s := strings.TrimSpace(te.Stderr); s != "" {
			return s
		}
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// Tmux runs commands against one tmux server, and is the only thing in
// Socrates that runs the binary at all. Every command carries -S, so no code
// path can reach the user's own server by accident.
type Tmux struct {
	Sock string
	Conf string
	Bin  string
	// Supervisor, when set, wraps the command that may start the server so
	// that the server outlives the Socrates process tree.
	Supervisor Supervisor
	// Logf is where the few things worth saying out loud go. Nil is quiet.
	Logf func(format string, args ...any)
}

func (t *Tmux) logf(format string, args ...any) {
	if t.Logf != nil {
		t.Logf(format, args...)
	}
}

// Run executes a tmux command on our socket and returns its standard output.
func (t *Tmux) Run(ctx context.Context, args ...string) (string, error) {
	return t.run(ctx, t.Bin, append([]string{"-S", t.Sock}, args...))
}

// RunStart executes the one command that may have to start the server, and so
// is the only one that passes the generated configuration and the UTF-8 flag.
// A later command on the same socket inherits both from the running server.
func (t *Tmux) RunStart(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-f", t.Conf, "-S", t.Sock, "-u"}, args...)
	if t.Supervisor != nil {
		bin, wrapped, ok := t.Supervisor.Wrap(t.Bin, full)
		if ok {
			out, err := t.run(ctx, bin, wrapped)
			if err == nil {
				return out, nil
			}
			// Supervision is a nicety, never a requirement: without it a
			// restart of Socrates under systemd takes the sessions with it,
			// which is the behaviour we would have had anyway.
			t.logf("could not start the tmux server under %s (%v); starting it directly", bin, err)
		}
	}
	return t.run(ctx, t.Bin, full)
}

func (t *Tmux) run(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Never let tmux read from us, and never let a nested tmux refuse to run
	// because it thinks it is already inside one.
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TMUX=")
	if err := cmd.Run(); err != nil {
		return stdout.String(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// Running reports whether a server is listening on our socket. A missing
// socket file is the reboot case and is answered without running anything.
func (t *Tmux) Running(ctx context.Context) bool {
	if _, err := os.Stat(t.Sock); err != nil {
		return false
	}
	_, err := t.Run(ctx, "list-sessions", "-F", "#{session_name}")
	if err == nil {
		return true
	}
	// An empty server answers "no server running on ..." too, and either way
	// there is nothing of ours on it.
	return false
}

// Lines splits tmux's output into non-empty lines.
func Lines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// serverGone reports whether a failed command means there is no server, as
// opposed to a command that the server refused.
func serverGone(err error) bool {
	s := strings.ToLower(Stderr(err))
	return strings.Contains(s, "no server running") ||
		strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "server exited unexpectedly") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "error connecting")
}
