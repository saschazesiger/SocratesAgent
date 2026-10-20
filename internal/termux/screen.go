package termux

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The two primitives that read and drive a pane without a browser in the
// middle. They are here rather than in manager.go because they belong to the
// features that talk *about* a terminal - the spoken status and the operator
// loop - and not to the substrate that keeps one alive.

// CaptureLines is how much of a screen is read when the caller does not say.
const CaptureLines = 40

// maxCaptureLines bounds one capture. A pane's history is configurable and can
// be enormous; the callers want a screen, not a scrollback.
const maxCaptureLines = 5000

// MaxKeyWait is the longest pause one action may ask for. An operator step
// that wants longer takes two steps, which is a step somebody can watch.
const MaxKeyWait = 10 * time.Second

// CapturePane returns the last n lines of a session's pane, joined and free of
// escape sequences.
//
// `-J` rejoins the wrapped lines, so a long sentence is one line rather than
// the terminal's idea of one; without `-e` there are no escapes at all, which
// is why nothing downstream has to strip ANSI. Trailing blank lines - a TUI
// leaves plenty - are trimmed, because they are the whole bottom half of a
// prompt that has been given to a model as context.
func (m *Manager) CapturePane(ctx context.Context, id string, lines int) (string, error) {
	if err := m.Available(); err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = CaptureLines
	}
	if lines > maxCaptureLines {
		lines = maxCaptureLines
	}
	name := m.tmuxNameOf(id)
	out, err := m.tmux.Run(ctx, "capture-pane", "-p", "-J", "-t", name, "-S", "-"+strconv.Itoa(lines))
	if err != nil {
		if noSuchTarget(err) || serverGone(err) {
			return "", fmt.Errorf("session %s has no pane to read", id)
		}
		return "", err
	}
	return lastLines(out, lines), nil
}

// visiblePane is what a session's pane shows right now, and none of its
// history: the screen a line that announces something live - "1 shell", "1
// background terminal running" - can only be on. A TUI that clears its
// screen leaves the old one in the history (tmux's scroll-on-clear), so a
// line that has gone from the screen is still in any capture that reaches
// back.
func (m *Manager) visiblePane(ctx context.Context, id string) (string, error) {
	if err := m.Available(); err != nil {
		return "", err
	}
	out, err := m.tmux.Run(ctx, "capture-pane", "-p", "-J", "-t", m.tmuxNameOf(id))
	if err != nil {
		if noSuchTarget(err) || serverGone(err) {
			return "", fmt.Errorf("session %s has no pane to read", id)
		}
		return "", err
	}
	return lastLines(out, 0), nil
}

// VisiblePane is visiblePane for the server: the screen as it is now and none
// of its history. Home's digests hash it to tell a prompt that moved on from
// one that is still the same, and a capture that reached back would change
// with every line the history gained, not with what the person can see.
func (m *Manager) VisiblePane(ctx context.Context, id string) (string, error) {
	return m.visiblePane(ctx, id)
}

// StyledPane is VisiblePane with its colours and attributes kept as SGR
// escapes (`-e`). It is for the one thing the text of a screen does not
// carry: which option a TUI that highlights in colour only - OpenCode's cards
// - has its highlight on.
func (m *Manager) StyledPane(ctx context.Context, id string) (string, error) {
	if err := m.Available(); err != nil {
		return "", err
	}
	out, err := m.tmux.Run(ctx, "capture-pane", "-p", "-J", "-e", "-t", m.tmuxNameOf(id))
	if err != nil {
		if noSuchTarget(err) || serverGone(err) {
			return "", fmt.Errorf("session %s has no pane to read", id)
		}
		return "", err
	}
	return lastLines(out, 0), nil
}

// lastLines trims the trailing blanks and keeps the final n lines, so that
// "the last hundred and twenty lines" is what the caller actually gets:
// capture-pane -S -n hands back n lines of history *plus* the visible pane.
func lastLines(out string, n int) string {
	rows := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	if n > 0 && len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

// Key is one action against a pane: literal text, a named key, or a pause.
type Key struct {
	// Text is literal UTF-8, sent as hex bytes so that no version of tmux gets
	// to interpret a backslash of ours.
	Text string
	// Name is a tmux key name: Enter, Escape, Tab, Up, C-c, and the rest.
	Name string
	// Repeat is how many times Name is pressed, for a value above one: a
	// line cleared with BSpace is one action, not one per character.
	Repeat int
	// Wait is how long to sleep after this action, capped at MaxKeyWait.
	Wait time.Duration
}

// repeatChunk is how many presses of one key go in one send-keys, so that a
// long run of them is a few commands and none with an argument list of
// thousands.
const repeatChunk = 200

// SendKeys types into a pane.
//
// It goes through `tmux send-keys`, which needs no attached viewer: an
// operator run keeps working with every browser closed, which is the whole
// point of running it on the server.
func (m *Manager) SendKeys(ctx context.Context, id string, keys []Key) error {
	if err := m.Available(); err != nil {
		return err
	}
	name := m.tmuxNameOf(id)
	for _, key := range keys {
		if err := m.sendKey(ctx, name, id, key); err != nil {
			return err
		}
		// The same rules a keystroke from a browser arms: whatever the pane
		// does after this was asked for, so its end is worth a chime, and a
		// permission prompt that goes quiet afterwards has been answered. The
		// unread mark is not touched - nobody has read anything.
		m.NoteTyped(id)
		if key.Wait > 0 {
			wait := key.Wait
			if wait > MaxKeyWait {
				wait = MaxKeyWait
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return nil
}

func (m *Manager) sendKey(ctx context.Context, name, id string, key Key) error {
	var calls [][]string
	switch {
	case key.Text != "":
		// -H rather than -l: byte exact for UTF-8, and immune to the
		// differences between tmux versions in how -l treats a backslash.
		args := []string{"send-keys", "-t", name, "-H"}
		for _, b := range []byte(key.Text) {
			args = append(args, hex.EncodeToString([]byte{b}))
		}
		calls = append(calls, args)
	case key.Name != "":
		// Several key names in one send-keys are pressed in order, which is
		// the same in every tmux (-N is not).
		for left := max(key.Repeat, 1); left > 0; left -= repeatChunk {
			args := []string{"send-keys", "-t", name}
			for range min(left, repeatChunk) {
				args = append(args, key.Name)
			}
			calls = append(calls, args)
		}
	default:
		// A pause and nothing else.
		return nil
	}
	for _, args := range calls {
		if _, err := m.tmux.Run(ctx, args...); err != nil {
			if noSuchTarget(err) || serverGone(err) {
				return fmt.Errorf("session %s has no pane to type into", id)
			}
			return err
		}
	}
	return nil
}

// tmuxNameOf is the tmux session name a Socrates session id runs under. The
// row is asked first, because the name is stored with it rather than
// recomputed, and a session adopted under a name of its own would otherwise be
// missed.
func (m *Manager) tmuxNameOf(id string) string {
	if row, err := m.st.GetSession(id); err == nil && row.TmuxName != "" {
		return row.TmuxName
	}
	return TmuxName(id)
}
