package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The life of a session, as the row records it.
//
// A session starts at StateStarting and reaches StateRunning once tmux has it.
// It leaves that state in one of three ways: the program inside it ended
// (StateExited, with the exit status), the machine was rebooted and the tmux
// session it lived in is gone (StateNeedsResume - the CLI can be started again
// in the same directory, on its own session id), or it never got going at all
// (StateFailed, with the reason).
const (
	StateStarting    = "starting"
	StateRunning     = "running"
	StateExited      = "exited"
	StateNeedsResume = "needs_resume"
	StateFailed      = "failed"
	// StatePrestart is a session that has been prepared and not started: its
	// settings and a prompt draft exist, and nothing else does - no tmux
	// session, no directory, no program. Every loop that watches terminals
	// leaves it alone, and the one way out of it is Start, which turns it into
	// StateStarting exactly like a new session.
	StatePrestart = "prestart"
)

// How much Socrates knows about the CLI's own session id - the id that lets
// Claude Code, Codex or OpenCode pick a conversation back up after the machine
// was rebooted.
//
// The distinction between CLINone and CLILost is what stops a resume that
// cannot work: an id that was known and is provably gone must never be passed
// to --resume, because the CLI treats that as an error rather than as a fresh
// start.
const (
	CLINone     = "none"
	CLIPending  = "pending"
	CLIKnown    = "known"
	CLIVerified = "verified"
	CLILost     = "lost"
)

// What a session row is. A terminal is a tmux pane (Shell, VS Code, and the
// agent TUIs until WP10 removes them); a chat is an agent driven through its
// protocol by an agent host, with no pane at all. Every loop that watches
// panes leaves chats alone.
const (
	KindTerminal = "terminal"
	KindChat     = "chat"
)

// How the working directory was chosen.
const (
	WorkdirDynamic = "dynamic"
	WorkdirPreset  = "preset"
	WorkdirCustom  = "custom"
)

// DefaultCols and DefaultRows are the size a session is created at when nobody
// said one. Creating a session is a REST call, so there is no viewer to ask;
// the first attach corrects it a moment later anyway.
const (
	DefaultCols = 120
	DefaultRows = 40
)

