package agenthost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultUnitPrefix names a chat's scope: socrates-agents-<chat>.scope, one
// per chat (spike S4 §8), so that what a dead host leaves behind can be ended
// exactly and its resources are accounted per chat.
const DefaultUnitPrefix = "socrates-agents-"

// readyWait is how long a launch waits for the new host to listen.
const readyWait = 15 * time.Second

// ErrAlive is a cleanup refused because the host is alive. Stopping the scope
// of a host that answers would kill a working chat (spike S4 §4.3).
var ErrAlive = errors.New("the agent host is alive")

// Launcher starts hosts and clears up after dead ones. It is the server's
// half of the host's life; the host itself never touches its own scope.
type Launcher struct {
	DataDir string
	// Binary is the Socrates executable, run as `agent-host`.
	Binary string
	// Scope starts each host in a transient systemd scope of its own, so that
	// restarting or updating the service never stops it. Without it the host
	// is started in a session of its own, which outlives the server but not a
	// stop of the cgroup it is in: that is the right fallback where Socrates
	// is not a systemd service (ServiceScope).
	Scope bool
	// UnitPrefix is the scope's name before the chat id; empty is
	// DefaultUnitPrefix.
	UnitPrefix string
	// Logf is where the few things worth saying go. Nil is quiet.
	Logf func(format string, args ...any)
}

// ServiceScope reports whether this process is the systemd service, which is
// when hosts belong in scopes of their own. It is termux's test for the tmux
// scope: INVOCATION_ID is what systemd gives a service, and the parent has to
// be PID 1 as well, because a program started inside a session inherits the
// variable and is not the service.
func ServiceScope() bool {
	if os.Getenv("INVOCATION_ID") == "" || os.Getppid() != 1 {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

func (l *Launcher) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// Unit is the scope unit of a chat, without the .scope suffix.
func (l *Launcher) Unit(chatID string) string {
	prefix := l.UnitPrefix
	if prefix == "" {
		prefix = DefaultUnitPrefix
	}
	return prefix + chatID
}

// Alive reports whether a host holds the chat's lock. A host takes it before
// anything else and the kernel releases it when the host ends, however it
// ends, so this is the evidence a dead host leaves no doubt about: no PID
// that could have been reused, no socket file that outlived its listener.
func (l *Launcher) Alive(chatID string) (bool, error) {
	if !ValidChatID(chatID) {
		return false, fmt.Errorf("%q is not a chat id", chatID)
	}
	return lockHeld(filepath.Join(ChatDir(l.DataDir, chatID), lockName))
}

// Reap clears up after a dead host: its scope, with the tool processes still
// in it that the CLI's parent-death signal does not reach, and its socket
// file. It refuses while the host is alive.
func (l *Launcher) Reap(ctx context.Context, chatID string) error {
	alive, err := l.Alive(chatID)
	if err != nil {
		return err
	}
	if alive {
		return ErrAlive
	}
	if l.Scope {
		l.stopScopes(ctx, chatID)
	}
	_ = os.Remove(SocketPath(l.DataDir, chatID))
	return nil
}

// Stop ends a chat's scope whatever is in it, for a chat that is being
// deleted after its host was told to stop.
func (l *Launcher) Stop(ctx context.Context, chatID string) {
	if l.Scope {
		l.stopScopes(ctx, chatID)
	}
}

// stopScopes stops the chat's scope and any second one a refused name made
// (socrates-agents-<chat>-<unix>).
func (l *Launcher) stopScopes(ctx context.Context, chatID string) {
	unit := l.Unit(chatID)
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend",
		"--type=scope", unit+".scope", unit+"-*.scope").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if !ownScope(unit, name) {
			continue
		}
		l.logf("agent host %s: stopping %s, which a dead host left behind", chatID, name)
		if err := exec.CommandContext(ctx, "systemctl", "stop", name).Run(); err != nil {
			l.logf("agent host %s: could not stop %s: %v", chatID, name, err)
		}
	}
}

