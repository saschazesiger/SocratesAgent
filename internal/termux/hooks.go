package termux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Hook is one thing tmux told us happened.
type Hook struct {
	Event   string `json:"event"`
	Session string `json:"session"`
	Status  string `json:"status"`
	// Signal is set instead of Status when the program was killed rather than
	// returning.
	Signal string `json:"signal,omitempty"`
}

// The two events Socrates asks tmux for.
const (
	HookPaneDied      = "pane-died"
	HookSessionClosed = "session-closed"
)

// HookSocketPath is where the running Socrates listens for them.
func HookSocketPath(dataDir string) string { return filepath.Join(dataDir, "hook.sock") }

// The hook bodies.
//
// Neither carries a path, and that is deliberate. A hook body is parsed twice
// - once by tmux when the hook fires, once by /bin/sh - so a data directory
// containing a space, an apostrophe or a dollar sign would have to survive two
// different quoting rules in the right order. Verified on tmux 3.6: reading
// the two paths out of the server's environment instead works with all three
// of those in the path, and run-shell still expands the #{} formats inside a
// single quoted body.
//
// The formats differ per event, and not decoratively: #{hook_session_name} is
// populated for session-closed, where the session is already gone and
// #{session_name} is empty, and it is #{session_name} that is populated for
// pane-died. Both hooks are set globally and once. A session scoped
// session-closed hook never fires at all, and `set-hook -g -t <sess>` sets the
// global one while ignoring the -t, so each call would replace the last.
//
// Every expansion is in double quotes inside the single quoted body, which
// survives to the shell. A pane killed by a signal has an *empty*
// #{pane_dead_status} and a #{pane_dead_signal} instead, and an unquoted empty
// expansion would leave --status with no value, so the hook would exit on its
// own arguments exactly when a program crashed.
const (
	paneDiedHook = `run-shell -b '"$` + EnvSocratesBin + `" tmux-hook --sock "$` + EnvHookSocket +
		`" --event pane-died --session "#{session_name}" --status "#{pane_dead_status}"` +
		` --signal "#{pane_dead_signal}"'`
	sessionClosedHook = `run-shell -b '"$` + EnvSocratesBin + `" tmux-hook --sock "$` + EnvHookSocket +
		`" --event session-closed --session "#{hook_session_name}"'`
)

// serverEnv is what the tmux server is started with. It reaches the hooks
// because run-shell children inherit the server's environment; it is set on
// the tmux commands rather than on the Socrates process, so that no other
// child of ours carries it.
func (m *Manager) serverEnv() []string {
	return []string{
		EnvSocratesBin + "=" + m.cfg.SocratesBin,
		EnvHookSocket + "=" + HookSocketPath(m.cfg.DataDir),
	}
}

// refreshServerEnv updates a running server, so that an upgrade that moved the
// binary does not leave the hooks pointing at a path that is gone.
func (m *Manager) refreshServerEnv(ctx context.Context) {
	_, _ = m.tmux.Run(ctx, "set-environment", "-g", EnvSocratesBin, m.cfg.SocratesBin)
	_, _ = m.tmux.Run(ctx, "set-environment", "-g", EnvHookSocket, HookSocketPath(m.cfg.DataDir))
}

// ensureHooks sets the two global hooks once per server. "Once" means once
// they are actually set: a server that died between being started and being
// configured must not leave us believing it has hooks.
func (m *Manager) ensureHooks(ctx context.Context) {
	m.mu.Lock()
	done := m.hooksSet
	m.mu.Unlock()
	if done {
		return
	}
	if !m.setHooks(ctx) {
		return
	}
	m.mu.Lock()
	m.hooksSet = true
	m.mu.Unlock()
}

// setHooks reports whether both hooks are now in place.
func (m *Manager) setHooks(ctx context.Context) bool {
	ok := true
	if _, err := m.tmux.Run(ctx, "set-hook", "-g", HookPaneDied, paneDiedHook); err != nil {
		m.logf("could not set the tmux pane-died hook: %v", err)
		ok = false
	}
	if _, err := m.tmux.Run(ctx, "set-hook", "-g", HookSessionClosed, sessionClosedHook); err != nil {
		m.logf("could not set the tmux session-closed hook: %v", err)
		ok = false
	}
	return ok
}

// listenHooks opens the socket the tmux-hook subcommand writes to. The socket
// is ours alone at mode 0600; a forged line could at worst say that something
// exited, which the poll would correct two seconds later anyway.
func (m *Manager) listenHooks() error {
	path := HookSocketPath(m.cfg.DataDir)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	m.mu.Lock()
	m.hookLn = ln
	m.mu.Unlock()
	go m.serveHooks(ln)
	return nil
}

func (m *Manager) serveHooks(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			scanner := bufio.NewScanner(conn)
			for scanner.Scan() {
				var h Hook
				if err := json.Unmarshal(scanner.Bytes(), &h); err != nil {
					continue
				}
				m.HandleHook(h)
			}
		}()
	}
}

// SendHook is what the tmux-hook subcommand does: one JSON line to the running
// Socrates. It is best effort by design - the poll is the authority, so a hook
// that never arrives costs latency and never correctness.
func SendHook(sock string, h Hook) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(line, '\n'))
	return err
}

// HandleHook applies one event to the row it belongs to.
func (m *Manager) HandleHook(h Hook) {
	id, ok := SessionID(strings.TrimSpace(h.Session))
	if !ok {
		return
	}
	row, err := m.st.GetSession(id)
	if err != nil {
		return
	}
	switch h.Event {
	case HookPaneDied:
		if !m.paneIsDead(row.TmuxName) {
			// A hook that arrives after the session was relaunched under the
			// same name would otherwise put a running terminal into the exit
			// overlay. The pane is the authority.
			return
		}
		m.markExited(row, exitStatus(h.Status, h.Signal))
	case HookSessionClosed:
		if row.State == store.StateRunning || row.State == store.StateStarting {
			// The tmux session is gone but the row is not: the program can be
			// started again on its own session id, which is the reboot path.
			_ = m.st.SetSessionState(id, store.StateNeedsResume, row.ExitStatus, "")
		}
	}
}

// exitStatus turns what tmux reported into one number. A pane killed by a
// signal has no status at all and a signal number instead, which is rendered
// the way a shell renders it.
func exitStatus(status, signal string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(status)); err == nil {
		return n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(signal)); err == nil && n > 0 {
		return 128 + n
	}
	return -1
}

// paneIsDead asks tmux, and answers yes when there is no pane at all: a
// session that has gone entirely is not a session that is still running.
func (m *Manager) paneIsDead(tmuxName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := m.tmux.Run(ctx, "display-message", "-p", "-t", tmuxName, "-F", "#{pane_dead}")
	if err != nil {
		return noSuchTarget(err) || serverGone(err)
	}
	return strings.TrimSpace(out) == "1"
}

func (m *Manager) markExited(row *store.Session, status int) {
	if row.State == store.StateExited && row.ExitStatus == status {
		return
	}
	if err := m.st.SetSessionState(row.ID, store.StateExited, status, ""); err != nil {
		m.logf("could not record the exit of session %s: %v", row.ID, err)
		return
	}
	if m.cfg.OnExit != nil {
		m.cfg.OnExit(row.ID, status)
	}
}