// Session is one terminal: a tmux session, the program running in it, and
// everything needed to bring both back after a restart or a reboot.
type Session struct {
	ID string `json:"id"`
	// ClientID is the key the browser generated for the request that created
	// this session. It is what makes creating one safe to retry over a link
	// that drops halfway.
	ClientID string `json:"client_id,omitempty"`
	Title    string `json:"title"`
	// TitleSource says who the name came from: TitleUser for a name a person
	// typed or a rename, TitleAuto once Socrates has had its one go at naming
	// the session, empty for the placeholder a session is created with. It is
	// persisted because "name it once, and never over a name the user chose"
	// has to survive a restart.
	TitleSource string `json:"title_source,omitempty"`
	Harness     string `json:"harness"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	// Workdir is always an absolute path, and WorkdirMode records how it was
	// arrived at, because a dynamic directory is ours to create and a custom
	// one is not.
	Workdir     string `json:"workdir"`
	WorkdirMode string `json:"workdir_mode"`
	// Options is the resolved per harness options as they were at launch. A
	// session keeps the settings it started with: changing the dashboard does
	// not reach into a terminal that is already running.
	Options json.RawMessage `json:"options,omitempty"`

	// TmuxName is stored rather than recomputed, so that a change to the id
	// format can never orphan a running tmux session.
	TmuxName string `json:"-"`
	// CLISessionID is the program's own session id, and CLISessionState says
	// how much is known about it. Neither is any of the browser's business.
	CLISessionID    string `json:"-"`
	CLISessionState string `json:"-"`

	State      string `json:"state"`
	ExitStatus int    `json:"exit_status"`
	FailReason string `json:"fail_reason,omitempty"`
	// Resumed is set while the "resumed after restart" notice has not been
	// seen yet, and ResumeCount is how often it has happened at all.
	Resumed     bool `json:"resumed"`
	ResumeCount int  `json:"resume_count"`
	Cols        int  `json:"cols"`
	Rows        int  `json:"rows"`

	CreatedAt    int64 `json:"created_at"`
	UpdatedAt    int64 `json:"updated_at"`
	LastAttached int64 `json:"last_attached"`
	// ActiveAt is the last moment the session's status changed: it started
	// working, finished, began needing an answer, or its program came up or
	// went down. It is what the list is ordered by, and it is deliberately not
	// UpdatedAt: opening a session to read it, renaming it, or resizing its
	// window are things that happen to the row, not things the session did,
	// and a row that jumped to the top for being looked at is a list that
	// cannot be read.
	ActiveAt int64 `json:"active_at"`
	// Status is where the work in this session stands, as the person sees it:
	// one of the Status constants. It is set by hand, with one exception - a
	// session that starts working again is in progress again - and it is not
	// State: a session can be running and pending, or not running and in
	// progress. StatusAt is when it was last set, which for a done session is
	// the start of the time it is kept for.
	Status   string `json:"status"`
	StatusAt int64  `json:"status_at"`
	// StartAt is when a prepared session starts by itself, in milliseconds
	// since the epoch, and zero for never. It only means something while the
	// row is StatePrestart; the transition that starts it clears it, so a
	// start that fails is never tried again on its own.
	StartAt int64 `json:"start_at"`
	// Kind is KindTerminal or KindChat. For a chat, CLISessionID is the
	// agent's own conversation id, as the host reported it.
	Kind string `json:"kind"`
}

// IsChat reports whether the row is a chat rather than a terminal.
func (s *Session) IsChat() bool { return s.Kind == KindChat }

// Where the work in a session stands. A done session has had its terminal
// closed and is deleted once its retention runs out. A background session is
// its opposite: a service somebody left running on purpose, which is kept out
// of the way, never refiled by the machine and never deleted. The other three
// keep running and differ only in how the person files them.
//
// StatusUnsorted is not a status anybody sets. It is what an upgrade from the
// build that had flags and an archive leaves on a session that was not
// archived, until the activity detector has seen whether it is working.
const (
	StatusInProgress = "in_progress"
	StatusReview     = "review"
	StatusPending    = "pending"
	StatusDone       = "done"
	StatusBackground = "background"
	StatusUnsorted   = ""
)

// ValidStatus reports whether a status is one a person may set.
func ValidStatus(status string) bool {
	switch status {
	case StatusInProgress, StatusReview, StatusPending, StatusDone, StatusBackground:
		return true
	}
	return false
}

// Where a session's name came from. The empty string is the third value: a
// session still carrying the placeholder it was created with, which is the
// only kind the automatic title may replace.
const (
	TitleUser = "user"
	TitleAuto = "auto"
)

const sessionCols = `id, client_id, title, title_source, harness, model, effort, workdir, workdir_mode,
                     options, tmux_name, cli_session_id, cli_session_state, state, exit_status,
                     fail_reason, resumed, resume_count, cols, rows,
                     created_at, updated_at, last_attached, active_at, status, status_at, start_at, kind`

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	s := &Session{}
	var options string
	var resumed int
	err := row.Scan(&s.ID, &s.ClientID, &s.Title, &s.TitleSource, &s.Harness, &s.Model, &s.Effort,
		&s.Workdir, &s.WorkdirMode, &options, &s.TmuxName, &s.CLISessionID, &s.CLISessionState,
		&s.State, &s.ExitStatus, &s.FailReason, &resumed, &s.ResumeCount, &s.Cols, &s.Rows,
		&s.CreatedAt, &s.UpdatedAt, &s.LastAttached, &s.ActiveAt, &s.Status, &s.StatusAt, &s.StartAt, &s.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if options != "" {
		s.Options = json.RawMessage(options)
	}
	s.Resumed = resumed == 1
	return s, nil
}

// CreateSession inserts a session, and is idempotent on ClientID: a request
// repeated over a flaky connection finds the row it already made instead of
// making a second one. The row is filled in on the way through, so the caller
// holds what was actually stored.
func (s *Store) CreateSession(sess *Session) error {
	// An options snapshot that is not JSON would be accepted here and then
	// break the encoding of the whole session list, not just its own row.
	if len(sess.Options) > 0 && !json.Valid(sess.Options) {
		return errors.New("session options are not valid JSON")
	}
	s.lock()
	defer s.unlock()
	if sess.CreatedAt == 0 {
		sess.CreatedAt = now()
	}
	sess.UpdatedAt = sess.CreatedAt
	sess.ActiveAt = sess.CreatedAt
	// Nothing has been attached to yet and nothing has been resumed, whatever
	// the caller's struct says: these are the row's to set, and the caller has
	// to be left holding what was actually stored.
	sess.Status, sess.StatusAt = StatusInProgress, sess.CreatedAt
	sess.Resumed, sess.LastAttached = false, 0
	if sess.State == "" {
		sess.State = StateStarting
	}
	if sess.CLISessionState == "" {
		sess.CLISessionState = CLINone
	}
	if sess.Kind == "" {
		sess.Kind = KindTerminal
	}
	if sess.Cols <= 0 {
		sess.Cols = DefaultCols
	}
	if sess.Rows <= 0 {
		sess.Rows = DefaultRows
	}
	// Nothing has exited yet, and -1 is what "no status" means in this column.
	sess.ExitStatus = -1
	options := "{}"
	if len(sess.Options) > 0 {
		options = string(sess.Options)
	}
	sess.Options = json.RawMessage(options)
	// A schedule belongs to a prepared session and to nothing else: a row
	// that is started from the outset has nothing left to start.
	if sess.State != StatePrestart || sess.StartAt < 0 {
		sess.StartAt = 0
	}
	_, err := s.db.Exec(`INSERT INTO sessions(`+sessionCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.ClientID, sess.Title, sess.TitleSource, sess.Harness, sess.Model, sess.Effort,
		sess.Workdir, sess.WorkdirMode, options, sess.TmuxName, sess.CLISessionID,
		sess.CLISessionState, sess.State, sess.ExitStatus, sess.FailReason, 0,
		sess.ResumeCount, sess.Cols, sess.Rows, sess.CreatedAt, sess.UpdatedAt, 0, sess.ActiveAt,
		sess.Status, sess.StatusAt, sess.StartAt, sess.Kind)
	if err != nil && sess.ClientID != "" && isUniqueViolation(err) {
		existing, lookupErr := s.sessionByClientID(sess.ClientID)
		if lookupErr != nil {
			return err
		}
		*sess = *existing
		return nil
	}
	if err != nil {
		return err
	}
	s.bump()
	s.touch(SessionObject(sess.ID))
	return nil
}

// isUniqueViolation reports whether an insert lost a race with itself. The
// driver has no error code to compare against, so the message is what there
// is; it is checked for the constraint by name.
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// GetSession loads one session.
func (s *Store) GetSession(id string) (*Session, error) {
	return scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id))
}

// SessionByClientID finds the session a browser already created under a key.
func (s *Store) SessionByClientID(clientID string) (*Session, error) {
	return s.sessionByClientID(clientID)
}

func (s *Store) sessionByClientID(clientID string) (*Session, error) {
	if clientID == "" {
		return nil, ErrNotFound
	}
	return scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE client_id = ?`, clientID))
}

