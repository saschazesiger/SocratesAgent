package termux

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// pane is one line of `list-panes -a`.
type pane struct {
	session     string
	dead        bool
	deadStatus  int
	piped       bool
	currentPath string
}

const paneFormat = "#{session_name}|#{pane_dead}|#{pane_dead_status}|#{pane_pipe}|#{pane_current_path}"

func parsePanes(out string) map[string]pane {
	panes := map[string]pane{}
	for _, line := range Lines(out) {
		f := strings.SplitN(line, "|", 5)
		if len(f) < 5 {
			continue
		}
		p := pane{session: f[0], dead: f[1] == "1", deadStatus: -1, piped: f[3] == "1", currentPath: f[4]}
		if n, err := strconv.Atoi(strings.TrimSpace(f[2])); err == nil {
			p.deadStatus = n
		}
		// One session, one window, one pane: a later line for the same session
		// would only be a pane we did not make.
		if _, seen := panes[p.session]; !seen {
			panes[p.session] = p
		}
	}
	return panes
}

// Adopt reconciles what tmux has with what the database says, and is run
// before the server starts serving.
//
// Socrates holds no state a restart may lose, so this is the whole of coming
// back: every running session is re-adopted, every dead pane becomes an exit,
// every session the machine lost becomes one to resume, and every tmux session
// of ours without a row is taken in rather than killed.
func (m *Manager) Adopt(ctx context.Context) error {
	if err := m.unavailable; err != nil {
		return nil
	}
	rows, err := m.st.ListSessions(true)
	if err != nil {
		return err
	}
	if !m.tmux.Running(ctx) {
		// Nothing is running, which after a reboot is the ordinary case and
		// not an error. Everything the database thinks is alive is a candidate
		// for resuming, and nothing is relaunched here: coming back from a
		// reboot with forty sessions must not start forty programs.
		m.markLost(rows)
		return nil
	}
	m.refreshServerEnv(ctx)
	m.setHooks(ctx)
	m.mu.Lock()
	m.hooksSet = true
	m.mu.Unlock()

	out, err := m.tmux.Run(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return err
	}
	panes := parsePanes(out)

	known := map[string]bool{}
	for i := range rows {
		row := &rows[i]
		known[row.TmuxName] = true
		if row.State != store.StateRunning && row.State != store.StateStarting {
			continue
		}
		p, ok := panes[row.TmuxName]
		switch {
		case !ok:
			_ = m.st.SetSessionState(row.ID, store.StateNeedsResume, row.ExitStatus, "")
		case p.dead:
			m.markExited(row, p.deadStatus)
		default:
			m.adopt(ctx, row, p)
		}
	}

	for name, p := range panes {
		if known[name] {
			continue
		}
		if _, ok := SessionID(name); !ok {
			// Not ours. Somebody else's session on our socket is still not
			// ours to touch.
			continue
		}
		m.recover(ctx, name, p)
	}
	return nil
}

// adopt takes a session that is still running back under management.
func (m *Manager) adopt(ctx context.Context, row *store.Session, p pane) {
	if err := m.applySizePolicy(ctx, row.TmuxName, row.Cols, row.Rows); err != nil {
		m.logf("could not re-apply the size policy to %s: %v", row.TmuxName, err)
	}
	if !p.piped {
		// Only when the pipe is closed: pipe-pane without -o replaces a
		// running sink, so re-issuing it needlessly would restart the journal
		// writer for nothing.
		if err := m.attachJournal(ctx, row.ID, row.TmuxName); err != nil {
			m.logf("could not re-attach the journal of %s: %v", row.ID, err)
		}
	}
	m.mu.Lock()
	m.live[row.ID] = &liveSession{id: row.ID, tmuxName: row.TmuxName, cols: row.Cols, rows: row.Rows}
	delete(m.missed, row.ID)
	m.mu.Unlock()
	if row.State != store.StateRunning {
		_ = m.st.SetSessionState(row.ID, store.StateRunning, row.ExitStatus, "")
	}
}

