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
	// Wait is how long to sleep after this action, capped at MaxKeyWait.
	Wait time.Duration
}

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
	var args []string
	switch {
	case key.Text != "":
		// -H rather than -l: byte exact for UTF-8, and immune to the
		// differences between tmux versions in how -l treats a backslash.
		args = []string{"send-keys", "-t", name, "-H"}
		for _, b := range []byte(key.Text) {
			args = append(args, hex.EncodeToString([]byte{b}))
		}
	case key.Name != "":
		args = []string{"send-keys", "-t", name, key.Name}
	default:
		// A pause and nothing else.
		return nil
	}
	if _, err := m.tmux.Run(ctx, args...); err != nil {
		if noSuchTarget(err) || serverGone(err) {
			return fmt.Errorf("session %s has no pane to type into", id)
		}
		return err
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