// ListSessions returns every session, most recent status change first.
//
// The order is by ActiveAt, not UpdatedAt: a session moves in the list when
// it did something, never because somebody looked at it. Two sessions whose
// status changed in the same millisecond fall back to the row's own clock.
func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`SELECT ` + sessionCols + ` FROM sessions ORDER BY active_at DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

// update runs one statement against one session, bumps updated_at and the
// revision, and reports a session that is not there as such. Every write below
// goes through it, so no write can forget to do those three things.
func (s *Store) update(id, query string, args ...any) error {
	s.lock()
	defer s.unlock()
	args = append(args, now(), id)
	res, err := s.db.Exec(`UPDATE sessions SET `+query+`, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	s.touch(SessionObject(id))
	return nil
}

// UpdateSessionTitle renames a session. A rename is a person naming it, so it
// also closes the door on the automatic title: a name somebody typed is never
// replaced by one a model wrote.
func (s *Store) UpdateSessionTitle(id, title string) error {
	return s.update(id, `title = ?, title_source = ?`, title, TitleUser)
}

// SetAutoSessionTitle records the name the title run came up with, and marks
// the session as named so the run never happens again.
//
// It applies only while the session is still nameless. The title run reads the
// row, then spends up to twenty seconds asking a model, and somebody who
// renames the session inside that window has said what it is called: an
// unconditional write would replace their name with the model's, which is
// exactly what "a name somebody typed is never replaced by one a model wrote"
// forbids. `ErrNotFound` back means the door closed while the model was
// thinking, and the caller must not announce a name it did not set.
func (s *Store) SetAutoSessionTitle(id, title string) error {
	return s.updateNameless(id, `title = ?, title_source = ?`, title, TitleAuto)
}

// ReplaceSessionTitle records a name a model came up with because somebody
// asked for a new one, whoever named the session before. It applies only
// while the session is still called was: a rename by hand while the model was
// thinking is the newer wish, and ErrNotFound back means it won.
func (s *Store) ReplaceSessionTitle(id, was, title string) error {
	s.lock()
	defer s.unlock()
	res, err := s.db.Exec(`UPDATE sessions SET title = ?, title_source = ?, updated_at = ? WHERE id = ? AND title = ?`,
		title, TitleAuto, now(), id, was)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	s.touch(SessionObject(id))
	return nil
}

// MarkSessionTitled records that the one automatic naming has happened, for a
// run that produced nothing usable. Without it a model that answers with an
// empty string would be asked again on every later turn.
//
// It is scoped the same way: a rename during the run is the answer, and a
// session somebody has named needs no marking to stop being asked about.
func (s *Store) MarkSessionTitled(id string) error {
	return s.updateNameless(id, `title_source = ?`, TitleAuto)
}

