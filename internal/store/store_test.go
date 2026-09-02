package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newSession(id string) *Session {
	return &Session{
		ID:          id,
		Title:       "Claude · repo",
		Harness:     "claude",
		Model:       "claude-sonnet-4-5",
		Workdir:     "/srv/repo",
		WorkdirMode: WorkdirPreset,
		TmuxName:    "soc_" + id,
	}
}

func TestSessionLifecycle(t *testing.T) {
	st := openTest(t)
	sess := newSession("a1")
	if err := st.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	// A fresh session is starting, has never exited, and has a size even
	// though nobody said one.
	if sess.State != StateStarting || sess.ExitStatus != -1 || sess.CLISessionState != CLINone {
		t.Fatalf("fresh session = %#v", sess)
	}
	if sess.Cols != DefaultCols || sess.Rows != DefaultRows {
		t.Fatalf("size = %dx%d", sess.Cols, sess.Rows)
	}

	got, err := st.GetSession("a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TmuxName != "soc_a1" || got.Harness != "claude" || string(got.Options) != "{}" {
		t.Fatalf("stored session = %#v", got)
	}

	if err := st.UpdateSessionTitle("a1", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionState("a1", StateExited, 7, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionCLI("a1", "9f2c", CLIVerified); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionSize("a1", 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteAttach("a1"); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteResume("a1"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetSession("a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Renamed" || got.State != StateExited || got.ExitStatus != 7 {
		t.Fatalf("after updates = %#v", got)
	}
	if got.CLISessionID != "9f2c" || got.CLISessionState != CLIVerified {
		t.Fatalf("cli session = %q/%q", got.CLISessionID, got.CLISessionState)
	}
	if got.Cols != 100 || got.Rows != 30 || got.LastAttached == 0 {
		t.Fatalf("size and attach = %#v", got)
	}
	if !got.Resumed || got.ResumeCount != 1 {
		t.Fatalf("resume = %v/%d", got.Resumed, got.ResumeCount)
	}
	if err := st.ClearResumedFlag("a1"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetSession("a1"); got.Resumed || got.ResumeCount != 1 {
		t.Fatalf("the notice was cleared but so was the count: %#v", got)
	}

	if err := st.DeleteSession("a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession("a1"); err != ErrNotFound {
		t.Fatalf("deleted session = %v", err)
	}
	// Every write says so when there is nothing to write to, rather than
	// reporting success on a session that is gone.
	if err := st.UpdateSessionTitle("a1", "x"); err != ErrNotFound {
		t.Fatalf("update of a missing session = %v", err)
	}
	if err := st.DeleteSession("a1"); err != ErrNotFound {
		t.Fatalf("second delete = %v", err)
	}
}

// Creating a session is what a phone on a bad link retries, so the same key
// has to find the row it already made instead of making a second one.
func TestCreateSessionIsIdempotentOnClientID(t *testing.T) {
	st := openTest(t)
	first := newSession("a1")
	first.ClientID = "key-1"
	if err := st.CreateSession(first); err != nil {
		t.Fatal(err)
	}
	retry := newSession("a2")
	retry.ClientID = "key-1"
	if err := st.CreateSession(retry); err != nil {
		t.Fatal(err)
	}
	// The retry is told about the session that exists, not about the one it
	// asked for: the caller goes on to attach to a terminal that is real.
	if retry.ID != "a1" {
		t.Fatalf("retry created a second session: %#v", retry)
	}
	list, err := st.ListSessions(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("sessions = %#v", list)
	}
	if found, err := st.SessionByClientID("key-1"); err != nil || found.ID != "a1" {
		t.Fatalf("lookup by key = %#v (%v)", found, err)
	}
	if _, err := st.SessionByClientID(""); err != ErrNotFound {
		t.Fatalf("empty key = %v", err)
	}

	// An empty key is not a key: two sessions without one are two sessions.
	if err := st.CreateSession(newSession("b1")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(newSession("b2")); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.ListSessions(true); len(list) != 3 {
		t.Fatalf("sessions = %d", len(list))
	}
}

// One tmux session can only have one owner: a second row claiming it would be
// two Socrates sessions writing into one terminal.
func TestTmuxNameIsUnique(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	clash := newSession("a2")
	clash.TmuxName = "soc_a1"
	if err := st.CreateSession(clash); err == nil {
		t.Fatal("a second row claimed the same tmux session")
	}
}

func TestListSessionsOrdersAndHidesArchived(t *testing.T) {
	st := openTest(t)
	for _, id := range []string{"a1", "a2"} {
		if err := st.CreateSession(newSession(id)); err != nil {
			t.Fatal(err)
		}
	}
	// Touching a1 puts it in front: the list is ordered by activity.
	if err := st.NoteAttach("a1"); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListSessions(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "a1" {
		t.Fatalf("list = %#v", list)
	}

	if err := st.SetSessionArchived("a2", true); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.ListSessions(false); len(list) != 1 || list[0].ID != "a1" {
		t.Fatalf("archived session still listed: %#v", list)
	}
	list, _ = st.ListSessions(true)
	if len(list) != 2 {
		t.Fatalf("archived session missing from the full list: %#v", list)
	}
	// A restored session is indistinguishable from one that was never away.
	if err := st.SetSessionArchived("a2", false); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.ListSessions(false); len(list) != 2 || list[1].Archived {
		t.Fatalf("restored session = %#v", list)
	}
}

// The options snapshot is what the session was launched with, and it has to
// come back out byte for byte: it is what a restart relaunches from.
func TestSessionOptionsRoundTrip(t *testing.T) {
	st := openTest(t)
	sess := newSession("a1")
	sess.Options = json.RawMessage(`{"sandbox":"workspace-write","extra_args":["--search"]}`)
	if err := st.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession("a1")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Sandbox   string   `json:"sandbox"`
		ExtraArgs []string `json:"extra_args"`
	}
	if err := json.Unmarshal(got.Options, &out); err != nil {
		t.Fatal(err)
	}
	if out.Sandbox != "workspace-write" || len(out.ExtraArgs) != 1 {
		t.Fatalf("options = %#v", out)
	}
}

// Every write bumps the revision, so a client holding a number knows whether
// anything changed at all without comparing rows.
func TestWritesBumpTheRevision(t *testing.T) {
	st := openTest(t)
	before := st.Rev()
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	afterCreate := st.Rev()
	if afterCreate <= before {
		t.Fatalf("create did not bump the revision: %d -> %d", before, afterCreate)
	}
	if err := st.UpdateSessionTitle("a1", "x"); err != nil {
		t.Fatal(err)
	}
	if st.Rev() <= afterCreate {
		t.Fatalf("update did not bump the revision: %d", st.Rev())
	}
}

func TestLogins(t *testing.T) {
	st := openTest(t)
	if err := st.CreateLogin("token", time.Hour); err != nil {
		t.Fatal(err)
	}
	if !st.ValidLogin("token") {
		t.Fatal("login should be valid")
	}
	if st.ValidLogin("other") || st.ValidLogin("") {
		t.Fatal("an unknown token must not validate")
	}
	if err := st.DeleteLogin("token"); err != nil {
		t.Fatal(err)
	}
	if st.ValidLogin("token") {
		t.Fatal("deleted login still valid")
	}
	_ = st.CreateLogin("expired", -time.Hour)
	if st.ValidLogin("expired") {
		t.Fatal("expired login must not validate")
	}
	if err := st.PurgeExpiredLogins(); err != nil {
		t.Fatal(err)
	}
	_ = st.CreateLogin("one", time.Hour)
	_ = st.CreateLogin("two", time.Hour)
	if err := st.DeleteAllLogins(); err != nil {
		t.Fatal(err)
	}
	if st.ValidLogin("one") || st.ValidLogin("two") {
		t.Fatal("changing the password left a browser signed in")
	}
}

func TestKV(t *testing.T) {
	st := openTest(t)
	if _, err := st.GetKV("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	type payload struct{ Name string }
	if err := st.SetJSON("k", payload{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	var out payload
	if err := st.GetJSON("k", &out); err != nil || out.Name != "x" {
		t.Fatalf("out = %#v (%v)", out, err)
	}
}

// v2 is the database this build replaces: the chat transcript, and an auth
// table that was called `sessions` before a session became a terminal.
const schemaV2 = `
CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE chats (
  id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
  client_id TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  effort TEXT NOT NULL DEFAULT '', agent_session TEXT NOT NULL DEFAULT '',
  host_dir TEXT NOT NULL DEFAULT '', host_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, seq INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE runs (id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE steps (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, chat_id TEXT NOT NULL, seq INTEGER NOT NULL, rev INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
CREATE INDEX idx_messages_chat ON messages(chat_id, seq);
CREATE UNIQUE INDEX idx_chats_client ON chats(client_id) WHERE client_id <> '';
`

// buildV2 writes a pre rewrite database by hand: the old tables, a login that
// must survive, a settings document that must be carried across, and a chat
// that must not be.
func buildV2(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "socrates.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaV2); err != nil {
		t.Fatal(err)
	}
	settings := `{"openrouter":{"api_key":"sk-test"},"agent":{"workspace_root":"/srv/work"},` +
		`"agents":{"claude":{"enabled":false,"binary":"/opt/claude","models":[{"id":"m1","effort":"high"}]},` +
		`"codex":{"enabled":true},"opencode":{"enabled":true}},"tunnel":{"enabled":true,"mode":"quick"}}`
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO kv(key, value) VALUES('password_hash', 'pbkdf2-sha256$1$a$b')`, nil},
		{`INSERT INTO kv(key, value) VALUES('settings', ?)`, []any{settings}},
		{`INSERT INTO sessions(token, created_at, expires_at) VALUES('tok', 1, ?)`,
			[]any{time.Now().Add(time.Hour).UnixMilli()}},
		{`INSERT INTO chats(id, title, created_at, updated_at) VALUES('c1', 'Old chat', 1, 1)`, nil},
		{`INSERT INTO messages(id, chat_id, seq, created_at) VALUES('m1', 'c1', 1, 1)`, nil},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// The clean cut: the transcript tables go, the auth table gives up the name
// `sessions` and keeps its rows, and the kv document survives with the parts
// of it that still mean something moved into their new places.
func TestMigrationDropsChatsAndRenamesSessions(t *testing.T) {
	path := buildV2(t)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open a v2 database: %v", err)
	}
	defer st.Close()

	for _, table := range []string{"chats", "messages", "runs", "steps"} {
		if ok, err := hasTable(st.db, table); err != nil || ok {
			t.Errorf("table %s survived the cut (%v)", table, err)
		}
	}
	if ok, err := hasTable(st.db, "logins"); err != nil || !ok {
		t.Fatalf("logins is missing (%v)", err)
	}
	// The rows came with the name change: nobody is signed out by an upgrade.
	if !st.ValidLogin("tok") {
		t.Error("the login did not survive the rename")
	}
	// And `sessions` is now the terminal table, which the new columns prove.
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatalf("the new sessions table is not usable: %v", err)
	}

	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}

	// The password survives, because it is in kv and kv is kept.
	if v, err := st.GetKV("password_hash"); err != nil || v == "" {
		t.Errorf("password_hash = %q (%v)", v, err)
	}
	var doc map[string]json.RawMessage
	if err := st.GetJSON("settings", &doc); err != nil {
		t.Fatal(err)
	}
	if _, present := doc["agents"]; present {
		t.Error("the settings document still has an agents section")
	}
	if _, present := doc["agent"]; present {
		t.Error("the settings document still has an agent section")
	}
	if _, kept := doc["tunnel"]; !kept {
		t.Error("the tunnel settings were lost")
	}
	// The three agents are three of the four harnesses, and everything the
	// user configured about them moved across whole.
	var harnesses struct {
		Claude struct {
			Enabled bool   `json:"enabled"`
			Binary  string `json:"binary"`
			Models  []struct {
				ID     string `json:"id"`
				Effort string `json:"effort"`
			} `json:"models"`
		} `json:"claude"`
	}
	if err := json.Unmarshal(doc["harnesses"], &harnesses); err != nil {
		t.Fatal(err)
	}
	if harnesses.Claude.Enabled || harnesses.Claude.Binary != "/opt/claude" {
		t.Errorf("claude settings did not move across: %#v", harnesses.Claude)
	}
	if len(harnesses.Claude.Models) != 1 || harnesses.Claude.Models[0].ID != "m1" {
		t.Errorf("the model short list was lost: %#v", harnesses.Claude.Models)
	}
	var workspace struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(doc["workspace"], &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.Root != "/srv/work" {
		t.Errorf("workspace root = %q", workspace.Root)
	}

	// The backup is the whole reason an irreversible cut is safe to run.
	backupPath := path + ".pre-v3.bak"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("no backup was written: %v", err)
	}
	old, err := sql.Open("sqlite", "file:"+backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var title string
	if err := old.QueryRow(`SELECT title FROM chats WHERE id = 'c1'`).Scan(&title); err != nil {
		t.Fatalf("the backup does not hold the old chats: %v", err)
	}
	if title != "Old chat" {
		t.Errorf("backed up chat title = %q", title)
	}
}

// Opening the migrated database again is an ordinary start: the version says
// so, and nothing is backed up or dropped a second time.
func TestMigrationIsIdempotent(t *testing.T) {
	path := buildV2(t)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	st.Close()

	backupPath := path + ".pre-v3.bak"
	if err := os.Remove(backupPath); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.GetSession("a1"); err != nil {
		t.Fatalf("the second open lost the sessions: %v", err)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("a database that needs no migration was backed up anyway")
	}
}

// A fresh installation is not a migration: it gets the schema and the version
// and no backup file next to it.
func TestFreshDatabaseIsAtTheCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socrates.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	if _, err := os.Stat(path + ".pre-v3.bak"); !os.IsNotExist(err) {
		t.Error("a fresh database was backed up")
	}
}
