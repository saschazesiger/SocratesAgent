package termux

import (
	"context"
	"os"
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

// The current path comes last because it is the one field that could contain
// the separator; everything after it would be lost, and there is nothing
// after it.
const paneFormat = "#{session_name}|#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}|" +
	"#{pane_pipe}|#{pane_current_path}"

func parsePanes(out string) map[string]pane {
	panes := map[string]pane{}
	for _, line := range Lines(out) {
		f := strings.SplitN(line, "|", 6)
		if len(f) < 6 {
			continue
		}
		p := pane{
			session:     f[0],
			dead:        f[1] == "1",
			deadStatus:  exitStatus(f[2], f[3]),
			piped:       f[4] == "1",
			currentPath: f[5],
		}
		// One session, one window, one pane: a later line for the same session
		// would only be a pane we did not make.
		if _, seen := panes[p.session]; !seen {
			panes[p.session] = p
		}
	}
	return panes
}

// listPanes asks the server for every pane it has.
//
// A live server that has lost its last session refuses `list-panes -a` with
// "no current target" - it is a command that needs one. That is an empty
// answer, not a failure: taking it for one would make Adopt error out on a
// perfectly healthy server, and would leave the poll one tick away from
// declaring a reboot for as long as nobody had a session open.
func (m *Manager) listPanes(ctx context.Context) (map[string]pane, error) {
	out, err := m.tmux.Run(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		if noSuchTarget(err) {
			return map[string]pane{}, nil
		}
		return nil, err
	}
	return parsePanes(out), nil
}

// Adopt reconciles what tmux has with what the database says, and is run
// before the server starts serving.
//
// Socrates holds no state a restart may lose, so this is the whole of coming
// back: every running session is re-adopted, every dead pane becomes an exit,
// every session the machine lost becomes one to resume, and every tmux session
// of ours without a row is taken in rather than killed.
func (m *Manager) Adopt(ctx context.Context) error {
	if err := m.Available(); err != nil {
		return nil
	}
	rows, err := m.st.ListSessions(true)
	if err != nil {
		return err
	}
	running, err := m.tmux.Running(ctx)
	if err != nil {
		// We could not tell. Leaving every row alone and letting the poll
		// decide is the only safe answer: "there is no server" is the sentence
		// that moves every session to needs_resume.
		return err
	}
	if !running {
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

	panes, err := m.listPanes(ctx)
	if err != nil {
		return err
	}

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
	// A session that outlived the Socrates which launched it still has no
	// conversation id of its own, and the watcher that was looking for one
	// died with that process. Started again here, a Codex or OpenCode session
	// can still be resumed after the next reboot.
	m.RearmCLIWatch(row)
}

// RecoveredTitle is the name a session taken in without a row is given. It is
// also how the Setup check finds them: a recovered session that somebody has
// renamed is one somebody has dealt with, and it stops being reported.
const RecoveredTitle = "Recovered session"

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
		Title:           RecoveredTitle,
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
	if m.Available() != nil {
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
	panes, err := m.listPanes(ctx)
	if err != nil {
		if m.notePollFailure() {
			m.markLost(rows)
		}
		return
	}
	m.notePollSuccess()
	m.applyPanes(rows, panes)
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
		if row.State != store.StateRunning && row.State != store.StateStarting &&
			row.State != store.StateExited {
			continue
		}
		p, ok := panes[row.TmuxName]
		switch {
		case !ok:
			if row.State == store.StateExited {
				// A session that has gone entirely is not news about a row
				// that already says its program ended.
				continue
			}
			if m.noteMissing(row.ID) {
				_ = m.st.SetSessionState(row.ID, store.StateNeedsResume, row.ExitStatus, "")
			}
		case p.dead:
			m.clearMissing(row.ID)
			m.markExited(row, p.deadStatus)
		default:
			m.clearMissing(row.ID)
			if row.State == store.StateStarting {
				// The pane is alive and the row never caught up - the last
				// write of a create that failed after tmux had done its part.
				// Left alone, the next viewer waits ten seconds and then
				// writes "failed" over a working terminal.
				_ = m.st.SetSessionState(row.ID, store.StateRunning, -1, "")
			}
			if row.State == store.StateExited {
				// The pane is alive and the row says it is not. The pane is
				// the authority: a stale hook, from a session relaunched
				// under the same name, must not leave a working terminal
				// behind the exit overlay for ever.
				_ = m.st.SetSessionState(row.ID, store.StateRunning, -1, "")
			}
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
