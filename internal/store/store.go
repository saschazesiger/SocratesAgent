// Package store is the persistence layer. Everything Socrates has to remember
// across a restart - the terminal sessions, the login cookies and the settings
// document - lives in a single SQLite file, so that a browser refresh, or a
// restart of the server, restores the exact state.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// SchemaVersion is the shape of the database this build understands. Version 3
// is the terminal harness: sessions are tmux sessions, and the chat transcript
// tables of versions 1 and 2 are gone. Version 4 adds title_source, which
// records who named a session and is what keeps the automatic title from
// running twice or overwriting a name the user chose.
const SchemaVersion = 4

// Store wraps the database handle.
type Store struct {
	db  *sql.DB
	rev atomic.Int64
	// revMark is the number the database has been told the counter may run up
	// to. It is guarded by mu, like every other write.
	revMark int64
	mu      sync.Mutex // serialises writes; SQLite has a single writer anyway
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS logins (
  token      TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id             TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL DEFAULT '',
  title          TEXT NOT NULL DEFAULT '',
  title_source   TEXT NOT NULL DEFAULT '',
  harness        TEXT NOT NULL,
  model          TEXT NOT NULL DEFAULT '',
  effort         TEXT NOT NULL DEFAULT '',
  workdir        TEXT NOT NULL,
  workdir_mode   TEXT NOT NULL,
  options        TEXT NOT NULL DEFAULT '{}',
  tmux_name      TEXT NOT NULL DEFAULT '',
  cli_session_id TEXT NOT NULL DEFAULT '',
  cli_session_state TEXT NOT NULL DEFAULT 'none',
  state          TEXT NOT NULL DEFAULT 'starting',
  exit_status    INTEGER NOT NULL DEFAULT -1,
  fail_reason    TEXT NOT NULL DEFAULT '',
  resumed        INTEGER NOT NULL DEFAULT 0,
  resume_count   INTEGER NOT NULL DEFAULT 0,
  cols           INTEGER NOT NULL DEFAULT 120,
  rows           INTEGER NOT NULL DEFAULT 40,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  last_attached  INTEGER NOT NULL DEFAULT 0,
  archived_at    INTEGER NOT NULL DEFAULT 0
);
-- The unique index on client_id is what makes creating a session safe to
-- retry: the same key can only ever produce one session.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_client ON sessions(client_id) WHERE client_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
-- And the one on tmux_name is what makes two rows for one tmux session
-- impossible, which is the mistake that would leave a terminal with no owner.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_tmux ON sessions(tmux_name) WHERE tmux_name <> '';
`

// Open opens the database at path, creating it if it is not there, and brings
// an older one up to the current schema.
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
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	st := &Store{db: db}
	seed := seedRev(db)
	st.rev.Store(seed)
	st.revMark = seed
	return st, nil
}

// revKey holds the revision the counter has been reserved up to, and
// revCheckpoint is the size of a reservation.
const (
	revKey        = "rev_high_water"
	revCheckpoint = 1024
)

// seedRev starts the revision counter above every number this database has
// already handed out. The counter has to be monotonic across a restart, or a
// browser that comes back holding a revision - the phone that lost signal in a
// car, which is the case this app is built for - would be told that nothing has
// changed since a number the server is about to hand out for the second time.
//
// Three floors, and the largest wins. Wall clock time makes an ordinary restart
// land far ahead of the run before it. The newest row covers a clock that went
// backwards. And the reserved mark covers the rest: it is written down before
// the numbers below it are handed out, so it is above everything the run that
// crashed could ever have used - including a run that wrote a thousand
// revisions inside one millisecond.
func seedRev(db *sql.DB) int64 {
	seed := time.Now().UnixMilli()
	var newest sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(updated_at) FROM sessions`).Scan(&newest); err == nil &&
		newest.Valid && newest.Int64 > seed {
		seed = newest.Int64
	}
	var mark int64
	if err := db.QueryRow(`SELECT CAST(value AS INTEGER) FROM kv WHERE key = ?`, revKey).Scan(&mark); err == nil &&
		mark > seed {
		seed = mark
	}
	return seed
}

