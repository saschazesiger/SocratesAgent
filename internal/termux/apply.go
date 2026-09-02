package termux

import (
	"context"
	"strconv"
)

// ApplyTerminal is what the dashboard's terminal card does when it is saved:
// it rewrites the generated configuration - which is what the *next* session
// is created on - and applies live what tmux will accept live, so that a
// change to the scrollback or the mouse reaches the panes already open.
//
// window-size is the exception, and it is not a small one: a global
// `window-size` of any value segfaults the tmux server on the next
// new-session, so the policy is never written to the file and never set
// globally. It is set per window, on every session there is, which is exactly
// what applySizePolicy does when a session is created.
func (m *Manager) ApplyTerminal(ctx context.Context, conf ConfOptions, windowSize string) error {
	conf = conf.Normalize()
	if windowSize == "" {
		windowSize = "manual"
	}
	m.mu.Lock()
	m.cfg.Conf = conf
	m.cfg.WindowSize = windowSize
	fallback := m.confFallback
	m.mu.Unlock()

	// A configuration tmux refused once is not written again: the fallback is
	// there because the full file did not load, and overwriting it would leave
	// the next start with the same broken file and no fallback.
	if !fallback {
		if err := WriteConf(m.tmux.Conf, conf); err != nil {
			return err
		}
	}
	if m.unavailable != nil {
		return nil
	}
	running, err := m.tmux.Running(ctx)
	if err != nil || !running {
		// No server means nothing to apply to, and the file above is already
		// what the next one will read.
		return nil
	}
	for _, args := range [][]string{
		{"set", "-g", "history-limit", strconv.Itoa(conf.HistoryLimit)},
		{"set", "-g", "mouse", onOff(conf.Mouse)},
		{"set", "-s", "extended-keys", onOff(conf.ExtendedKeys)},
	} {
		if _, err := m.tmux.Run(ctx, args...); err != nil {
			return err
		}
	}
	for _, name := range m.liveNames() {
		if _, err := m.tmux.Run(ctx, "setw", "-t", name, "window-size", windowSize); err != nil {
			return err
		}
	}
	return nil
}

// liveNames is the tmux name of every session this manager is watching.
func (m *Manager) liveNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.live))
	for _, live := range m.live {
		if live.tmuxName != "" {
			names = append(names, live.tmuxName)
		}
	}
	return names
}

// LiveSessionNames is every session on this Socrates' tmux server, asked of
// tmux rather than of the database: the dashboard's diagnostics are there to
// find the case where the two disagree.
func (m *Manager) LiveSessionNames(ctx context.Context) ([]string, error) {
	if err := m.unavailable; err != nil {
		return nil, err
	}
	running, err := m.tmux.Running(ctx)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, nil
	}
	out, err := m.tmux.Run(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil, err
	}
	return Lines(out), nil
}
