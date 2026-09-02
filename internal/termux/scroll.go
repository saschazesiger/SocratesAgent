package termux

import (
	"context"
	"strconv"
	"strings"
)

// Scrolling the pane's history, from the browser.
//
// A finger on a phone has to reach what has scrolled off the top of a pane,
// and the only history that always exists is tmux's own. The obvious route -
// hand the drag to xterm.js as a wheel - is not one route but three, and two
// of them are wrong: with a program tracking the mouse (which is every one of
// Claude Code, Codex and OpenCode) the wheel becomes an SGR mouse report and
// is forwarded to the program, which scrolls its own conversation or ignores
// it entirely; with the mouse off it becomes arrow keys, which walks a shell's
// history onto the prompt. Neither reaches tmux's scrollback, and on an
// alternate screen there is no xterm scrollback to move either.
//
// So the drag is not a wheel at all. It is a command on the control channel
// that puts the pane into tmux's own copy mode and scrolls it there, which is
// the same history a person at a desk reaches with a prefix and PageUp. It
// works the same whether the program is a shell or a full-screen TUI, whether
// tmux's mouse is on or off, and whether the program tracks the mouse itself,
// because none of them is consulted.
//
// Copy mode is entered with -e, so that scrolling back down to the bottom
// leaves it on its own: dragging up until the pane stops moving is a complete
// gesture with nothing left behind.
//
// The one thing that must never happen is a keystroke swallowed by a copy mode
// nobody knows they are in. Whoever writes input asks InCopyMode first, and
// this is why the state is a flag on the Manager rather than a tmux query: the
// flag is only ever true just after we scrolled, so a keystroke costs a map
// read and never a subprocess.

// PaneScroll is what a viewer needs to know to scroll one pane.
type PaneScroll struct {
	// InMode is whether the pane is held above its live bottom, which is what
	// puts the "Jump to live" pill on screen.
	InMode bool
	// Wheel is whether this pane has no tmux history to move, so that the
	// browser should send the program the wheel it asked for instead.
	//
	// A pane showing an alternate screen is the whole of that case. tmux's
	// scrollback belongs to the normal buffer, and the alternate screen is a
	// window over it with nothing above: verified, `copy-mode` followed by
	// twenty `scroll-up` leaves `capture-pane` returning the same three lines,
	// while `capture-pane -a` still holds the normal buffer underneath. The
	// only thing that can scroll such a pane is the program's own view, and a
	// program that draws a full screen is tracking the mouse in order to be
	// asked - which is what Codex and OpenCode do.
	Wheel bool
}

// PaneState answers both questions without moving anything. The handshake
// asks it, so that the first drag of a session already takes the right route.
func (m *Manager) PaneState(ctx context.Context, sessionID string) PaneScroll {
	m.scrollMu.Lock()
	defer m.scrollMu.Unlock()
	name := m.tmuxNameOf(sessionID)
	if name == "" {
		return PaneScroll{}
	}
	in, wheel, err := m.paneFlags(ctx, name, sessionID)
	if err != nil {
		return PaneScroll{}
	}
	return PaneScroll{InMode: in, Wheel: wheel}
}

// ScrollHistory moves the pane's view through tmux's own scrollback.
//
// Positive lines go back into the history, negative lines return towards the
// live bottom.
func (m *Manager) ScrollHistory(ctx context.Context, sessionID string, lines int) (PaneScroll, error) {
	m.scrollMu.Lock()
	defer m.scrollMu.Unlock()
	name := m.tmuxNameOf(sessionID)
	if name == "" {
		return PaneScroll{}, nil
	}
	in, wheel, err := m.paneFlags(ctx, name, sessionID)
	if err != nil {
		return PaneScroll{}, err
	}
	// A pane with no history of its own is not scrolled here at all: the
	// browser is told to hand the drag to the program instead.
	if wheel && !in {
		return PaneScroll{Wheel: true}, nil
	}
	if lines == 0 {
		return PaneScroll{InMode: in, Wheel: wheel}, nil
	}
	if !m.inCopyMode(sessionID) {
		// Nothing to come back to: the pane is already at the bottom.
		if lines < 0 {
			return PaneScroll{Wheel: wheel}, nil
		}
		if _, err := m.tmux.Run(ctx, "copy-mode", "-e", "-t", name); err != nil {
			return PaneScroll{}, err
		}
		m.setCopyMode(sessionID, true)
	}
	command := "scroll-up"
	count := lines
	if lines < 0 {
		command = "scroll-down"
		count = -lines
	}
	if _, err := m.tmux.Run(ctx, "send-keys", "-t", name,
		"-N", strconv.Itoa(count), "-X", command); err != nil {
		// The pane can leave copy mode under us - a redraw, another viewer, or
		// the -e exit on the way down - and then send-keys has no mode to send
		// to. That is the bottom, not a failure.
		if noSuchTarget(err) || strings.Contains(strings.ToLower(Stderr(err)), "not in a mode") {
			m.setCopyMode(sessionID, false)
			return PaneScroll{Wheel: wheel}, nil
		}
		return PaneScroll{}, err
	}
	after, wheelAfter, err := m.paneFlags(ctx, name, sessionID)
	if err != nil {
		return PaneScroll{}, err
	}
	return PaneScroll{InMode: after, Wheel: wheelAfter}, nil
}

// ExitCopyMode returns the pane to its live bottom. It is what the pill taps
// and what the first keystroke after a scroll does, and it is a no-op when
// nothing has been scrolled.
func (m *Manager) ExitCopyMode(ctx context.Context, sessionID string) error {
	m.scrollMu.Lock()
	defer m.scrollMu.Unlock()
	if !m.inCopyMode(sessionID) {
		return nil
	}
	name := m.tmuxNameOf(sessionID)
	if name == "" {
		m.setCopyMode(sessionID, false)
		return nil
	}
	m.setCopyMode(sessionID, false)
	if _, err := m.tmux.Run(ctx, "send-keys", "-t", name, "-X", "cancel"); err != nil {
		if noSuchTarget(err) || serverGone(err) ||
			strings.Contains(strings.ToLower(Stderr(err)), "not in a mode") {
			return nil
		}
		return err
	}
	return nil
}

// InCopyMode reports whether this session was left scrolled back by a viewer.
// It is a map read, so the input path may ask it on every keystroke.
func (m *Manager) InCopyMode(sessionID string) bool { return m.inCopyMode(sessionID) }

// paneFlags asks tmux both questions in one command, and records the copy-mode
// half - so that a pane which left copy mode by itself, through the -e exit at
// the bottom or a program that redrew, is not remembered as scrolled.
func (m *Manager) paneFlags(ctx context.Context, tmuxName, sessionID string) (inMode, wheel bool, err error) {
	out, err := m.tmux.Run(ctx, "display-message", "-p", "-t", tmuxName,
		"#{pane_in_mode},#{alternate_on}")
	if err != nil {
		if noSuchTarget(err) || serverGone(err) {
			m.setCopyMode(sessionID, false)
			return false, false, nil
		}
		return false, false, err
	}
	fields := strings.SplitN(strings.TrimSpace(out), ",", 2)
	inMode = fields[0] == "1"
	wheel = len(fields) > 1 && fields[1] == "1"
	m.setCopyMode(sessionID, inMode)
	return inMode, wheel, nil
}

func (m *Manager) inCopyMode(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scrolled[sessionID]
}

func (m *Manager) setCopyMode(sessionID string, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		if m.scrolled == nil {
			m.scrolled = map[string]bool{}
		}
		m.scrolled[sessionID] = true
		return
	}
	delete(m.scrolled, sessionID)
}
