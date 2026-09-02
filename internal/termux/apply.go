package termux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
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
	if m.Available() != nil {
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
	if err := m.Available(); err != nil {
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

// policy and confOptions are how everything else reads the two fields
// ApplyTerminal writes. They exist because a save of the terminal card can
// land while a session is being created or a viewer is being resized, and a
// string read while it is being written is a race whatever the timing looks
// like in practice.
func (m *Manager) policy() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.WindowSize == "" {
		return "manual"
	}
	return m.cfg.WindowSize
}

func (m *Manager) confOptions() ConfOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Conf
}

// Redetect asks the machine about tmux again and republishes the answer.
//
// It exists for exactly one moment: the installer has just finished, the
// dashboard's card says tmux is ready, and without this the manager still
// holds the "tmux is not installed" it decided at start-up - so the sheet's
// Start stays disabled and the page contradicts itself. Everything a session
// needs from the manager is behind the fields set here, so putting them right
// is the whole of making sessions possible again.
//
// It returns the reason sessions are still impossible, or nil.
func (m *Manager) Redetect(ctx context.Context) error {
	bin := m.cfg.TmuxBin
	explicit := bin != ""
	var unavailable error
	if !explicit {
		found, err := exec.LookPath("tmux")
		if err != nil {
			unavailable = errors.New("tmux is not installed. Socrates needs it to keep sessions alive")
			bin = "tmux"
		} else {
			bin = found
		}
	}
	var version Version
	if unavailable == nil {
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		v, err := BinaryVersion(probe, bin)
		switch {
		case err != nil:
			unavailable = fmt.Errorf("could not run %s: %w", bin, err)
		case !v.OK():
			unavailable = fmt.Errorf("tmux %s is too old; Socrates needs %d.%d or newer",
				v, MinMajor, MinMinor)
		}
		version = v
	}

	// Tmux.Bin is deliberately left alone. When tmux was missing it is the
	// literal "tmux", which exec resolves on PATH at every call - so the
	// binary that has just been installed is found without writing a field
	// that every tmux command reads without a lock.
	m.mu.Lock()
	m.tmuxPath = bin
	m.tmuxVersion = version
	m.unavailable = unavailable
	m.mu.Unlock()

	if unavailable != nil {
		return unavailable
	}
	// The generated configuration and the hook socket are what Start puts in
	// place, and a Socrates that came up without tmux never got that far.
	// Start is not called twice for the socket, though: listenHooks unlinks
	// and re-listens, which would strand the goroutine already serving it.
	m.mu.Lock()
	listening := m.hookLn != nil
	m.mu.Unlock()
	if listening {
		return WriteConf(m.tmux.Conf, m.confOptions())
	}
	return m.Start(ctx)
}
