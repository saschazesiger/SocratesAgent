package termux

import (
	"context"
	"strconv"
	"strings"
	"sync"
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
	// asked - which is what all three CLIs do. Claude Code is one of them as
	// this product launches it: the fixed policy sets CLAUDE_CODE_NO_FLICKER=1
	// (harnesses/claude.go), and with it claude takes the alternate screen and
	// tracks the mouse from its idle prompt onward. Only the Shell harness is
	// on the normal buffer.
	Wheel bool
}

// PaneState answers both questions without moving anything. The handshake
// asks it, so that the first drag of a session already takes the right route.
func (m *Manager) PaneState(ctx context.Context, sessionID string) PaneScroll {
	unlock := m.lockScroll(sessionID)
	defer unlock()
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
	unlock := m.lockScroll(sessionID)
	defer unlock()
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
		if _, err := m.tmux.Run(ctx, append(m.copyModeArgs(), "-t", name)...); err != nil {
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
	unlock := m.lockScroll(sessionID)
	defer unlock()
	_, err := m.leaveCopyMode(ctx, sessionID)
	return err
}

// LeaveCopyMode takes the pane out of copy mode for a keystroke and then runs
// write, with the session's scroll lock held across both.
//
// The lock is the point. Checking whether the pane is in copy mode and then
// writing the byte are two steps, and between them another device's finger can
// run a whole copy-mode command: the check says "live", the scroll says "in
// mode", and the keystroke lands in copy mode and is read as one of its
// commands. Holding the lock across the pair closes that window, and it is
// per session, so one pane's tmux call cannot delay another pane's keystroke.
//
// write is always called, whether or not there was a mode to leave. The first
// result is whether the pane actually left copy mode, which is what decides
// whether every viewer is told the pill is gone - a failed cancel must not
// take the pill off a pane that is still held above its bottom.
func (m *Manager) LeaveCopyMode(ctx context.Context, sessionID string, write func() error) (bool, error) {
	unlock := m.lockScroll(sessionID)
	defer unlock()
	left, err := m.leaveCopyMode(ctx, sessionID)
	if err != nil {
		m.logf("session %s: could not leave copy mode to type: %v", sessionID, err)
	}
	return left, write()
}

// leaveCopyMode is the body of both, and expects the session's lock to be held.
func (m *Manager) leaveCopyMode(ctx context.Context, sessionID string) (bool, error) {
	if !m.inCopyMode(sessionID) {
		return false, nil
	}
	name := m.tmuxNameOf(sessionID)
	if name == "" {
		m.setCopyMode(sessionID, false)
		return true, nil
	}
	if _, err := m.tmux.Run(ctx, "send-keys", "-t", name, "-X", "cancel"); err != nil {
		if noSuchTarget(err) || serverGone(err) ||
			strings.Contains(strings.ToLower(Stderr(err)), "not in a mode") {
			// There is no mode there to be in; the flag was stale.
			m.setCopyMode(sessionID, false)
			return true, nil
		}
		// The pane is still in copy mode as far as anybody knows, so the flag
		// stays up and so does the pill.
		return false, err
	}
	m.setCopyMode(sessionID, false)
	return true, nil
}

// copyModeArgs is how this tmux is asked to enter copy mode.
//
// -e leaves the mode by itself at the bottom, so dragging up until the pane
// stops moving is a complete gesture. -H hides tmux's own position indicator -
// `23:13 [40/464]` in yellow, top right - which is the one thing on screen
// this product does not paint white (DESIGN.md E.10). It arrived in tmux 3.4;
// on 3.3 the indicator is shown, which is a visible deviation on that one
// version and better than no scrollback at all.
func (m *Manager) copyModeArgs() []string {
	args := []string{"copy-mode", "-e"}
	if !m.TmuxVersion().Less(3, 4) {
		args = append(args, "-H")
	}
	return args
}

// lockScroll takes this session's scroll lock and answers with its release.
//
// Per session rather than one global lock: PaneState runs a tmux subprocess on
// every handshake, and a global lock would put every reconnect in the queue
// behind one slow session's pane.
func (m *Manager) lockScroll(sessionID string) func() {
	m.mu.Lock()
	lock := m.scrollLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		if m.scrollLocks == nil {
			m.scrollLocks = map[string]*sync.Mutex{}
		}
		m.scrollLocks[sessionID] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
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
