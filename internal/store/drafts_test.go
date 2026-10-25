package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPrestart is a prepared terminal of the build before the agents were
// chats, which the clean cut still meets.
func newPrestart(id string) *Session {
	return &Session{
		ID:          id,
		Title:       "Prepared",
		Harness:     "claude",
		Model:       "a-model",
		WorkdirMode: WorkdirDynamic,
		State:       StatePrestart,
		Options:     json.RawMessage(`{"profile_auto":true}`),
	}
}

// newChat is a chat nobody has written to yet.
func newChat(id string) *Session {
	return &Session{ID: id, Title: "Chat", Harness: "claude", Kind: KindChat, State: StateRunning,
		Workdir: "/srv/chat", WorkdirMode: WorkdirDynamic}
}

// putDraft writes a draft the way a build with PreStart did, for a row that
// SaveDraft refuses now.
func putDraft(t *testing.T, st *Store, id, text string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO drafts(session_id, text, rev, updated_at) VALUES(?, ?, 1, 1)`, id, text); err != nil {
		t.Fatal(err)
	}
}

func TestDraftSavesAreRevisionChecked(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newChat("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDraft("p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a fresh chat has a draft: %v", err)
	}
	before := st.Rev()
	first, err := st.SaveDraft("p1", "hello\nworld", 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Rev != 1 || first.Text != "hello\nworld" || first.UpdatedAt == 0 {
		t.Fatalf("first save = %#v", first)
	}
	if st.Rev() <= before {
		t.Fatal("a draft save did not move the revision, so no other device would notice it")
	}
	second, err := st.SaveDraft("p1", "hello again", 1)
	if err != nil || second.Rev != 2 {
		t.Fatalf("second save = %#v %v", second, err)
	}

	// A device that is still on revision 1 is told, and handed the text that
	// won.
	current, err := st.SaveDraft("p1", "stale", 1)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale save was taken: %v", err)
	}
	if current.Text != "hello again" || current.Rev != 2 {
		t.Fatalf("the conflict carried %#v", current)
	}

	if _, err := st.SaveDraft("missing", "x", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a draft for no session: %v", err)
	}

	meta, ok := st.DraftMetaOf("p1")
	if !ok || meta.Rev != 2 || meta.Preview != "hello again" {
		t.Fatalf("meta = %#v %v", meta, ok)
	}
	metas, err := st.DraftMetas()
	if err != nil || metas["p1"].Rev != 2 {
		t.Fatalf("metas = %#v %v", metas, err)
	}
}

func TestDraftPreviewIsTheFirstLineWithText(t *testing.T) {
	if got := draftPreview("\n   \n  First line  \nsecond"); got != "First line" {
		t.Fatalf("preview = %q", got)
	}
	long := strings.Repeat("ä", 300)
	got := draftPreview(long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != previewRunes+1 {
		t.Fatalf("a long line was cut to %d runes", len([]rune(got)))
	}
}

// A terminal has no draft: what is typed into it goes to its program.
func TestATerminalRefusesADraft(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveDraft("a1", "text", 0); !errors.Is(err, ErrNotChat) {
		t.Fatalf("a terminal took a draft: %v", err)
	}
}

func TestDeletingASessionDeletesItsDraft(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newChat("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveDraft("p1", "text", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSession("p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDraft("p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the draft outlived its session: %v", err)
	}
}

// A schema 7 database gains the drafts table and the schedule column, and
// its rows are untouched.
func TestMigrationToSchema8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socrates.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(newSession("a1")); err != nil {
		t.Fatal(err)
	}
	// Take the database back to what schema 7 looked like.
	for _, q := range []string{
		`DROP TABLE drafts`,
		`ALTER TABLE sessions DROP COLUMN start_at`, `PRAGMA user_version = 7`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("opening a schema 7 database: %v", err)
	}
	defer st.Close()
	got, err := st.GetSession("a1")
	if err != nil || got.Harness != "claude" {
		t.Fatalf("the session after the migration = %#v %v", got, err)
	}
	if ok, err := hasTable(st.db, "drafts"); err != nil || !ok {
		t.Fatalf("table drafts is missing: %v", err)
	}
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("user_version = %d %v", version, err)
	}
}

// The reconciliation writes are conditional on the state they were decided
// from, and news about a started program never touches a prepared row.
func TestStateWritesLeaveAPreparedRowAlone(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newPrestart("p1")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionStateFrom("p1", StateStarting, StateNeedsResume, -1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a write decided on a stale state went through: %v", err)
	}
	if err := st.SetStartedSessionState("p1", StateExited, 0, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("news about a program reached a prepared row: %v", err)
	}
	if got, _ := st.GetSession("p1"); got.State != StatePrestart {
		t.Fatalf("state = %q", got.State)
	}
}

// The clean cut makes a prepared terminal a prepared chat in place: the row,
// its settings and its draft stay, only what it is and its options change -
// and only for a prepared terminal.
func TestAPreparedTerminalBecomesAPreparedChat(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newPrestart("p1")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, st, "p1", "the first prompt")
	if err := st.MakePreparedChat("p1", json.RawMessage(`{"ultracode":true}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSession("p1")
	if !got.IsChat() || got.State != StatePrestart || got.Model != "a-model" || string(got.Options) != `{"ultracode":true}` {
		t.Fatalf("the prepared chat = %#v", got)
	}
	if draft, _ := st.GetDraft("p1"); draft.Text != "the first prompt" {
		t.Fatalf("draft = %q", draft.Text)
	}
	if err := st.MakePreparedChat("p1", nil); !errors.Is(err, ErrNotPrestart) {
		t.Fatalf("a chat made a chat again: %v", err)
	}
	if err := st.MakePreparedChat("nobody", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a row that is not there: %v", err)
	}
}