// updateNameless is `update` for the two writes the automatic naming makes: it
// touches the row only while nothing has named it yet.
func (s *Store) updateNameless(id, query string, args ...any) error {
	s.lock()
	defer s.unlock()
	args = append(args, now(), id)
	res, err := s.db.Exec(`UPDATE sessions SET `+query+`, updated_at = ? WHERE id = ? AND title_source = ''`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	s.touch(SessionObject(id))
	return nil
}

// SetSessionState records where a session is in its life. The exit status and
// the reason travel with it, because they are only ever meaningful together
// with the state that explains them.
//
// A state that is actually new is a status change, so it moves the session in
// the list. The same state written twice - the lifecycle poll confirming what
// it already knew - is not, and leaves the order alone.
func (s *Store) SetSessionState(id, state string, exitStatus int, failReason string) error {
	return s.update(id, `active_at = CASE WHEN state <> ? THEN ? ELSE active_at END,
		state = ?, exit_status = ?, fail_reason = ?`, state, now(), state, exitStatus, failReason)
}

// SetSessionStateFrom is SetSessionState for a writer that decided on a state
// it read a moment ago: it writes only while the row is still in `from`, and
// answers ErrNotFound otherwise. The reconciliation loops decide from a
// snapshot of the whole list, and in the time between that read and their
// write a start can fail and put its row back to prepared; an unconditional
// write would then turn a prepared session - prompt and all - into one to
// resume.
func (s *Store) SetSessionStateFrom(id, from, state string, exitStatus int, failReason string) error {
	return s.setStateWhere(id, `state = ?`, from, state, exitStatus, failReason)
}

// SetStartedSessionState is SetSessionState for news about a program that
// was started - a hook saying its pane died, or that its tmux session is
// gone. Such news is about whatever the row is now, as long as it is a
// started session at all: a row that a failed start put back to prepared
// (or that was deleted) is left alone, and answers ErrNotFound.
func (s *Store) SetStartedSessionState(id, state string, exitStatus int, failReason string) error {
	return s.setStateWhere(id, `state <> ?`, StatePrestart, state, exitStatus, failReason)
}

func (s *Store) setStateWhere(id, cond string, condArg any, state string, exitStatus int, failReason string) error {
	s.lock()
	defer s.unlock()
	at := now()
	res, err := s.db.Exec(`UPDATE sessions SET active_at = CASE WHEN state <> ? THEN ? ELSE active_at END,
		state = ?, exit_status = ?, fail_reason = ?, updated_at = ? WHERE id = ? AND `+cond,
		state, at, state, exitStatus, failReason, at, id, condArg)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	s.touch(SessionObject(id))
	return nil
}

// NoteSessionActivity records that the session's status changed at `at` - it
// started working, finished, or began needing an answer - which is what moves
// it up the list. The detector decides which changes count; this only writes
// the moment down.
func (s *Store) NoteSessionActivity(id string, at int64) error {
	return s.update(id, `active_at = ?`, at)
}

// SetSessionStatus files a session under a status and starts the status's
// clock. It is the row and nothing more: closing the terminal of a session
// that is done is the manager's business, and a status that is not one of
// the five is refused rather than stored.
func (s *Store) SetSessionStatus(id, status string) error {
	if !ValidStatus(status) {
		return fmt.Errorf("%q is not a session status", status)
	}
	return s.update(id, `status = ?, status_at = ?`, status, now())
}

// SetSessionCLI records the program's own session id and how much is known
// about it. This is what a resume after a reboot is built on.
func (s *Store) SetSessionCLI(id, cliID, cliState string) error {
	return s.update(id, `cli_session_id = ?, cli_session_state = ?`, cliID, cliState)
}

// SetSessionSize remembers the size the window was last set to, so a session
// opened again in a browser that has not measured itself yet comes back the
// size it was. A size with no area is refused rather than ignored: a caller
// that computed one has a bug, and silently writing nothing is how it stays
// hidden.
func (s *Store) SetSessionSize(id string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("session size %dx%d is not a terminal", cols, rows)
	}
	return s.update(id, `cols = ?, rows = ?`, cols, rows)
}

// NoteAttach records that a viewer opened this session.
func (s *Store) NoteAttach(id string) error {
	return s.update(id, `last_attached = ?`, now())
}

// NoteResume records that the program was started again on its old
// conversation, and raises the flag that says so in the browser.
func (s *Store) NoteResume(id string) error {
	return s.update(id, `resume_count = resume_count + 1, resumed = 1`)
}

// ClearResumedFlag lowers that flag once the notice has been seen.
func (s *Store) ClearResumedFlag(id string) error {
	return s.update(id, `resumed = 0`)
}

// DeleteSession removes a session row, and the prompt draft that went with
// it. Killing the tmux session behind it is the caller's business: the store
// knows nothing about terminals.
//
// The two deletes are one transaction, so a draft can never outlive its row
// and come back from the bucket as an object that belongs to nothing.
func (s *Store) DeleteSession(id string) error {
	s.lock()
	defer s.unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM drafts WHERE session_id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.bump()
	s.touch(SessionObject(id), DraftObject(id))
	return nil
}