// recover takes in a session of ours that has no row.
//
// It is never killed. A restored database, a failed migration or a crash in
// the moment between a tmux session appearing and its row being written must
// not destroy running work; Socrates only ever kills a session the user asked
// it to delete.
func (m *Manager) recover(ctx context.Context, tmuxName string, p pane) {
	id, ok := SessionID(tmuxName)
	if !ok {
		return
	}
	workdir := p.currentPath
	if workdir == "" {
		workdir = m.cfg.DataDir
	}
	row := &store.Session{
		ID:              id,
		Title:           "Recovered session",
		Harness:         "shell",
		Workdir:         workdir,
		WorkdirMode:     store.WorkdirCustom,
		TmuxName:        tmuxName,
		CLISessionState: store.CLINone,
		State:           store.StateRunning,
	}
	if err := m.st.CreateSession(row); err != nil {
		m.logf("could not take in the unrecorded tmux session %s: %v", tmuxName, err)
		return
	}
	m.logf("took in the tmux session %s, which had no record in the database, as %q", tmuxName, row.Title)
	m.adopt(ctx, row, p)
}

func (m *Manager) markLost(rows []store.Session) {
	for i := range rows {
		row := &rows[i]
		if row.State == store.StateRunning || row.State == store.StateStarting {
			_ = m.st.SetSessionState(row.ID, store.StateNeedsResume, row.ExitStatus, "")
		}
	}
}

// ---------------------------------------------------------------- polling

// StartPoll runs the reconciliation loop until the context ends. It is the
// second of the three ways a dead pane is noticed - the hook is the first and
// a viewer reading end of file is the third - and it is the one that is
// allowed to be slow, because it is the one that cannot be missed.
func (m *Manager) StartPoll(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Poll(ctx)
			}
		}
	}()
}

// Poll asks tmux once what is still alive and reconciles the answer.
func (m *Manager) Poll(ctx context.Context) {
	if m.unavailable != nil {
		return
	}
	rows, err := m.st.ListSessions(true)
	if err != nil {
		return
	}
	if _, err := os.Stat(m.tmux.Sock); err != nil {
		// No socket is evidence enough on its own: the server is not merely
		// busy, it is not there.
		m.mu.Lock()
		m.pollFails = pollTolerance
		m.mu.Unlock()
		m.markLost(rows)
		return
	}
	out, err := m.tmux.Run(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		if m.notePollFailure() {
			m.markLost(rows)
		}
		return
	}
	m.notePollSuccess()
	m.applyPanes(rows, parsePanes(out))
}

// notePollFailure records one failed poll and reports whether that is now
// enough to declare the sessions lost. One failure never is: a momentary
// failure must not flip every session to needs_resume and set off a wave of
// resumes.
func (m *Manager) notePollFailure() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pollFails++
	return m.pollFails >= pollTolerance
}

func (m *Manager) notePollSuccess() {
	m.mu.Lock()
	m.pollFails = 0
	m.mu.Unlock()
}

// applyPanes moves each row to what tmux says about it.
func (m *Manager) applyPanes(rows []store.Session, panes map[string]pane) {
	for i := range rows {
		row := &rows[i]
		if row.State != store.StateRunning && row.State != store.StateStarting {
			continue
		}
		p, ok := panes[row.TmuxName]
		switch {
		case !ok:
			if m.noteMissing(row.ID) {
				_ = m.st.SetSessionState(row.ID, store.StateNeedsResume, row.ExitStatus, "")
			}
		case p.dead:
			m.clearMissing(row.ID)
			m.markExited(row, p.deadStatus)
		default:
			m.clearMissing(row.ID)
		}
	}
}

func (m *Manager) noteMissing(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.missed[id]++
	return m.missed[id] >= pollTolerance
}

func (m *Manager) clearMissing(id string) {
	m.mu.Lock()
	delete(m.missed, id)
	m.mu.Unlock()
}