// bump moves the revision on. When it reaches the number the database was told
// about, it reserves the next block of revCheckpoint before handing any of them
// out - so the stored mark is never behind what a crash could have used, and
// the cost of that guarantee is one statement per thousand writes.
//
// It talks to the database directly rather than through SetKV, because every
// caller already holds the write lock.
func (s *Store) bump() int64 {
	v := s.rev.Add(1)
	if v >= s.revMark {
		s.revMark = v + revCheckpoint
		// A failed reservation is not fatal - the counter still moves, and this
		// run stays monotonic - but it silently removes the guarantee across a
		// crash, so it is said out loud rather than swallowed.
		if _, err := s.db.Exec(`INSERT INTO kv(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, revKey, strconv.FormatInt(s.revMark, 10)); err != nil {
			log.Printf("store: could not reserve revisions up to %d: %v", s.revMark, err)
		}
	}
	return v
}

// migrate brings the file at path to SchemaVersion. The version lives in
// SQLite's own user_version pragma rather than in a table of our own, so that
// the check costs nothing and works on a database that has no tables yet.
func migrate(db *sql.DB, path string) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version == SchemaVersion {
		return nil
	}
	// A file from a newer build is not a legacy file. Creating the tables it
	// already has would do nothing and rewriting the version would hide the
	// mismatch, leaving this build to run against a schema it cannot know.
	if version > SchemaVersion {
		return fmt.Errorf("the database is at schema %d and this build understands %d: "+
			"it was written by a newer Socrates", version, SchemaVersion)
	}

	// A pre rewrite database is recognised by the table the rewrite removes.
	// user_version was never set by those builds, so the tables are the only
	// honest evidence of what this file is.
	legacy, err := hasTable(db, "chats")
	if err != nil {
		return err
	}
	if legacy {
		if err := backup(db, path); err != nil {
			// A backup that cannot be written is worth stopping for: the next
			// statement drops the only copy of the old transcripts.
			return fmt.Errorf("back up the database before migrating: %w", err)
		}
		if err := cutToTerminalSchema(db); err != nil {
			return fmt.Errorf("migrate to schema %d: %w", SchemaVersion, err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS does nothing to a table that is already
	// there, so a column added to an existing table has to be added by hand.
	if err := addSessionColumns(db); err != nil {
		return err
	}
	// PRAGMA takes no bound parameters, and the value is a constant.
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
		return fmt.Errorf("write schema version: %w", err)
	}
	return nil
}

// cutToTerminalSchema is the clean cut. The old chats are dropped rather than
// converted, because a converted chat would be a row that can never be opened:
// its transcript lived in the tables going away, its agent host is gone, and
// it never had a tmux session. An empty list is more honest than a list of
// things that do nothing.
//
// What survives is what is still true: the kv document with the password, the
// tunnel and the voice settings, and the login cookies - whose table has to
// give up the name `sessions`, because a terminal session is what that word
// means now.
func cutToTerminalSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DROP TABLE IF EXISTS steps`,
		`DROP TABLE IF EXISTS runs`,
		`DROP TABLE IF EXISTS messages`,
		`DROP TABLE IF EXISTS chats`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	// The rename only applies to the auth table. A database that already has
	// `logins` has been renamed before, and one whose `sessions` table is the
	// new one must not be touched at all.
	oldAuth, err := txHasColumn(tx, "sessions", "token")
	if err != nil {
		return err
	}
	newAuth, err := txHasTable(tx, "logins")
	if err != nil {
		return err
	}
	if oldAuth && !newAuth {
		if _, err := tx.Exec(`ALTER TABLE sessions RENAME TO logins`); err != nil {
			return err
		}
	}
	if err := migrateSettingsDocument(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateSettingsDocument moves the parts of the old settings JSON that still
// mean something into their new places: the three agents become three of the
// four harnesses, and the agent's workspace root becomes the workspace root.
// It is done here rather than in the config package because it is a fact about
// this one upgrade, not about the shape of the settings, and it works on the
// raw document so that a field this build has never heard of survives it.
func migrateSettingsDocument(tx *sql.Tx) error {
	var raw string
	err := tx.QueryRow(`SELECT value FROM kv WHERE key = 'settings'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &doc) != nil {
		// An unreadable settings document is replaced by the defaults on the
		// next start anyway; failing the whole migration over it would be
		// worse than losing settings that could not be read.
		return nil
	}

	if agents, ok := doc["agents"]; ok {
		var byID map[string]json.RawMessage
		if json.Unmarshal(agents, &byID) == nil {
			harnesses := map[string]json.RawMessage{}
			if h, ok := doc["harnesses"]; ok {
				_ = json.Unmarshal(h, &harnesses)
			}
			// Every field of the old entry - enabled, binary, extra_args and
			// the model short list - is a field of the new one under the same
			// name, so the entry moves across whole.
			for _, id := range []string{"claude", "codex", "opencode"} {
				if entry, ok := byID[id]; ok {
					if _, taken := harnesses[id]; !taken {
						harnesses[id] = entry
					}
				}
			}
			if encoded, err := json.Marshal(harnesses); err == nil {
				doc["harnesses"] = encoded
			}
		}
		delete(doc, "agents")
	}

	if agent, ok := doc["agent"]; ok {
		var old struct {
			WorkspaceRoot string `json:"workspace_root"`
		}
		if json.Unmarshal(agent, &old) == nil && old.WorkspaceRoot != "" {
			if _, taken := doc["workspace"]; !taken {
				if encoded, err := json.Marshal(map[string]string{"root": old.WorkspaceRoot}); err == nil {
					doc["workspace"] = encoded
				}
			}
		}
		delete(doc, "agent")
	}

	next, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	_, err = tx.Exec(`UPDATE kv SET value = ? WHERE key = 'settings'`, string(next))
	return err
}

// backup writes a copy of the database next to it before the cut. It costs one
// file and it makes an irreversible migration reversible by hand, which is the
// whole point of writing it.
func backup(db *sql.DB, path string) error {
	dest := path + ".pre-v3.bak"
	// VACUUM INTO refuses to overwrite, and a second attempt after a failed
	// start would otherwise never get its backup.
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		// A backup that failed half way is worse than none: it is a file
		// somebody will find, believe and act on.
		if rmErr := os.Remove(dest); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("%w (and the partial backup at %s could not be removed: %v)", err, dest, rmErr)
		}
		return err
	}
	log.Printf("store: migrating to schema %d; the previous database was copied to %s", SchemaVersion, dest)
	return nil
}

// addSessionColumns brings a sessions table from an older build up to the
// current shape. Every column here is added with a default, so an existing row
// is complete the moment the statement returns.
func addSessionColumns(db *sql.DB) error {
	for _, col := range [][2]string{
		{"title_source", `TEXT NOT NULL DEFAULT ''`},
	} {
		has, err := hasColumn(db, "sessions", col[0])
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN ` + col[0] + ` ` + col[1]); err != nil {
			return fmt.Errorf("add sessions.%s: %w", col[0], err)
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

func hasTable(db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func txHasTable(tx *sql.Tx, name string) (bool, error) {
	var found string
	err := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// txHasColumn reports whether a table exists and carries a column. It is how
// the auth table is told apart from the terminal one: both were called
// `sessions`, and only the old one has a `token`.
func txHasColumn(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
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

// Rev returns the current revision. It counts writes and only ever rises,
// across restarts included, so a client that holds a number knows whether
// anything at all has changed since it looked.
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

// DeleteKV removes a value, and says nothing when there was none: the callers
// are cleanups after a session that may never have had one.
func (s *Store) DeleteKV(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
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
