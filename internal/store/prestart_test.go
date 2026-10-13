package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// A prepared row is a row like any other, with no tmux name and no
// directory, and the schedule it was created with.
func TestPrestartRowIsStoredAsPrepared(t *testing.T) {
	st := openTest(t)
	row := newPrestart("p1")
	row.StartAt = 12345
	if err := st.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StatePrestart || got.TmuxName != "" || got.Workdir != "" || got.StartAt != 12345 {
		t.Fatalf("prepared row = %#v", got)
	}

	// A schedule on a row that is started from the outset means nothing and
	// is not stored.
	plain := newSession("a1")
	plain.StartAt = 999
	if err := st.CreateSession(plain); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("a1"); got.StartAt != 0 {
		t.Fatalf("a running row kept a schedule: %d", got.StartAt)
	}
}

func TestDraftSavesAreRevisionChecked(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newPrestart("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDraft("p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a fresh prepared session has a draft: %v", err)
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

// Once a session has started, its prompt was sent: the draft is kept, read
// only, and the settings can no longer be changed under it.
func TestAStartedSessionRefusesDraftAndSettings(t *testing.T) {
	st := openTest(t)
	row := newPrestart("p1")
	if err := st.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveDraft("p1", "the prompt", 0); err != nil {
		t.Fatal(err)
	}
	started := *row
	started.Workdir, started.WorkdirMode, started.TmuxName = "/srv/made", WorkdirDynamic, "soc_p1"
	started.Options = json.RawMessage(`{"profile":"p-1"}`)
	started.CLISessionID, started.CLISessionState = "conv", CLIKnown
	if err := st.BeginStart(&started); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginStart(&started); !errors.Is(err, ErrNotPrestart) {
		t.Fatalf("a second start was let through: %v", err)
	}
	got, _ := st.GetSession("p1")
	if got.State != StateStarting || got.TmuxName != "soc_p1" || got.Workdir != "/srv/made" ||
		got.CLISessionID != "conv" || string(got.Options) != `{"profile":"p-1"}` {
		t.Fatalf("started row = %#v", got)
	}
	if _, err := st.SaveDraft("p1", "edited later", 1); !errors.Is(err, ErrNotPrestart) {
		t.Fatalf("a started session's draft was edited: %v", err)
	}
	if d, err := st.GetDraft("p1"); err != nil || d.Text != "the prompt" {
		t.Fatalf("the draft did not survive the start: %#v %v", d, err)
	}
	if err := st.UpdatePrestart("p1", PrestartSettings{Harness: "codex"}); !errors.Is(err, ErrNotPrestart) {
		t.Fatalf("a started session's settings were changed: %v", err)
	}
	if err := st.UpdatePrestart("missing", PrestartSettings{Harness: "codex"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settings for no session: %v", err)
	}
}

// A start that failed before the program ran puts the row back exactly as it
// was prepared, with the reason and without the schedule.
func TestRevertStartRestoresThePreparedRow(t *testing.T) {
	st := openTest(t)
	row := newPrestart("p1")
	row.StartAt = time.Now().Add(time.Hour).UnixMilli()
	if err := st.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	prepared, _ := st.GetSession("p1")
	started := *prepared
	started.Workdir, started.TmuxName = "/srv/made", "soc_p1"
	if err := st.BeginStart(&started); err != nil {
		t.Fatal(err)
	}
	if err := st.RevertStart(prepared, "tmux said no"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSession("p1")
	if got.State != StatePrestart || got.TmuxName != "" || got.Workdir != "" || got.FailReason != "tmux said no" ||
		got.StartAt != 0 || string(got.Options) != `{"profile_auto":true}` {
		t.Fatalf("reverted row = %#v", got)
	}
	// Changing a setting clears the reason, which described the old one.
	if err := st.UpdatePrestart("p1", PrestartSettings{Harness: "codex", WorkdirMode: WorkdirDynamic}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.FailReason != "" || got.Harness != "codex" {
		t.Fatalf("after an update = %#v", got)
	}
}

// The scheduler's claim is exact: it takes the start only while the row is
// prepared and scheduled for the moment it saw.
func TestClaimScheduledStartIsExact(t *testing.T) {
	st := openTest(t)
	row := newPrestart("p1")
	row.StartAt = 1000
	if err := st.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimScheduledStart("p1", 999); ok || err != nil {
		t.Fatalf("a claim for another time won: %v %v", ok, err)
	}
	if ok, err := st.ClaimScheduledStart("p1", 1000); !ok || err != nil {
		t.Fatalf("the claim lost: %v %v", ok, err)
	}
	if ok, _ := st.ClaimScheduledStart("p1", 1000); ok {
		t.Fatal("one schedule was claimed twice")
	}
	if got, _ := st.GetSession("p1"); got.StartAt != 0 || got.State != StatePrestart {
		t.Fatalf("claimed row = %#v", got)
	}
}

func TestDeletingASessionDeletesItsDraft(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession(newPrestart("p1")); err != nil {
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

// A schema 7 database gains the drafts table, the pending table and the
// schedule column, and its rows are untouched.
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
		`DROP TABLE drafts`, `DROP TABLE mirror_pending`,
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
	if err != nil || got.StartAt != 0 || got.Harness != "claude" {
		t.Fatalf("the session after the migration = %#v %v", got, err)
	}
	for _, table := range []string{"drafts", "mirror_pending"} {
		if ok, err := hasTable(st.db, table); err != nil || !ok {
			t.Fatalf("table %s is missing: %v", table, err)
		}
	}
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 8 {
		t.Fatalf("user_version = %d %v", version, err)
	}
}

// A nameless prepared row is listed under what it will run: the start (and a
// change of settings) writes the new placeholder, a failed start puts the
// prepared one back, and a name somebody gave is never touched.
func TestAPreparedRowsPlaceholderTitleFollowsItsSettings(t *testing.T) {
	st := openTest(t)
	row := newPrestart("p1")
	row.Title = "Claude Code · repoA"
	if err := st.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePrestart("p1", PrestartSettings{Title: "Codex · repoB", Harness: "codex", WorkdirMode: WorkdirDynamic}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.Title != "Codex · repoB" {
		t.Fatalf("title after a change of settings = %q", got.Title)
	}
	if err := st.UpdatePrestart("p1", PrestartSettings{Harness: "codex", WorkdirMode: WorkdirDynamic}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.Title != "Codex · repoB" {
		t.Fatalf("an update without a title changed it to %q", got.Title)
	}

	prepared, _ := st.GetSession("p1")
	started := *prepared
	started.Title, started.TmuxName = "Codex · started", "soc_p1"
	if err := st.BeginStart(&started); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.Title != "Codex · started" {
		t.Fatalf("title after the start = %q", got.Title)
	}
	if err := st.RevertStart(prepared, "no"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.Title != "Codex · repoB" {
		t.Fatalf("title after a failed start = %q", got.Title)
	}

	if err := st.UpdateSessionTitle("p1", "Mine"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePrestart("p1", PrestartSettings{Title: "Claude Code · x", Harness: "claude", WorkdirMode: WorkdirDynamic}); err != nil {
		t.Fatal(err)
	}
	prepared, _ = st.GetSession("p1")
	started = *prepared
	started.Title = "Claude Code · y"
	if err := st.BeginStart(&started); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.Title != "Mine" {
		t.Fatalf("a name somebody gave was replaced by %q", got.Title)
	}
}

// The reconciliation writes are conditional on the state they were decided
// from, and news about a started program never touches a prepared row: a
// start that failed and put its row back is not turned into one to resume.
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

	// The poll may have declared a starting row lost in the middle of its
	// launch; a launch that then finds nothing ran still gets it back.
	prepared, _ := st.GetSession("p1")
	started := *prepared
	started.TmuxName = "soc_p1"
	if err := st.BeginStart(&started); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionStateFrom("p1", StateStarting, StateNeedsResume, -1, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.RevertStart(prepared, "no tmux"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession("p1"); got.State != StatePrestart || got.FailReason != "no tmux" {
		t.Fatalf("after the revert: %+v", got)
	}
}
