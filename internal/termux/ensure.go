package termux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// again on its own conversation, and one that failed is handed back with the
// reason so the page can offer a restart rather than an empty terminal.
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
	unlock := m.lockSession(sessionID)
	defer unlock()
	row, err := m.st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if m.Available() == nil && row.TmuxName != "" && !m.paneIsDead(row.TmuxName) {
		return nil, fmt.Errorf("%w: %s", ErrStillRunning, row.TmuxName)
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

// resume starts the program again on the conversation the row remembers.
//
// The verification before it is not a nicety: an id that is *provably* gone
// makes `claude --resume` and `opencode --session` exit immediately, so that
// case becomes a fresh session here rather than a pane that dies on sight.
//
// A verification that could not be answered is a different thing and is
// treated differently, as §C.5 and §C.6 require: the id is kept and the resume
// is attempted anyway. A transient error - an unreadable directory, a
// CODEX_HOME missing from Socrates' own environment - must not permanently
// discard a conversation, and a resume that then fails says so in the pane,
// which is honest and recoverable with the Restart button.
func (m *Manager) resume(ctx context.Context, row *store.Session) (*store.Session, error) {
	if err := m.Available(); err != nil {
		return nil, err
	}
	h, ok := harnesses.Get(row.Harness)
	if !ok {
		return nil, fmt.Errorf("%q is not a harness this Socrates knows", row.Harness)
	}
	if m.cfg.Settings == nil {
		return nil, errors.New("this manager was built without settings, so it cannot plan a resume")
	}
	req := harnesses.PlanRequest{
		SessionID:  row.ID,
		Title:      row.Title,
		Cwd:        row.Workdir,
		Model:      row.Model,
		Effort:     row.Effort,
		CLISession: strings.TrimSpace(row.CLISessionID),
		Settings:   m.cfg.Settings(),
		DataDir:    m.cfg.DataDir,
	}
	if row.CLISessionState == store.CLILost {
		req.CLISession = ""
	}
	verified, wanted := false, req.CLISession
	if req.CLISession != "" {
		alive, err := h.VerifyCLISession(ctx, req)
		switch {
		case err != nil:
			m.logf("could not tell whether session %s can still be resumed, trying it anyway: %v", row.ID, err)
		case alive:
			verified = true
		default:
			_ = m.st.SetSessionCLI(row.ID, "", store.CLILost)
			req.CLISession = ""
		}
	}

	var plan harnesses.LaunchPlan
	var err error
	if req.CLISession != "" {
		plan, err = h.ResumePlan(ctx, req)
		if errors.Is(err, harnesses.ErrNoResume) {
			req.CLISession = ""
			verified = false
		} else if err != nil {
			return m.planFailed(row, err)
		}
	}
	if req.CLISession == "" {
		if plan, err = h.Plan(ctx, req); err != nil {
			return m.planFailed(row, err)
		}
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
	// What the resume actually did. A relaunch that had to start a fresh
	// conversation is indistinguishable from one that continued the old one in
	// every stored field, and the browser has to be able to say so.
	m.noteResume(row.ID, ResumeNote{
		// Fresh means there was a conversation and this is not it. A session
		// that never had one - a shell, or a Codex pane nobody typed into -
		// has lost nothing and says nothing.
		Fresh: wanted != "" && plan.CLISession != wanted,
		From:  wanted,
		At:    time.Now().UnixMilli(),
	})
	cliState := cliStateFor(plan)
	if verified {
		cliState = store.CLIVerified
	}
	if err := m.st.SetSessionCLI(row.ID, plan.CLISession, cliState); err != nil {
		m.logf("could not record the conversation id of session %s: %v", row.ID, err)
	}
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

// ResumeNote is what the last resume of a session did, kept because no stored
// field distinguishes the two outcomes: a session that came back on its own
// conversation and one that had to start a new one are byte for byte the same
// row, and §C.8 requires the browser to be able to say "the previous
// conversation could not be resumed".
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
// the watcher still looking for a conversation id it will never need.
func (m *Manager) forgetSession(id string) {
	m.mu.Lock()
	stop := m.watchers[id]
	delete(m.watchers, id)
	delete(m.locks, id)
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	m.ClearResumeNote(id)
}

// ---------------------------------------------------------- id discovery

// watchCLISession learns the program's own session id for the two harnesses
// that will not be told one, and is what arms a later resume.
//
// It runs detached because it can take as long as the user takes: neither
// Codex nor OpenCode has a session to name until a real turn has happened, so
// "nothing yet" is the ordinary state of a session somebody has opened and not
// typed into. The session works throughout; only reboot-resume is not armed
// until this returns. Both watchers give up on their own after a quarter of an
// hour, and the manager's context ends them when Socrates stops.
func (m *Manager) watchCLISession(id string, plan harnesses.LaunchPlan, since time.Time) {
	if plan.CLISession != "" {
		return // Chosen by us, or resumed: there is nothing to find out.
	}
	switch plan.Discover {
	case harnesses.DiscoverCodexRollout, harnesses.DiscoverOpenCodeAPI:
	default:
		return
	}
	parent := m.discoverCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := context.WithCancel(parent)
	m.mu.Lock()
	if previous := m.watchers[id]; previous != nil {
		previous()
	}
	m.watchers[id] = stop
	m.mu.Unlock()
	go func() {
		defer stop()
		found, err := m.discoverCLISession(ctx, id, plan, since)
		switch {
		case errors.Is(err, context.Canceled):
			// Socrates is shutting down; the session is not, and the id is
			// looked for again the next time it is opened.
		case err != nil:
			m.logf("could not learn the conversation id of session %s: %v", id, err)
		case found == "":
			m.logf("session %s has no conversation to resume yet", id)
		default:
			if err := m.st.SetSessionCLI(id, found, store.CLIKnown); err != nil {
				m.logf("could not record the conversation id of session %s: %v", id, err)
			}
		}
	}()
}

// RearmCLIWatch starts the session-id watcher again for a session that
// outlived the Socrates that launched it.
//
// Without it the ownership of the discoverers is only half taken: a Codex or
// OpenCode session adopted after a restart, an upgrade or a crash would stay
// `pending` for ever and could never be resumed - which is exactly the
// durability the product is built around. Everything the watcher needs is in
// plan.json, including OpenCode's server password, which is why that file is
// written 0600.
func (m *Manager) RearmCLIWatch(row *store.Session) {
	if row.CLISessionState != store.CLIPending || row.CLISessionID != "" {
		return
	}
	plan, err := m.readPlan(row.ID)
	if err != nil {
		m.logf("could not read back the launch plan of session %s: %v", row.ID, err)
		return
	}
	// The session was launched before this process existed, so "since" is when
	// the row says it started, not now.
	since := time.UnixMilli(row.CreatedAt)
	if row.CreatedAt == 0 {
		since = time.Now()
	}
	m.watchCLISession(row.ID, plan, since)
}

// readPlan is the launch plan as it was written next to the session.
func (m *Manager) readPlan(id string) (harnesses.LaunchPlan, error) {
	var plan harnesses.LaunchPlan
	raw, err := os.ReadFile(filepath.Join(SessionDir(m.cfg.DataDir, id), "plan.json"))
	if err != nil {
		return plan, err
	}
	return plan, json.Unmarshal(raw, &plan)
}

func (m *Manager) discoverCLISession(ctx context.Context, id string, plan harnesses.LaunchPlan, since time.Time) (string, error) {
	// The launch time and the ids other sessions of this harness already hold
	// are what keep a directory with history - a preset, or one the user has
	// run the CLI in by hand - from handing this pane somebody else's
	// conversation.
	d := harnesses.Discovery{
		Cwd:     plan.Cwd,
		Since:   since,
		Claimed: func(cli string) bool { return m.cliHeldElsewhere(id, cli) },
	}
	switch plan.Discover {
	case harnesses.DiscoverCodexRollout:
		return harnesses.WatchRollout(ctx, codexHome(plan.Env), d)
	case harnesses.DiscoverOpenCodeAPI:
		access, ok := openCodeAccess(id, plan)
		if !ok {
			return "", errors.New("the OpenCode server of this session has no credentials on record")
		}
		return harnesses.DiscoverOpenCodeSession(ctx, access, d)
	}
	return "", nil
}

// openCodeAccess is how to talk to one session's TUI server: what the
// launcher remembered if this process started it, and otherwise the plan the
// launch was written down as - which is the only copy that survives a restart,
// and the reason plan.json holds the password at 0600.
func openCodeAccess(id string, plan harnesses.LaunchPlan) (harnesses.ServerAccess, bool) {
	if access, ok := harnesses.OpenCodeAccess(id); ok {
		return access, true
	}
	password := plan.Env["OPENCODE_SERVER_PASSWORD"]
	if plan.Port <= 0 || password == "" {
		return harnesses.ServerAccess{}, false
	}
	return harnesses.ServerAccess{
		Port:     plan.Port,
		Username: plan.Env["OPENCODE_SERVER_USERNAME"],
		Password: password,
	}, true
}

// cliHeldElsewhere reports whether another session of the same harness has
// already recorded a conversation id. Neither Codex nor OpenCode offers a
// per-session handle, so two sessions started in one directory would otherwise
// both claim the first conversation that appears there.
func (m *Manager) cliHeldElsewhere(id, cli string) bool {
	if cli == "" {
		return false
	}
	rows, err := m.st.ListSessions(true)
	if err != nil {
		// Not knowing is not the same as knowing it is free, but refusing
		// every id on a database hiccup would leave the session pending for
		// ever; the launch-time bound still holds.
		return false
	}
	for _, row := range rows {
		if row.ID != id && row.CLISessionID == cli {
			return true
		}
	}
	return false
}

// codexHome is where Codex keeps its rollout files, as the launched process
// will see it: the plan's own environment first, because that is what the pane
// was given, then ours, then the default beside the home directory.
func codexHome(env map[string]string) string {
	if dir := strings.TrimSpace(env["CODEX_HOME"]); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}