// ownScope is whether name is the chat's scope: <unit>.scope, or the
// <unit>-<unix seconds>.scope a refused name made. A chat id may contain a
// hyphen, so "<unit>-anything" could be another chat's.
func ownScope(unit, name string) bool {
	if name == unit+".scope" {
		return true
	}
	digits, ok := strings.CutPrefix(name, unit+"-")
	if !ok {
		return false
	}
	digits, ok = strings.CutSuffix(digits, ".scope")
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Launch writes the chat's spec and starts a host for it, and returns once
// the host listens. The caller has made sure there is no host (Alive) and
// cleared up after the last one (Reap).
func (l *Launcher) Launch(ctx context.Context, spec Spec) error {
	if err := supported(); err != nil {
		return err
	}
	if !ValidChatID(spec.ChatID) {
		return fmt.Errorf("%q is not a chat id", spec.ChatID)
	}
	if err := WriteSpec(l.DataDir, spec); err != nil {
		return fmt.Errorf("write the launch spec: %w", err)
	}
	host := []string{l.Binary, "agent-host", "--data", l.DataDir, "--chat", spec.ChatID}
	if !l.Scope {
		return l.start(ctx, spec.ChatID, host[0], host[1:])
	}
	return l.startScoped(ctx, spec.ChatID, host)
}

// startScoped starts the host in its scope, the way termux starts tmux: a
// systemd too old for OOMPolicy on a scope gets the scope without it, and a
// name systemd refuses - a scope that is still being torn down - is tried once
// more as <unit>-<unix seconds>.
func (l *Launcher) startScoped(ctx context.Context, chatID string, host []string) error {
	base := l.Unit(chatID)
	unit, oom := base, true
	for {
		args := []string{"--scope", "--quiet", "--collect", "--unit=" + unit, "--description=Socrates chat " + chatID}
		if oom {
			args = append(args, "-p", "OOMPolicy=continue")
		}
		err := l.start(ctx, chatID, "systemd-run", append(args, host...))
		if err == nil {
			return nil
		}
		switch {
		case oom && strings.Contains(err.Error(), "OOMPolicy"):
			l.logf("agent host %s: systemd refuses OOMPolicy for a scope, so the scope is made without it", chatID)
			oom = false
		case unit == base:
			unit = fmt.Sprintf("%s-%d", base, time.Now().Unix())
			l.logf("agent host %s: %s.scope could not be made, trying %s.scope: %v", chatID, base, unit, err)
		default:
			return err
		}
	}
}

// start runs one launch and waits for the socket. The host's output goes to
// host.log - a file and never a pipe, because the host outlives this process
// and a write to a pipe whose reader is gone would kill it.
func (l *Launcher) start(ctx context.Context, chatID, bin string, args []string) error {
	logPath := filepath.Join(ChatDir(l.DataDir, chatID), logName)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	from, _ := logf.Seek(0, io.SeekEnd)
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	cmd.Env = hostEnv()
	detachAttr(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the agent host: %w", err)
	}
	exited := make(chan error, 1)
	// The host is a child of this process (systemd-run execs into it), so it
	// is reaped here while this process lives, and by PID 1 after.
	go func() { exited <- cmd.Wait() }()

	sock := SocketPath(l.DataDir, chatID)
	deadline := time.Now().Add(readyWait)
	for {
		if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
			c.Close()
			return nil
		}
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("it exited")
			}
			return fmt.Errorf("the agent host did not come up: %v%s", err, logTail(logPath, from))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the agent host did not listen within %s%s", readyWait, logTail(logPath, from))
		}
	}
}

// hostEnv is the environment a host is started with: this process's, minus
// what belongs to the service alone.
func hostEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if key == "NOTIFY_SOCKET" || key == "INVOCATION_ID" || strings.HasPrefix(key, "WATCHDOG_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// logTail is what the host (or systemd-run) wrote since from, as the end of
// an error message.
func logTail(path string, from int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return ""
	}
	raw, _ := io.ReadAll(io.LimitReader(f, 4<<10))
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	return ": " + text
}
