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
	// Archived is a session that has been put away: it is hidden from the list
	// until the list is switched to showing everything. The timestamp doubles
	// as the flag, and the client only ever needs the boolean.
	Archived   bool  `json:"archived"`
	ArchivedAt int64 `json:"-"`
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
                     created_at, updated_at, last_attached, archived_at`

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	s := &Session{}
	var options string
	var resumed int
	err := row.Scan(&s.ID, &s.ClientID, &s.Title, &s.TitleSource, &s.Harness, &s.Model, &s.Effort,
		&s.Workdir, &s.WorkdirMode, &options, &s.TmuxName, &s.CLISessionID, &s.CLISessionState,
		&s.State, &s.ExitStatus, &s.FailReason, &resumed, &s.ResumeCount, &s.Cols, &s.Rows,
		&s.CreatedAt, &s.UpdatedAt, &s.LastAttached, &s.ArchivedAt)
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
	s.Archived = s.ArchivedAt > 0
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.CreatedAt == 0 {
		sess.CreatedAt = now()
	}
	sess.UpdatedAt = sess.CreatedAt
	// Nothing has been attached to yet and nothing has been resumed, whatever
	// the caller's struct says: these three are the row's to set, and the
	// caller has to be left holding what was actually stored.
	sess.Archived, sess.ArchivedAt = false, 0
	sess.Resumed, sess.LastAttached = false, 0
	if sess.State == "" {
		sess.State = StateStarting
	}
	if sess.CLISessionState == "" {
		sess.CLISessionState = CLINone
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
	_, err := s.db.Exec(`INSERT INTO sessions(`+sessionCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.ClientID, sess.Title, sess.TitleSource, sess.Harness, sess.Model, sess.Effort,
		sess.Workdir, sess.WorkdirMode, options, sess.TmuxName, sess.CLISessionID,
		sess.CLISessionState, sess.State, sess.ExitStatus, sess.FailReason, 0,
		sess.ResumeCount, sess.Cols, sess.Rows, sess.CreatedAt, sess.UpdatedAt, 0, 0)
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

// ListSessions returns sessions, newest activity first. Archived ones are left
// out unless they are asked for, which is what the list's own switch decides.
func (s *Store) ListSessions(includeArchived bool) ([]Session, error) {
	where := ` WHERE archived_at = 0`
	if includeArchived {
		where = ``
	}
	rows, err := s.db.Query(`SELECT ` + sessionCols + ` FROM sessions` + where + ` ORDER BY updated_at DESC`)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	args = append(args, now(), id)
	res, err := s.db.Exec(`UPDATE sessions SET `+query+`, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	args = append(args, now(), id)
	res, err := s.db.Exec(`UPDATE sessions SET `+query+`, updated_at = ? WHERE id = ? AND title_source = ''`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	return nil
}

// SetSessionState records where a session is in its life. The exit status and
// the reason travel with it, because they are only ever meaningful together
// with the state that explains them.
func (s *Store) SetSessionState(id, state string, exitStatus int, failReason string) error {
	return s.update(id, `state = ?, exit_status = ?, fail_reason = ?`, state, exitStatus, failReason)
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

// SetSessionArchived puts a session away or brings it back. A restored session
// is indistinguishable from one that was never archived.
func (s *Store) SetSessionArchived(id string, archived bool) error {
	ts := int64(0)
	if archived {
		ts = now()
	}
	return s.update(id, `archived_at = ?`, ts)
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

// DeleteSession removes a session row. Killing the tmux session behind it is
// the caller's business: the store knows nothing about terminals.
func (s *Store) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	return nil
}
