// Package store is the persistence layer. Everything the web UI shows -
// chats, messages, the live process view and pending questions - lives in a
// single SQLite file so that a browser refresh (or a server restart) restores
// the exact state.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database handle.
type Store struct {
	db  *sql.DB
	rev atomic.Int64
	mu  sync.Mutex // serialises writes; SQLite has a single writer anyway
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chats (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  workspace  TEXT NOT NULL DEFAULT '',
  client_id  TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id         TEXT PRIMARY KEY,
  chat_id    TEXT NOT NULL,
  run_id     TEXT NOT NULL DEFAULT '',
  role       TEXT NOT NULL,
  content    TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  rev        INTEGER NOT NULL DEFAULT 0,
  client_id  TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id, seq);
CREATE TABLE IF NOT EXISTS llm_messages (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id TEXT NOT NULL,
  payload TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_llm_chat ON llm_messages(chat_id, id);
CREATE TABLE IF NOT EXISTS runs (
  id         TEXT PRIMARY KEY,
  chat_id    TEXT NOT NULL,
  status     TEXT NOT NULL,
  error      TEXT NOT NULL DEFAULT '',
  auto       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_chat ON runs(chat_id, created_at);
CREATE TABLE IF NOT EXISTS steps (
  id         TEXT PRIMARY KEY,
  run_id     TEXT NOT NULL,
  chat_id    TEXT NOT NULL,
  parent_id  TEXT NOT NULL DEFAULT '',
  seq        INTEGER NOT NULL,
  rev        INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'running',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_steps_run ON steps(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_steps_chat_rev ON steps(chat_id, rev);
CREATE TABLE IF NOT EXISTS questions (
  id          TEXT PRIMARY KEY,
  chat_id     TEXT NOT NULL,
  run_id      TEXT NOT NULL,
  step_id     TEXT NOT NULL,
  kind        TEXT NOT NULL,
  question    TEXT NOT NULL,
  options     TEXT NOT NULL DEFAULT '[]',
  status      TEXT NOT NULL,
  answer      TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  answered_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_questions_chat ON questions(chat_id, created_at);
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
`

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection keeps write ordering trivial and is plenty for the
	// single user workload this app is built for.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	// The revision counter has to start above everything already written, or a
	// reconnecting browser would be told that old rows are new.
	var maxRev sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(r) FROM (SELECT MAX(rev) AS r FROM steps UNION ALL SELECT MAX(rev) FROM messages)`).
		Scan(&maxRev); err == nil && maxRev.Valid {
		s.rev.Store(maxRev.Int64)
	}
	return s, nil
}

// migrate adds what came after the first release. CREATE TABLE IF NOT EXISTS
// leaves an existing table alone, so every column added later has to be
// applied to databases that are already out there.
func migrate(db *sql.DB) error {
	for _, add := range []struct{ table, column, definition string }{
		{"chats", "client_id", "TEXT NOT NULL DEFAULT ''"},
		{"messages", "rev", "INTEGER NOT NULL DEFAULT 0"},
		{"messages", "client_id", "TEXT NOT NULL DEFAULT ''"},
		{"questions", "source", "TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := hasColumn(db, add.table, add.column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + add.table + " ADD COLUMN " + add.column + " " + add.definition); err != nil {
			return err
		}
	}
	// The unique indexes are what make a retried request idempotent: the same
	// client id can only ever produce one chat and one message.
	_, err := db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_chats_client ON chats(client_id) WHERE client_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_client ON messages(chat_id, client_id) WHERE client_id <> '';
CREATE INDEX IF NOT EXISTS idx_messages_rev ON messages(chat_id, rev);
`)
	return err
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// NextRev hands out the next global revision number. Every step write carries
// one so that a reconnecting client can ask for "everything newer than X".
func (s *Store) NextRev() int64 { return s.rev.Add(1) }

// Rev returns the current revision.
func (s *Store) Rev() int64 { return s.rev.Load() }

func now() int64 { return time.Now().UnixMilli() }

// ---------------------------------------------------------------- key/value

// GetKV reads a raw value.
func (s *Store) GetKV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetKV writes a raw value.
func (s *Store) SetKV(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetJSON decodes a JSON value stored under key.
func (s *Store) GetJSON(key string, out any) error {
	v, err := s.GetKV(key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(v), out)
}

// SetJSON encodes and stores a JSON value.
func (s *Store) SetJSON(key string, in any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return s.SetKV(key, string(b))
}

// ------------------------------------------------------------------- chats

// Chat is a conversation.
type Chat struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Workspace string `json:"workspace"`
	// ClientID is the key the browser generated for the request that created
	// this chat. It is what makes creating a chat safe to retry.
	ClientID  string `json:"client_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const chatCols = `id, title, workspace, client_id, created_at, updated_at`

func scanChat(row interface{ Scan(...any) error }) (*Chat, error) {
	c := &Chat{}
	err := row.Scan(&c.ID, &c.Title, &c.Workspace, &c.ClientID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// CreateChat inserts a new chat.
func (s *Store) CreateChat(c *Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	c.UpdatedAt = c.CreatedAt
	_, err := s.db.Exec(`INSERT INTO chats(id, title, workspace, client_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)`, c.ID, c.Title, c.Workspace, c.ClientID, c.CreatedAt, c.UpdatedAt)
	return err
}

// GetChat loads one chat.
func (s *Store) GetChat(id string) (*Chat, error) {
	return scanChat(s.db.QueryRow(`SELECT `+chatCols+` FROM chats WHERE id = ?`, id))
}

// ChatByClientID finds the chat a browser already created under this key, so
// that a request repeated over a flaky connection does not create a second one.
func (s *Store) ChatByClientID(clientID string) (*Chat, error) {
	if clientID == "" {
		return nil, ErrNotFound
	}
	return scanChat(s.db.QueryRow(`SELECT `+chatCols+` FROM chats WHERE client_id = ?`, clientID))
}

// ListChats returns all chats, newest activity first.
func (s *Store) ListChats() ([]Chat, error) {
	rows, err := s.db.Query(`SELECT ` + chatCols + ` FROM chats ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Title, &c.Workspace, &c.ClientID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateChat writes title and workspace.
func (s *Store) UpdateChat(id, title, workspace string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET title = ?, workspace = ?, updated_at = ? WHERE id = ?`,
		title, workspace, now(), id)
	return err
}

// TouchChat bumps the updated_at timestamp used for sidebar ordering.
func (s *Store) TouchChat(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET updated_at = ? WHERE id = ?`, now(), id)
	return err
}

// DeleteChat removes a chat and everything attached to it.
func (s *Store) DeleteChat(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM messages WHERE chat_id = ?`,
		`DELETE FROM llm_messages WHERE chat_id = ?`,
		`DELETE FROM steps WHERE chat_id = ?`,
		`DELETE FROM questions WHERE chat_id = ?`,
		`DELETE FROM runs WHERE chat_id = ?`,
		`DELETE FROM chats WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- messages

// Message is one visible chat bubble.
type Message struct {
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	RunID   string `json:"run_id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Seq     int64  `json:"seq"`
	// Rev shares the counter with steps, so one revision number is enough for
	// a reconnecting browser to ask for everything it missed.
	Rev int64 `json:"rev"`
	// ClientID is the key the browser generated for the send. Repeating the
	// request with the same key returns the message that already exists.
	ClientID  string `json:"client_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

const messageCols = `id, chat_id, run_id, role, content, seq, rev, client_id, created_at`

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ChatID, &m.RunID, &m.Role, &m.Content, &m.Seq, &m.Rev,
			&m.ClientID, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMessage appends a visible message.
func (s *Store) AddMessage(m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.CreatedAt == 0 {
		m.CreatedAt = now()
	}
	if m.Seq == 0 {
		var maxSeq sql.NullInt64
		if err := s.db.QueryRow(`SELECT MAX(seq) FROM messages WHERE chat_id = ?`, m.ChatID).Scan(&maxSeq); err != nil {
			return err
		}
		m.Seq = maxSeq.Int64 + 1
	}
	m.Rev = s.rev.Add(1)
	_, err := s.db.Exec(`INSERT INTO messages(`+messageCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.ChatID, m.RunID, m.Role, m.Content, m.Seq, m.Rev,
		m.ClientID, m.CreatedAt)
	return err
}

// ListMessages returns the visible transcript of a chat.
func (s *Store) ListMessages(chatID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE chat_id = ? ORDER BY seq`, chatID)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// MessagesSince returns the messages of a chat written after a revision. It is
// the message half of what a reconnecting client replays.
func (s *Store) MessagesSince(chatID string, rev int64) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE chat_id = ? AND rev > ? ORDER BY seq`, chatID, rev)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// MessageByClientID finds the message a browser already sent under this key.
func (s *Store) MessageByClientID(chatID, clientID string) (*Message, error) {
	if clientID == "" {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE chat_id = ? AND client_id = ?`, chatID, clientID)
	if err != nil {
		return nil, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, ErrNotFound
	}
	return &msgs[0], nil
}

// ------------------------------------------------------------ llm messages

// AppendLLMMessage stores one raw provider message (the exact JSON that is sent
// back to the model), so a chat can be continued with full tool call context.
func (s *Store) AppendLLMMessage(chatID string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO llm_messages(chat_id, payload) VALUES(?, ?)`, chatID, string(payload))
	return err
}

// LLMMessages returns the raw provider messages of a chat in order.
func (s *Store) LLMMessages(chatID string) ([]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT payload FROM llm_messages WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(p))
	}
	return out, rows.Err()
}

// -------------------------------------------------------------------- runs

// Run states.
const (
	RunRunning     = "running"
	RunWaiting     = "waiting_input"
	RunDone        = "done"
	RunFailed      = "failed"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted"
)

// Run is one turn of the orchestrator.
type Run struct {
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Auto      bool   `json:"auto"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CreateRun inserts a run.
func (s *Store) CreateRun(r *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.CreatedAt == 0 {
		r.CreatedAt = now()
	}
	r.UpdatedAt = r.CreatedAt
	auto := 0
	if r.Auto {
		auto = 1
	}
	_, err := s.db.Exec(`INSERT INTO runs(id, chat_id, status, error, auto, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, r.ID, r.ChatID, r.Status, r.Error, auto, r.CreatedAt, r.UpdatedAt)
	return err
}

// SetRunStatus updates status and error of a run.
func (s *Store) SetRunStatus(id, status, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE runs SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, now(), id)
	return err
}

// GetRun loads a run.
func (s *Store) GetRun(id string) (*Run, error) {
	r := &Run{}
	var auto int
	err := s.db.QueryRow(`SELECT id, chat_id, status, error, auto, created_at, updated_at FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.ChatID, &r.Status, &r.Error, &auto, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.Auto = auto == 1
	return r, err
}

// ListRuns returns all runs of a chat, oldest first.
func (s *Store) ListRuns(chatID string) ([]Run, error) {
	rows, err := s.db.Query(`SELECT id, chat_id, status, error, auto, created_at, updated_at
		FROM runs WHERE chat_id = ? ORDER BY created_at`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var auto int
		if err := rows.Scan(&r.ID, &r.ChatID, &r.Status, &r.Error, &auto, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Auto = auto == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRun returns the run of a chat that is still in flight, if any.
func (s *Store) ActiveRun(chatID string) (*Run, error) {
	r := &Run{}
	var auto int
	err := s.db.QueryRow(`SELECT id, chat_id, status, error, auto, created_at, updated_at FROM runs
		WHERE chat_id = ? AND status IN ('running','waiting_input') ORDER BY created_at DESC LIMIT 1`, chatID).
		Scan(&r.ID, &r.ChatID, &r.Status, &r.Error, &auto, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.Auto = auto == 1
	return r, err
}

// RecoverRuns marks runs that were in flight when the process died. Called once
// at startup so the UI never shows a spinner that will never finish.
func (s *Store) RecoverRuns() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now()
	if _, err := s.db.Exec(`UPDATE runs SET status = 'interrupted', error = 'Server restarted while this run was in progress.', updated_at = ?
		WHERE status IN ('running','waiting_input')`, ts); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE steps SET status = 'interrupted', updated_at = ? WHERE status = 'running'`, ts); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE questions SET status = 'cancelled', answered_at = ? WHERE status = 'pending'`, ts)
	return err
}

// ------------------------------------------------------------------- steps

// Step kinds shown in the process view.
const (
	StepThinking = "thinking"
	StepText     = "text"
	StepTerminal = "terminal"
	StepShell    = "shell"
	StepQuestion = "question"
	StepError    = "error"
)

// Step statuses.
const (
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusPending     = "pending"
	StatusAnswered    = "answered"
	StatusCancelled   = "cancelled"
	StatusInterrupted = "interrupted"
)

// Step is one entry of the live process view.
type Step struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id"`
	ChatID    string          `json:"chat_id"`
	ParentID  string          `json:"parent_id"`
	Seq       int64           `json:"seq"`
	Rev       int64           `json:"rev"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	Status    string          `json:"status"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

// PutStep inserts or updates a step and assigns it a fresh revision.
func (s *Store) PutStep(st *Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now()
	if st.CreatedAt == 0 {
		st.CreatedAt = ts
	}
	st.UpdatedAt = ts
	st.Rev = s.rev.Add(1)
	detail := ""
	if len(st.Detail) > 0 {
		detail = string(st.Detail)
	}
	_, err := s.db.Exec(`INSERT INTO steps(id, run_id, chat_id, parent_id, seq, rev, kind, title, body, detail, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET rev = excluded.rev, title = excluded.title, body = excluded.body,
			detail = excluded.detail, status = excluded.status, updated_at = excluded.updated_at,
			parent_id = excluded.parent_id`,
		st.ID, st.RunID, st.ChatID, st.ParentID, st.Seq, st.Rev, st.Kind, st.Title, st.Body, detail,
		st.Status, st.CreatedAt, st.UpdatedAt)
	return err
}

// DeleteStep removes a step. It is used when a streamed answer graduates into
// a real chat message and should no longer appear in the process view.
func (s *Store) DeleteStep(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM steps WHERE id = ?`, id)
	return err
}

// NextStepSeq returns the next ordering number inside a run.
func (s *Store) NextStepSeq(runID string) (int64, error) {
	var maxSeq sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM steps WHERE run_id = ?`, runID).Scan(&maxSeq); err != nil {
		return 0, err
	}
	return maxSeq.Int64 + 1, nil
}

func scanSteps(rows *sql.Rows) ([]Step, error) {
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var st Step
		var detail string
		if err := rows.Scan(&st.ID, &st.RunID, &st.ChatID, &st.ParentID, &st.Seq, &st.Rev, &st.Kind,
			&st.Title, &st.Body, &detail, &st.Status, &st.CreatedAt, &st.UpdatedAt); err != nil {
			return nil, err
		}
		if detail != "" {
			st.Detail = json.RawMessage(detail)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

const stepCols = `id, run_id, chat_id, parent_id, seq, rev, kind, title, body, detail, status, created_at, updated_at`

// GetStep loads one step by id.
func (s *Store) GetStep(id string) (*Step, error) {
	rows, err := s.db.Query(`SELECT id, run_id, chat_id, parent_id, seq, rev, kind, title, body,
		detail, status, created_at, updated_at FROM steps WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps, err := scanSteps(rows)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, ErrNotFound
	}
	return &steps[0], nil
}

// ListSteps returns every step of a chat in display order.
func (s *Store) ListSteps(chatID string) ([]Step, error) {
	rows, err := s.db.Query(`SELECT `+stepCols+` FROM steps WHERE chat_id = ? ORDER BY seq, created_at`, chatID)
	if err != nil {
		return nil, err
	}
	return scanSteps(rows)
}

// StepsSince returns steps of a chat that changed after the given revision.
// This is what makes a reconnecting SSE client catch up without a full reload.
func (s *Store) StepsSince(chatID string, rev int64) ([]Step, error) {
	rows, err := s.db.Query(`SELECT `+stepCols+` FROM steps WHERE chat_id = ? AND rev > ? ORDER BY rev`, chatID, rev)
	if err != nil {
		return nil, err
	}
	return scanSteps(rows)
}

// StepIDs lists every step a chat still has. A deletion is not a revision, so
// this is how a client that was away learns which rows went away while it was
// gone instead of showing them forever.
func (s *Store) StepIDs(chatID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM steps WHERE chat_id = ?`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --------------------------------------------------------------- questions

// Question is an interactive prompt that blocks a run: either the orchestrator
// asking the user something.
type Question struct {
	ID       string   `json:"id"`
	ChatID   string   `json:"chat_id"`
	RunID    string   `json:"run_id"`
	StepID   string   `json:"step_id"`
	Kind     string   `json:"kind"` // ask | permission
	Question string   `json:"question"`
	Options  []Option `json:"options"`
	Status   string   `json:"status"`
	Answer   string   `json:"answer"`
	// Source names who is really asking, so the browser can say "Claude Code
	// asks…" instead of putting every question in Socrates' own voice. Empty
	// means Socrates itself.
	Source     string `json:"source,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	AnsweredAt int64  `json:"answered_at"`
}

// Option is one selectable answer.
type Option struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// CreateQuestion stores a pending question.
func (s *Store) CreateQuestion(q *Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.CreatedAt == 0 {
		q.CreatedAt = now()
	}
	opts, err := json.Marshal(q.Options)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO questions(id, chat_id, run_id, step_id, kind, question, options, status, answer, source, created_at, answered_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.ID, q.ChatID, q.RunID, q.StepID, q.Kind, q.Question, string(opts), q.Status, q.Answer, q.Source, q.CreatedAt, q.AnsweredAt)
	return err
}

// GetQuestion loads a question.
func (s *Store) GetQuestion(id string) (*Question, error) {
	q := &Question{}
	var opts string
	err := s.db.QueryRow(`SELECT id, chat_id, run_id, step_id, kind, question, options, status, answer, source, created_at, answered_at
		FROM questions WHERE id = ?`, id).
		Scan(&q.ID, &q.ChatID, &q.RunID, &q.StepID, &q.Kind, &q.Question, &opts, &q.Status, &q.Answer, &q.Source, &q.CreatedAt, &q.AnsweredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(opts), &q.Options)
	return q, nil
}

// AnswerQuestion records the user's choice.
func (s *Store) AnswerQuestion(id, answer, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE questions SET answer = ?, status = ?, answered_at = ? WHERE id = ?`,
		answer, status, now(), id)
	return err
}

// PendingQuestion returns the open question of a chat, if any.
func (s *Store) PendingQuestion(chatID string) (*Question, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM questions WHERE chat_id = ? AND status = 'pending' ORDER BY created_at DESC LIMIT 1`, chatID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetQuestion(id)
}

// ListQuestions returns all questions of a chat.
func (s *Store) ListQuestions(chatID string) ([]Question, error) {
	rows, err := s.db.Query(`SELECT id, chat_id, run_id, step_id, kind, question, options, status, answer, source, created_at, answered_at
		FROM questions WHERE chat_id = ? ORDER BY created_at`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		var q Question
		var opts string
		if err := rows.Scan(&q.ID, &q.ChatID, &q.RunID, &q.StepID, &q.Kind, &q.Question, &opts,
			&q.Status, &q.Answer, &q.Source, &q.CreatedAt, &q.AnsweredAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(opts), &q.Options)
		out = append(out, q)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- sessions

// CreateSession stores a login session token.
func (s *Store) CreateSession(token string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO sessions(token, created_at, expires_at) VALUES(?, ?, ?)`,
		token, ts, ts+ttl.Milliseconds())
	return err
}

// ValidSession reports whether a token is known and not expired.
func (s *Store) ValidSession(token string) bool {
	if token == "" {
		return false
	}
	var exp int64
	if err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, token).Scan(&exp); err != nil {
		return false
	}
	return exp > now()
}

// DeleteSession logs a token out.
func (s *Store) DeleteSession(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteAllSessions invalidates every login (used when the password changes).
func (s *Store) DeleteAllSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions`)
	return err
}

// PurgeExpiredSessions removes stale rows.
func (s *Store) PurgeExpiredSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now())
	return err
}
