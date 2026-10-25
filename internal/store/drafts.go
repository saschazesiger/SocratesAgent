package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// A chat's draft is the next message being written in its composer - for a
// chat nobody has written to yet, its first. It lives here, beside the
// session, so that it survives a closed tab, a lost phone and a restart, and
// is the same text on every device.
//
// The rest of this file is what is left of prepared sessions (PreStart),
// which are gone: the clean cut of an old data folder still meets prepared
// terminals (MakePreparedChat), and a data folder from before PreStart was
// removed holds prepared chats that become ordinary chats nobody has written
// to yet (PreparedChats, ReadyPreparedChat).

// ErrNotChat is the refusal of a draft for a session that is not a chat: a
// Shell's text is typed into its terminal, not written ahead.
var ErrNotChat = errors.New("only a chat has a draft")

// ErrNotPrestart is a prepared session that is not prepared any more, which
// the migrations that turn one into a chat find when a run cut short has
// already done it.
var ErrNotPrestart = errors.New("the session is not a prepared one")

// ErrConflict is a draft save that was based on a revision that is no longer
// the current one: another device saved in between. The current draft comes
// back with it, so the person can choose.
var ErrConflict = errors.New("the draft was changed somewhere else")

// Draft is the text a chat's composer holds.
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
// is the ordinary state of a chat nobody has typed into yet.
func (s *Store) GetDraft(id string) (Draft, error) {
	var d Draft
	err := s.db.QueryRow(`SELECT text, rev, updated_at FROM drafts WHERE session_id = ?`, id).
		Scan(&d.Text, &d.Rev, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	return d, err
}

// SaveDraft stores the text of a chat's composer, if baseRev is the revision
// it was edited from, and returns what was stored.
//
// A save from an older revision is refused with ErrConflict and the current
// draft, rather than merged or silently overwritten: two devices typing into
// one message is rare, and when it happens the person is the only one who
// knows which text they meant. A terminal refuses with ErrNotChat.
//
// The revision of the store moves too, which is how other devices learn from
// the session list that the draft changed.
func (s *Store) SaveDraft(id, text string, baseRev int64) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback()
	var kind string
	err = tx.QueryRow(`SELECT kind FROM sessions WHERE id = ?`, id).Scan(&kind)
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
	if kind != KindChat {
		return current, ErrNotChat
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

// MakePreparedChat turns a prepared terminal into a prepared chat with these
// options: the clean cut of REBUILD.md §8.3, for the agents' terminals that
// were prepared and never started. Everything else the person set - the
// agent, the folder, the model, the schedule and the first prompt in the
// draft - stays. It answers ErrNotPrestart for a row that is not a prepared
// terminal any more, which a run cut short after the change finds.
func (s *Store) MakePreparedChat(id string, options json.RawMessage) error {
	value := "{}"
	if len(options) > 0 {
		if !json.Valid(options) {
			return errors.New("session options are not valid JSON")
		}
		value = string(options)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE sessions SET kind = ?, options = ?, updated_at = ? WHERE id = ? AND state = ? AND kind = ?`,
		KindChat, value, now(), id, StatePrestart, KindTerminal)
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
	return nil
}

// PreparedChats is every prepared chat: rows of a build that still had
// PreStart, which the server turns into ordinary chats as it starts
// (ReadyPreparedChat).
func (s *Store) PreparedChats() ([]Session, error) {
	rows, err := s.ListSessions()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, row := range rows {
		if row.State == StatePrestart && row.Kind == KindChat {
			out = append(out, row)
		}
	}
	return out, nil
}

// ReadyPreparedChat makes a prepared chat an ordinary chat nobody has
// written to yet: running, as a chat made from the sheet is, in the folder
// it was resolved to, with its schedule gone. Its draft stays where it is,
// and is the composer's first message now. The title follows the placeholder
// rule: a name somebody gave is kept. ErrNotPrestart is a row somebody else
// already made ready.
func (s *Store) ReadyPreparedChat(id, workdir, model, effort, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE sessions SET state = ?, workdir = ?, model = ?, effort = ?, start_at = 0,
		fail_reason = '', exit_status = -1, title = CASE WHEN ? <> '' AND title_source = '' THEN ? ELSE title END,
		updated_at = ? WHERE id = ? AND state = ? AND kind = ?`,
		StateRunning, workdir, model, effort, title, title, now(), id, StatePrestart, KindChat)
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
	return nil
}
