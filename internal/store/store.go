// Package store is the persistence layer. Everything the web UI shows -
// chats, messages and the live process view - lives in a single SQLite file
// so that a browser refresh (or a server restart) restores the exact state.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
  id            TEXT PRIMARY KEY,
  title         TEXT NOT NULL DEFAULT '',
  workspace     TEXT NOT NULL DEFAULT '',
  client_id     TEXT NOT NULL DEFAULT '',
  agent         TEXT NOT NULL DEFAULT '',
  model         TEXT NOT NULL DEFAULT '',
  effort        TEXT NOT NULL DEFAULT '',
  agent_session TEXT NOT NULL DEFAULT '',
  host_dir      TEXT NOT NULL DEFAULT '',
  host_seq      INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  archived_at   INTEGER NOT NULL DEFAULT 0
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
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
-- The unique indexes are what make a retried request idempotent: the same
-- client id can only ever produce one chat and one message.
CREATE UNIQUE INDEX IF NOT EXISTS idx_chats_client ON chats(client_id) WHERE client_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_client ON messages(chat_id, client_id) WHERE client_id <> '';
CREATE INDEX IF NOT EXISTS idx_messages_rev ON messages(chat_id, rev);
`

// Open opens the database at path, creating it if it is not there.
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
		return nil, fmt.Errorf("create schema: %w", err)
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

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

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
	ClientID string `json:"client_id,omitempty"`

	// Agent is which program answers in this chat, and it is decided when the
	// chat is created: a conversation cannot change who it is with.
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	// AgentSession is the program's own session id, which is what lets a chat
	// be resumed after its process, or Socrates itself, was restarted. It is
	// not something to hand out, so it stays off the wire, as do the two
	// fields below: they are plumbing the browser has no use for.
	AgentSession string `json:"-"`
	HostDir      string `json:"-"`
	// HostSeq is the journal seq at which the current or last turn began - not
	// the last event consumed. See internal/engine.
	HostSeq int64 `json:"-"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
	// Archived is a chat that has been put away: it keeps its transcript but
	// owns nothing that is still running, and it is hidden from the sidebar
	// until the list is switched to showing everything. ArchivedAt is when
	// that happened, and zero for a chat that is active; it is what the list
	// query filters on, and the client only ever needs the boolean.
	Archived   bool  `json:"archived"`
	ArchivedAt int64 `json:"-"`
}

const chatCols = `id, title, workspace, client_id, agent, model, effort,
                  agent_session, host_dir, host_seq, created_at, updated_at, archived_at`

