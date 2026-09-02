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

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Hook is one thing tmux told us happened.
type Hook struct {
	Event   string `json:"event"`
	Session string `json:"session"`
	Status  string `json:"status"`
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
const (
	paneDiedHook = `run-shell -b '"$` + EnvSocratesBin + `" tmux-hook --sock "$` + EnvHookSocket +
		`" --event pane-died --session #{session_name} --status #{pane_dead_status}'`
	sessionClosedHook = `run-shell -b '"$` + EnvSocratesBin + `" tmux-hook --sock "$` + EnvHookSocket +
		`" --event session-closed --session #{hook_session_name}'`
)

// setServerEnv puts the two paths the hooks read into the environment of the
// tmux server we are about to start, or of the one already running.
func (m *Manager) setServerEnv() {
	_ = os.Setenv(EnvSocratesBin, m.cfg.SocratesBin)
	_ = os.Setenv(EnvHookSocket, HookSocketPath(m.cfg.DataDir))
}

// refreshServerEnv updates a running server, so that an upgrade that moved the
// binary does not leave the hooks pointing at a path that is gone.
func (m *Manager) refreshServerEnv(ctx context.Context) {
	m.setServerEnv()
	_, _ = m.tmux.Run(ctx, "set-environment", "-g", EnvSocratesBin, m.cfg.SocratesBin)
	_, _ = m.tmux.Run(ctx, "set-environment", "-g", EnvHookSocket, HookSocketPath(m.cfg.DataDir))
}

// ensureHooks sets the two global hooks once per server.
func (m *Manager) ensureHooks(ctx context.Context) {
	m.mu.Lock()
	if m.hooksSet {
		m.mu.Unlock()
		return
	}
	m.hooksSet = true
	m.mu.Unlock()
	m.setHooks(ctx)
}

func (m *Manager) setHooks(ctx context.Context) {
	if _, err := m.tmux.Run(ctx, "set-hook", "-g", HookPaneDied, paneDiedHook); err != nil {
		m.logf("could not set the tmux pane-died hook: %v", err)
	}
	if _, err := m.tmux.Run(ctx, "set-hook", "-g", HookSessionClosed, sessionClosedHook); err != nil {
		m.logf("could not set the tmux session-closed hook: %v", err)
	}
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
		status, err := strconv.Atoi(strings.TrimSpace(h.Status))
		if err != nil {
			status = -1
		}
		m.markExited(row, status)
	case HookSessionClosed:
		if row.State == store.StateRunning || row.State == store.StateStarting {
			// The tmux session is gone but the row is not: the program can be
			// started again on its own session id, which is the reboot path.
			_ = m.st.SetSessionState(id, store.StateNeedsResume, row.ExitStatus, "")
		}
	}
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
