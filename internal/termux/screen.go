package termux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The read of a pane without a browser in the middle. It is here rather than
// in manager.go because it belongs to the features that talk *about* a
// terminal - a Shell's title, read from what it shows - and not to the
// substrate that keeps one alive.

// CaptureLines is how much of a screen is read when the caller does not say.
const CaptureLines = 40

// maxCaptureLines bounds one capture. A pane's history is configurable and can
// be enormous; the callers want a screen, not a scrollback.
const maxCaptureLines = 5000

// CapturePane returns the last n lines of a session's pane, joined and free of
// escape sequences.
//
// `-J` rejoins the wrapped lines, so a long sentence is one line rather than
// the terminal's idea of one; without `-e` there are no escapes at all, which
// is why nothing downstream has to strip ANSI. Trailing blank lines - a
// full-screen program leaves plenty - are trimmed, because they are the whole
// bottom half of a prompt that has been given to a model as context.
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