func scanChat(row interface{ Scan(...any) error }) (*Chat, error) {
	c := &Chat{}
	err := row.Scan(&c.ID, &c.Title, &c.Workspace, &c.ClientID, &c.Agent, &c.Model, &c.Effort,
		&c.AgentSession, &c.HostDir, &c.HostSeq, &c.CreatedAt, &c.UpdatedAt, &c.ArchivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	c.Archived = c.ArchivedAt > 0
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
	c.Archived, c.ArchivedAt = false, 0
	_, err := s.db.Exec(`INSERT INTO chats(id, title, workspace, client_id, agent, model, effort, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.Title, c.Workspace, c.ClientID,
		c.Agent, c.Model, c.Effort, c.CreatedAt, c.UpdatedAt)
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

// ListChats returns chats, newest activity first. Archived ones are left out
// unless they are asked for, which is what the sidebar's own switch decides.
func (s *Store) ListChats(includeArchived bool) ([]Chat, error) {
	where := ` WHERE archived_at = 0`
	if includeArchived {
		where = ``
	}
	rows, err := s.db.Query(`SELECT ` + chatCols + ` FROM chats` + where + ` ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// SetChatArchived puts a chat away or brings it back. The timestamp doubles as
// the flag, so a restored chat is indistinguishable from one that was never
// archived. updated_at is deliberately left alone: archiving is not activity,
// and reordering the sidebar because of it would only be confusing.
func (s *Store) SetChatArchived(id string, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := int64(0)
	if archived {
		ts = now()
	}
	res, err := s.db.Exec(`UPDATE chats SET archived_at = ? WHERE id = ?`, ts, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateChat writes title and workspace.
func (s *Store) UpdateChat(id, title, workspace string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET title = ?, workspace = ?, updated_at = ? WHERE id = ?`,
		title, workspace, now(), id)
	return err
}

// UpdateChatModel changes which model, and how hard, this chat's agent works.
// The agent itself is never changed: a different agent is a different
// conversation.
func (s *Store) UpdateChatModel(id, model, effort string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE chats SET model = ?, effort = ?, updated_at = ? WHERE id = ?`,
		model, effort, now(), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetChatSession records the agent's own session id, which is what a resume
// after a restart is built on.
func (s *Store) SetChatSession(id, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET agent_session = ? WHERE id = ?`, session, id)
	return err
}

// SetChatHost records which host directory a chat is using and resets the
// turn-start position in the same statement: a new host has a new journal that
// starts at seq 1, and carrying an old position over would make it skip its
// own first events. Passing an empty dir clears both.
func (s *Store) SetChatHost(id, hostDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET host_dir = ?, host_seq = 0 WHERE id = ?`, hostDir, id)
	return err
}

// SetChatHostSeq records where the turn that is starting begins in the host's
// journal. Written once per turn, never per event: no revision, no publish.
func (s *Store) SetChatHostSeq(id string, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET host_seq = ? WHERE id = ?`, seq, id)
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
		`DELETE FROM steps WHERE chat_id = ?`,
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

// AddMessage appends a visible message, or patches the one that is already
// there. The upsert is what makes replay harmless: the engine writes the
// assistant answer of a turn under a deterministic id, and adopting a turn
// that was partly applied before a restart has to end with the same one row,
// not two. The sequence number is deliberately not updated, so a replay does
// not reorder the transcript.
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
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET content = excluded.content, rev = excluded.rev,
			run_id = excluded.run_id`, m.ID, m.ChatID, m.RunID, m.Role, m.Content, m.Seq, m.Rev,
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

// -------------------------------------------------------------------- runs

// Run states.
const (
	RunRunning     = "running"
	RunDone        = "done"
	RunFailed      = "failed"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted"
)

// Run is one turn: a user message, everything the agent did about it, and the
// answer at the end.
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

// ActiveRun returns the run of a chat that is still in flight, if any.
func (s *Store) ActiveRun(chatID string) (*Run, error) {
	r := &Run{}
	var auto int
	err := s.db.QueryRow(`SELECT id, chat_id, status, error, auto, created_at, updated_at FROM runs
		WHERE chat_id = ? AND status = 'running' ORDER BY created_at DESC LIMIT 1`, chatID).
		Scan(&r.ID, &r.ChatID, &r.Status, &r.Error, &auto, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.Auto = auto == 1
	return r, err
}

// RecoverRuns marks runs that were in flight when the process died, except the
// ones the engine has just adopted from a host that kept running. Called once
// at startup so the UI never shows a spinner that will never finish - and with
// the exclusion list, so it does not interrupt the run Adopt claimed one line
// earlier, or the tool steps still running underneath its pump.
func (s *Store) RecoverRuns(except ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now()
	notIn, args := excludeClause("id", except)
	runArgs := append([]any{RunInterrupted, ts, RunRunning}, args...)
	if _, err := s.db.Exec(`UPDATE runs SET status = ?, error = 'Server restarted while this run was in progress.', updated_at = ?
		WHERE status = ?`+notIn, runArgs...); err != nil {
		return err
	}
	stepNotIn, stepArgs := excludeClause("run_id", except)
	all := append([]any{StatusInterrupted, ts, StatusRunning}, stepArgs...)
	_, err := s.db.Exec(`UPDATE steps SET status = ?, updated_at = ? WHERE status = ?`+stepNotIn, all...)
	return err
}

// excludeClause builds a "AND col NOT IN (?, ?)" fragment and its arguments.
// With nothing to exclude it returns an empty string, so the statement is
// exactly what it was before adoption existed.
func excludeClause(column string, ids []string) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	args := make([]any, 0, len(ids))
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, "?")
	}
	return " AND " + column + " NOT IN (" + strings.Join(placeholders, ", ") + ")", args
}

// ------------------------------------------------------------------- steps

// Step kinds shown in the process view. The set is closed and chat.js renders
// exactly these seven: adding one is a change to the browser as well.
const (
	StepTool      = "tool"
	StepSubagent  = "subagent"
	StepReasoning = "reasoning"
	// StepDraft is the answer while it is still being written. It is removed
	// when the turn ends and the text becomes a real assistant message.
	StepDraft  = "draft"
	StepUsage  = "usage"
	StepNotice = "notice"
	StepError  = "error"
)

// Step statuses.
const (
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// Step is one entry of the live process view.
type Step struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id"`
	ChatID    string          `json:"chat_id"`
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
	_, err := s.db.Exec(`INSERT INTO steps(id, run_id, chat_id, seq, rev, kind, title, body, detail, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET rev = excluded.rev, title = excluded.title, body = excluded.body,
			detail = excluded.detail, status = excluded.status, updated_at = excluded.updated_at`,
		st.ID, st.RunID, st.ChatID, st.Seq, st.Rev, st.Kind, st.Title, st.Body, detail,
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

// NextStepSeq returns the next ordering number for a chat's transcript.
//
// The scope is the chat and not the run on purpose: ListSteps orders a whole
// chat by seq, so a run whose counter restarted at 1 would have its steps sort
// in among the previous run's. It is what the engine seeds a run's counter
// with, at the start of a run and again when a turn is adopted after a
// restart.
func (s *Store) NextStepSeq(chatID string) (int64, error) {
	var maxSeq sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM steps WHERE chat_id = ?`, chatID).Scan(&maxSeq); err != nil {
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
		if err := rows.Scan(&st.ID, &st.RunID, &st.ChatID, &st.Seq, &st.Rev, &st.Kind,
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

const stepCols = `id, run_id, chat_id, seq, rev, kind, title, body, detail, status, created_at, updated_at`

// GetStep loads one step by id.
func (s *Store) GetStep(id string) (*Step, error) {
	rows, err := s.db.Query(`SELECT id, run_id, chat_id, seq, rev, kind, title, body,
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
