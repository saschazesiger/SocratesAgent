package termux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// startingGrace is how long a session that is still being created gets before
// a viewer is told it will not start. Creating one is a handful of tmux
// commands, so ten seconds is not patience, it is the point at which something
// is wrong.
const startingGrace = 10 * time.Second

// Ensure makes a session ready to be watched, and is what every viewer calls
// before it attaches.
//
// It is the whole of the recovery story seen from the browser: a session that
// is running is returned as it is, one whose machine was rebooted is started
// again in its own folder, and one that failed is handed back with the reason
// so the page can offer a restart rather than an empty terminal.
//
// Nothing is resumed eagerly anywhere else: coming back from a reboot with
// forty stored sessions must not start forty programs, so the relaunch happens
// here, when somebody actually opens one.
// Two viewers opening the same session at once - a phone and a laptop, which
// is the product's stated case - must not both relaunch it, so the whole
// decision is taken under a per session lock and the row is read again inside
// it. The loser waits for the relaunch and is answered with its result.
func (m *Manager) Ensure(ctx context.Context, sessionID string) (*store.Session, error) {
	unlock := m.lockSession(sessionID)
	defer unlock()
	return m.ensureLocked(ctx, sessionID)
}

func (m *Manager) ensureLocked(ctx context.Context, sessionID string) (*store.Session, error) {
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if row.State == store.StatePrestart {
		return row, ErrNotStarted
	}
	if row.Status == store.StatusDone {
		return row, ErrDone
	}
	switch row.State {
	case store.StateRunning, store.StateExited, store.StateFailed:
		// Exited attaches too: the pane is dead but the screen it left is
		// still there, and the exit overlay is drawn over it.
		return row, nil
	case store.StateStarting:
		return m.waitForStart(ctx, row)
	case store.StateNeedsResume:
		return m.resume(ctx, row)
	}
	return row, nil
}

// Restart is the exit overlay's button: the session is put back into the state
// a reboot would have left it in, and resumed from there.
//
// A session whose pane is still alive is refused, and refused *before* the row
// is touched. Marking it first and finding out afterwards is how a double tap
// on the button - or a restart pressed a moment after the program came back on
// its own - used to leave a perfectly healthy terminal recorded as failed, in
// a state nothing polls out of again.
func (m *Manager) Restart(ctx context.Context, sessionID string) (*store.Session, error) {
	return m.RestartAfter(ctx, sessionID, nil)
}

// RestartAfter is Restart with something to do once the restart is certain to
// happen - every refusal is behind it - and before the program is launched:
// what was driving the old program is stopped there, and only there. A
// restart that is refused leaves it running.
func (m *Manager) RestartAfter(ctx context.Context, sessionID string, before func()) (*store.Session, error) {
	unlock := m.lockSession(sessionID)
	defer unlock()
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if row.State == store.StatePrestart {
		// Restart would mark it needs_resume and launch it from the row,
		// without the prompt and without the settings Start resolves.
		return nil, ErrNotStarted
	}
	if row.Status == store.StatusDone {
		return nil, ErrDone
	}
	if m.Available() == nil && row.TmuxName != "" && !m.paneIsDead(row.TmuxName) {
		return nil, fmt.Errorf("%w: %s", ErrStillRunning, row.TmuxName)
	}
	if before != nil {
		before()
	}
	if err := m.st.SetSessionState(row.ID, store.StateNeedsResume, -1, ""); err != nil {
		return nil, err
	}
	row.State, row.ExitStatus, row.FailReason = store.StateNeedsResume, -1, ""
	return m.resume(ctx, row)
}

