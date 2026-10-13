package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// A prepared session - StatePrestart - is a row with settings and a prompt
// draft and nothing else. This file is the store's side of it: the draft, the
// settings that may still change, and the two transitions in and out of
// "starting" that Start is built from.
//
// Every write here is conditional on the row still being prepared. The checks
// are in the WHERE clauses rather than in a read before the write because two
// devices and a scheduler can all be acting on one prepared session at once,
// and a check that is not the write itself is a check somebody can slip past.

// ErrNotPrestart is the refusal to touch the settings or the draft of a
// session that has been started: from then on they describe a running
// program, and changing them would describe nothing.
var ErrNotPrestart = errors.New("the session has already been started")

// ErrConflict is a draft save that was based on a revision that is no longer
// the current one: another device saved in between. The current draft comes
// back with it, so the person can choose.
var ErrConflict = errors.New("the draft was changed somewhere else")

// Draft is the prompt a prepared session will be started with.
type Draft struct {
	Text string `json:"text"`
	// Rev counts saves, starting at one. A save names the revision it was
	// based on, and only a save based on the current one is taken.
	Rev       int64 `json:"rev"`
	UpdatedAt int64 `json:"updated_at"`
}

// DraftMeta is what the session list says about a draft: enough for another
// device to notice it changed and to show its first line, and not the text,
// which can be a hundred kilobytes per session.
type DraftMeta struct {
	Rev       int64
	Preview   string
	UpdatedAt int64
}

// previewRunes is how much of a draft's first line the list carries.
const previewRunes = 120