// A prepared chat of a build that had PreStart becomes an ordinary chat
// nobody has written to yet: running, in its folder, a past schedule gone
// and one still ahead kept, a
// placeholder name renewed and a given one kept, and its first prompt still
// its draft.
func TestAPreparedChatBecomesReady(t *testing.T) {
	st := openTest(t)
	for _, id := range []string{"p1", "p2", "p3"} {
		row := newPrestart(id)
		row.Kind = KindChat
		if err := st.CreateSession(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`UPDATE sessions SET start_at = 12345 WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().Add(time.Hour).UnixMilli()
	if _, err := st.db.Exec(`UPDATE sessions SET start_at = ? WHERE id = 'p3'`, ahead); err != nil {
		t.Fatal(err)
	}
	putDraft(t, st, "p1", "the first prompt")
	if err := st.UpdateSessionTitle("p2", "Mine"); err != nil {
		t.Fatal(err)
	}
	// A prepared terminal is the clean cut's, not this.
	if err := st.CreateSession(newPrestart("t1")); err != nil {
		t.Fatal(err)
	}

	list, err := st.PreparedChats()
	if err != nil || len(list) != 3 {
		t.Fatalf("prepared chats = %d %v", len(list), err)
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if err := st.ReadyPreparedChat(id, "/srv/"+id, "m", "high", "Claude Code · "+id); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetSession("p1")
	if !got.IsChat() || got.State != StateRunning || got.Workdir != "/srv/p1" || got.Model != "m" ||
		got.Effort != "high" || got.Title != "Claude Code · p1" {
		t.Fatalf("the ready chat = %#v", got)
	}
	if got.StartAt != 0 {
		t.Fatalf("a past schedule was kept: %d", got.StartAt)
	}
	if p3, _ := st.GetSession("p3"); p3.StartAt != ahead {
		t.Fatalf("the schedule ahead became %d", p3.StartAt)
	}
	if draft, err := st.GetDraft("p1"); err != nil || draft.Text != "the first prompt" {
		t.Fatalf("draft = %#v %v", draft, err)
	}
	if got, _ := st.GetSession("p2"); got.Title != "Mine" {
		t.Fatalf("a name somebody gave was replaced by %q", got.Title)
	}
	if err := st.ReadyPreparedChat("p1", "/x", "", "", ""); !errors.Is(err, ErrNotPrestart) {
		t.Fatalf("a ready chat was made ready again: %v", err)
	}
	if err := st.ReadyPreparedChat("t1", "/x", "", "", ""); !errors.Is(err, ErrNotPrestart) {
		t.Fatalf("a prepared terminal was made a ready chat: %v", err)
	}
	if list, _ := st.PreparedChats(); len(list) != 0 {
		t.Fatalf("still prepared: %d", len(list))
	}
}

// A chat's schedule is set, moved and cleared, and claimed only at exactly
// the time it holds, once; a terminal has none.
func TestAChatsScheduledStart(t *testing.T) {
	st := openTest(t)
	chat := newChat("c1")
	chat.StartAt = 500
	if err := st.CreateSession(chat); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("c1"); got.StartAt != 500 {
		t.Fatalf("created with %d", got.StartAt)
	}
	shell := newSession("a1")
	shell.StartAt = 999
	if err := st.CreateSession(shell); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("a1"); got.StartAt != 0 {
		t.Fatalf("a terminal kept a schedule: %d", got.StartAt)
	}
	if err := st.SetStartAt("a1", 5); !errors.Is(err, ErrNotChat) {
		t.Fatalf("a terminal was scheduled: %v", err)
	}
	if err := st.SetStartAt("nobody", 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no row: %v", err)
	}
	if err := st.SetStartAt("c1", 700); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimScheduledStart("c1", 500); ok || err != nil {
		t.Fatalf("a moved schedule was claimed at its old time: %v %v", ok, err)
	}
	if ok, err := st.ClaimScheduledStart("c1", 700); !ok || err != nil {
		t.Fatalf("the claim: %v %v", ok, err)
	}
	if ok, _ := st.ClaimScheduledStart("c1", 700); ok {
		t.Fatal("claimed twice")
	}
	if got, _ := st.GetSession("c1"); got.StartAt != 0 {
		t.Fatalf("after the claim: %d", got.StartAt)
	}
	if err := st.SetStartAt("c1", 800); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStartAt("c1", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("c1"); got.StartAt != 0 {
		t.Fatalf("after clearing: %d", got.StartAt)
	}
}