// waitForStart gives a create that is still running its grace, and calls it a
// failure when the grace runs out. A row left in starting for ever is the one
// outcome a viewer cannot do anything with.
func (m *Manager) waitForStart(ctx context.Context, row *store.Session) (*store.Session, error) {
	deadline := time.Now().Add(startingGrace)
	for {
		select {
		case <-ctx.Done():
			return row, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		current, err := m.st.GetSession(row.ID)
		if err != nil {
			return nil, err
		}
		if current.State != store.StateStarting {
			return m.ensureLocked(ctx, row.ID)
		}
		if time.Now().After(deadline) {
			if m.Available() == nil && current.TmuxName != "" && !m.paneIsDead(current.TmuxName) {
				// The pane is alive and only the row is behind - a store write
				// that failed at the end of Create. tmux is the authority, and
				// writing "failed" over a working terminal is the one outcome
				// worse than waiting.
				_ = m.st.SetSessionState(row.ID, store.StateRunning, -1, "")
				return m.st.GetSession(row.ID)
			}
			reason := fmt.Sprintf("the session did not start within %s", startingGrace)
			_ = m.st.SetSessionState(row.ID, store.StateFailed, -1, reason)
			current.State, current.FailReason = store.StateFailed, reason
			return current, nil
		}
	}
}

// resume starts the program again in the session's folder. A terminal has no
// conversation to continue - a Shell is a new shell, VS Code opens the folder
// again - so a resume is the plan the session would be created with now.
func (m *Manager) resume(ctx context.Context, row *store.Session) (*store.Session, error) {
	if err := m.Available(); err != nil {
		return nil, err
	}
	h, ok := harnesses.TerminalOf(row.Harness)
	if !ok {
		// An agent's terminal from before the agents became chats. The
		// migration removes every one of them, so this is a row that came
		// back from somewhere else - a restored database - and it says so
		// rather than starting something that is not there any more.
		return m.planFailed(row, fmt.Errorf("%q does not run in a terminal", row.Harness))
	}
	if m.cfg.Settings == nil {
		return nil, errors.New("this manager was built without settings, so it cannot plan a resume")
	}
	plan, err := h.Plan(ctx, harnesses.PlanRequest{
		SessionID: row.ID,
		Cwd:       row.Workdir,
		Settings:  m.cfg.Settings(),
		DataDir:   m.cfg.DataDir,
	})
	if err != nil {
		return m.planFailed(row, err)
	}
	if err := m.Relaunch(ctx, row, plan); err != nil {
		// The row already carries the reason, unless the refusal was that the
		// session is still running, which leaves it untouched. Either way the
		// caller has to see the error: swallowing it made the 409 unreachable
		// and answered a refusal with a healthy-looking 200.
		current, getErr := m.st.GetSession(row.ID)
		if getErr != nil {
			return nil, err
		}
		return current, err
	}
	// A terminal has no conversation to lose, so its resume is never a fresh
	// start in the sense the note means: it says when, and nothing else.
	m.noteResume(row.ID, ResumeNote{At: time.Now().UnixMilli()})
	if err := m.st.NoteResume(row.ID); err != nil {
		m.logf("could not record the resume of session %s: %v", row.ID, err)
	}
	return m.st.GetSession(row.ID)
}

// planFailed records a launch that could not even be planned - a binary that
// is gone, a generated file that could not be written - as a failure with its
// reason, which is what the browser draws its overlay from.
func (m *Manager) planFailed(row *store.Session, err error) (*store.Session, error) {
	_ = m.st.SetSessionState(row.ID, store.StateFailed, -1, err.Error())
	return nil, err
}

// ---------------------------------------------------------- resume notes

// ResumeNote is what the last resume of a session did. It is the terminal
// protocol's resume notice (socrates.term.v1): a terminal has no conversation
// to lose, so Fresh is false and From empty for every resume this package
// makes, and the fields stay because the notice carries them.
//
// It lives in the key/value table rather than in a column: it is unread-notice
// state with the same life as the `resumed` flag, it is cleared by the same
// acknowledgement, and it goes with the session when it is deleted.
type ResumeNote struct {
	// Fresh is true when a conversation was lost and a new one was started.
	Fresh bool `json:"fresh"`
	// From is the conversation id the resume was meant to continue.
	From string `json:"resumed_from,omitempty"`
	At   int64  `json:"at"`
}

// resumeNoteKey is where one session's note lives.
func resumeNoteKey(id string) string { return "resume_note:" + id }

func (m *Manager) noteResume(id string, note ResumeNote) {
	if err := m.st.SetJSON(resumeNoteKey(id), note); err != nil {
		m.logf("could not record what the resume of session %s did: %v", id, err)
	}
}

// ResumeNoteOf returns what the last resume of a session did, and whether
// there is one to tell about.
func (m *Manager) ResumeNoteOf(id string) (ResumeNote, bool) {
	var note ResumeNote
	if err := m.st.GetJSON(resumeNoteKey(id), &note); err != nil || note.At == 0 {
		return ResumeNote{}, false
	}
	return note, true
}

// ClearResumeNote drops it, which is what acknowledging the banner does.
func (m *Manager) ClearResumeNote(id string) {
	if err := m.st.SetJSON(resumeNoteKey(id), ResumeNote{}); err != nil {
		m.logf("could not clear the resume note of session %s: %v", id, err)
	}
}

// ---------------------------------------------------------- serialisation

// lockSession takes the lock that makes everything which relaunches a session
// - Ensure, Restart, and the resume inside them - one at a time per session,
// and returns the function that gives it back.
func (m *Manager) lockSession(id string) func() {
	m.mu.Lock()
	lock := m.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[id] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// forgetSession drops what a deleted session left in the manager: its lock and
// its remembered plan.
func (m *Manager) forgetSession(id string) {
	m.mu.Lock()
	delete(m.locks, id)
	delete(m.plans, id)
	m.mu.Unlock()
	m.ClearResumeNote(id)
	// The unread mark of a session that no longer exists is not news; it is
	// clutter in a key nobody prunes.
	m.act.forget(id)
}

// ---------------------------------------------------------- the plan

// readPlan is the launch plan as it was written next to the session.
func (m *Manager) readPlan(id string) (harnesses.LaunchPlan, error) {
	var plan harnesses.LaunchPlan
	raw, err := os.ReadFile(filepath.Join(SessionDir(m.cfg.DataDir, id), "plan.json"))
	if err != nil {
		return plan, err
	}
	return plan, json.Unmarshal(raw, &plan)
}