// GetDraft returns a session's draft, or ErrNotFound when it has none - which
// is the ordinary state of a prepared session nobody has typed into yet.
func (s *Store) GetDraft(id string) (Draft, error) {
	var d Draft
	err := s.db.QueryRow(`SELECT text, rev, updated_at FROM drafts WHERE session_id = ?`, id).
		Scan(&d.Text, &d.Rev, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	return d, err
}

// SaveDraft stores the text of a prepared session's prompt if baseRev is the
// revision it was edited from, and returns what was stored.
//
// A save from an older revision is refused with ErrConflict and the current
// draft, rather than merged or silently overwritten: two devices typing into
// one prompt is rare, and when it happens the person is the only one who knows
// which text they meant. A session that has been started refuses with
// ErrNotPrestart - its prompt was sent, and editing it afterwards would be
// editing history.
//
// The revision of the store moves too, which is how other devices learn from
// the session list that the draft changed.
func (s *Store) SaveDraft(id, text string, baseRev int64) (Draft, error) {
	s.lock()
	defer s.unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRow(`SELECT state FROM sessions WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	var current Draft
	err = tx.QueryRow(`SELECT text, rev, updated_at FROM drafts WHERE session_id = ?`, id).
		Scan(&current.Text, &current.Rev, &current.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Draft{}, err
	}
	if state != StatePrestart {
		return current, ErrNotPrestart
	}
	if baseRev != current.Rev {
		return current, ErrConflict
	}
	next := Draft{Text: text, Rev: current.Rev + 1, UpdatedAt: now()}
	if _, err := tx.Exec(`INSERT INTO drafts(session_id, text, rev, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET text = excluded.text, rev = excluded.rev,
		updated_at = excluded.updated_at`, id, next.Text, next.Rev, next.UpdatedAt); err != nil {
		return Draft{}, err
	}
	if err := tx.Commit(); err != nil {
		return Draft{}, err
	}
	s.bump()
	s.touch(DraftObject(id))
	return next, nil
}

// DraftMetas returns the metadata of every draft, by session id, for the
// session list. Only the head of each text is read: the preview is one line,
// and the list is polled.
func (s *Store) DraftMetas() (map[string]DraftMeta, error) {
	rows, err := s.db.Query(`SELECT session_id, rev, updated_at, substr(text, 1, 600) FROM drafts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DraftMeta{}
	for rows.Next() {
		var id, head string
		var m DraftMeta
		if err := rows.Scan(&id, &m.Rev, &m.UpdatedAt, &head); err != nil {
			return nil, err
		}
		m.Preview = draftPreview(head)
		out[id] = m
	}
	return out, rows.Err()
}

// DraftMetaOf is DraftMetas for one session; ok is false when it has no draft.
func (s *Store) DraftMetaOf(id string) (DraftMeta, bool) {
	var m DraftMeta
	var head string
	err := s.db.QueryRow(`SELECT rev, updated_at, substr(text, 1, 600) FROM drafts WHERE session_id = ?`, id).
		Scan(&m.Rev, &m.UpdatedAt, &head)
	if err != nil {
		return DraftMeta{}, false
	}
	m.Preview = draftPreview(head)
	return m, true
}

// draftPreview is the first line of a draft that has any text on it, cut to
// previewRunes.
func draftPreview(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > previewRunes {
			runes := []rune(line)
			line = string(runes[:previewRunes]) + "…"
		}
		return line
	}
	return ""
}

// PrestartSettings is everything about a prepared session that may change
// before it starts. A rename is not here: a rename is a rename whatever the
// state.
type PrestartSettings struct {
	// Title is the placeholder name for the new settings ("Codex · repo"),
	// written only while the row has no name somebody gave it - the same
	// rule the automatic title follows. Empty keeps the title.
	Title       string
	Harness     string
	Model       string
	Effort      string
	WorkdirMode string
	// Workdir is the chosen path for a preset or custom directory, and empty
	// for a dynamic one, which is only made when the session starts.
	Workdir string
	Options json.RawMessage
	StartAt int64
}

// UpdatePrestart replaces the settings of a prepared session. A fail reason
// left by a start that did not work is cleared with it: it described the
// settings that are being replaced.
func (s *Store) UpdatePrestart(id string, p PrestartSettings) error {
	options := "{}"
	if len(p.Options) > 0 {
		if !json.Valid(p.Options) {
			return errors.New("session options are not valid JSON")
		}
		options = string(p.Options)
	}
	if p.StartAt < 0 {
		p.StartAt = 0
	}
	// The title condition is in the write, not in a read before it: a rename
	// from another device that lands first must win over a placeholder.
	return s.updatePrestart(id, `harness = ?, model = ?, effort = ?, workdir_mode = ?, workdir = ?,
		options = ?, start_at = ?, fail_reason = '',
		title = CASE WHEN ? <> '' AND title_source = '' THEN ? ELSE title END`,
		p.Harness, p.Model, p.Effort, p.WorkdirMode, p.Workdir, options, p.StartAt, p.Title, p.Title)
}

// SetPrestartFailure records why a start did not happen, on a row that is
// still prepared. The row stays prepared: nothing ran, so there is nothing to
// restart, and the person fixes the setting and presses Start again.
func (s *Store) SetPrestartFailure(id, reason string) error {
	return s.updatePrestart(id, `fail_reason = ?`, reason)
}

// ClaimScheduledStart takes a due schedule for the scheduler: it clears
// start_at if, and only if, the row is still prepared and still scheduled for
// exactly `at`. True means this caller owns the start. Clearing it before the
// start is attempted is what keeps a start that fails from being tried again
// every ten seconds for ever, and the exact match is what keeps a schedule
// somebody moved in the meantime from being taken for the old one.
func (s *Store) ClaimScheduledStart(id string, at int64) (bool, error) {
	s.lock()
	defer s.unlock()
	res, err := s.db.Exec(`UPDATE sessions SET start_at = 0, updated_at = ?
		WHERE id = ? AND state = ? AND start_at = ? AND start_at > 0`, now(), id, StatePrestart, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	s.bump()
	s.touch(SessionObject(id))
	return true, nil
}

// BeginStart is the transition out of prepared: the row takes on everything
// the launch resolved - the directory that now exists, the options it runs
// with, its tmux name and conversation id - and becomes StateStarting, from
// where it is exactly a session being created. It refuses with ErrNotPrestart
// when somebody else started it first, which is what makes Start safe to call
// twice.
//
// Its place in the list is now, because starting is the most that can happen
// to a session.
func (s *Store) BeginStart(row *Session) error {
	options := "{}"
	if len(row.Options) > 0 {
		if !json.Valid(row.Options) {
			return errors.New("session options are not valid JSON")
		}
		options = string(row.Options)
	}
	if row.CLISessionState == "" {
		row.CLISessionState = CLINone
	}
	if row.Cols <= 0 {
		row.Cols = DefaultCols
	}
	if row.Rows <= 0 {
		row.Rows = DefaultRows
	}
	at := now()
	// The title goes with the rest: a nameless row is listed under the name
	// of what it runs, which is only known now (the harness and directory
	// may have changed since it was prepared).
	err := s.updatePrestart(row.ID, `title = CASE WHEN title_source = '' THEN ? ELSE title END, harness = ?, model = ?, effort = ?, workdir = ?, workdir_mode = ?,
		options = ?, tmux_name = ?, cli_session_id = ?, cli_session_state = ?, state = ?, exit_status = -1,
		fail_reason = '', start_at = 0, cols = ?, rows = ?, active_at = ?`,
		row.Title, row.Harness, row.Model, row.Effort, row.Workdir, row.WorkdirMode, options, row.TmuxName,
		row.CLISessionID, row.CLISessionState, StateStarting, row.Cols, row.Rows, at)
	if err != nil {
		return err
	}
	row.State, row.ExitStatus, row.FailReason, row.StartAt, row.ActiveAt = StateStarting, -1, "", 0, at
	row.Options = json.RawMessage(options)
	return nil
}

// RevertStart puts a row that BeginStart moved back where it was, for a start
// that failed before the program ever ran: the settings it was prepared with,
// no tmux name, and the reason. The schedule stays cleared - a start that did
// not work is not retried by itself - and the draft was never touched.
//
// It also takes the row back from needs_resume: the poll may have seen it
// "starting" with no tmux server yet (after a reboot, the launch is what
// starts one) and declared it lost in the middle of the launch. Only the
// launch knows that nothing ran, and a prepared session must not become one
// to resume - that path never sends the prompt.
func (s *Store) RevertStart(prepared *Session, reason string) error {
	options := "{}"
	if len(prepared.Options) > 0 {
		options = string(prepared.Options)
	}
	s.lock()
	defer s.unlock()
	res, err := s.db.Exec(`UPDATE sessions SET title = CASE WHEN title_source = '' THEN ? ELSE title END, harness = ?, model = ?, effort = ?, workdir = ?, workdir_mode = ?,
		options = ?, tmux_name = '', cli_session_id = '', cli_session_state = ?, state = ?, exit_status = -1,
		fail_reason = ?, start_at = 0, cols = ?, rows = ?, updated_at = ?
		WHERE id = ? AND state IN (?, ?)`,
		prepared.Title, prepared.Harness, prepared.Model, prepared.Effort, prepared.Workdir, prepared.WorkdirMode, options,
		CLINone, StatePrestart, reason, prepared.Cols, prepared.Rows, now(), prepared.ID, StateStarting, StateNeedsResume)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	s.bump()
	s.touch(SessionObject(prepared.ID))
	return nil
}

// updatePrestart is `update` for a row that has to still be prepared: it
// answers ErrNotFound for a row that is not there and ErrNotPrestart for one
// that has been started.
func (s *Store) updatePrestart(id, query string, args ...any) error {
	s.lock()
	defer s.unlock()
	args = append(args, now(), id, StatePrestart)
	res, err := s.db.Exec(`UPDATE sessions SET `+query+`, updated_at = ? WHERE id = ? AND state = ?`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var state string
		if err := s.db.QueryRow(`SELECT state FROM sessions WHERE id = ?`, id).Scan(&state); err != nil {
			return ErrNotFound
		}
		return ErrNotPrestart
	}
	s.bump()
	s.touch(SessionObject(id))
	return nil
}
