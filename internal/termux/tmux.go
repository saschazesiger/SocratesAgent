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
	// Env is added to the environment of every tmux command. The server
	// inherits it from the command that starts it, and its hooks read two of
	// its variables; putting it here rather than in os.Setenv keeps those out
	// of every other child Socrates starts.
	Env []string
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

// RunConf executes a command with the generated configuration and the UTF-8
// flag, which only the command that starts the server actually needs. It is
// used for `new-session` as belt and braces: should the server somehow not be
// running, the session still gets our configuration rather than the user's.
func (t *Tmux) RunConf(ctx context.Context, args ...string) (string, error) {
	return t.run(ctx, t.Bin, append([]string{"-f", t.Conf, "-S", t.Sock, "-u"}, args...))
}

// RunStart executes the command that starts the server, and is the only one
// the supervisor wraps: a transient scope per session create would be a bus
// round trip for nothing.
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
	cmd.Env = append(append(os.Environ(), "TMUX="), t.Env...)
	if err := cmd.Run(); err != nil {
		return stdout.String(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// Running reports whether a server is listening on our socket.
//
// A missing socket file is answered without running anything, and a server
// that says it is not there is a no. Anything else - busy, refused, an answer
// we could not read - is returned as an error rather than as a no, because
// "there is no server" is the sentence that moves every session to
// needs_resume, and it needs evidence rather than a failed command.
func (t *Tmux) Running(ctx context.Context) (bool, error) {
	if _, err := os.Stat(t.Sock); err != nil {
		return false, nil
	}
	// An empty server answers list-sessions with exit 0 and nothing at all,
	// so an empty answer is not an absent server.
	if _, err := t.Run(ctx, "list-sessions", "-F", "#{session_name}"); err != nil {
		if serverGone(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// noSuchTarget reports whether tmux refused a command because the thing it
// names is not there - including, on a server with no sessions at all, a
// command that needs a current target.
func noSuchTarget(err error) bool {
	s := strings.ToLower(Stderr(err))
	return strings.Contains(s, "no current target") ||
		strings.Contains(s, "can't find session") ||
		strings.Contains(s, "can't find pane") ||
		strings.Contains(s, "can't find window") ||
		strings.Contains(s, "no such target")
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
	if noSuchTarget(err) {
		// A live server with no sessions left refuses a command that needs a
		// target. That is an empty server, not an absent one.
		return false
	}
	return strings.Contains(s, "no server running") ||
		strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "server exited unexpectedly") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "error connecting")
}
