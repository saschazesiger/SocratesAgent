package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestChatLifecycle(t *testing.T) {
	st := openTest(t)
	chat := &Chat{ID: "c1", Title: "First"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetChat("c1")
	if err != nil || got.Title != "First" {
		t.Fatalf("get: %v %#v", err, got)
	}
	if _, err := st.GetChat("missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := st.UpdateChat("c1", "Renamed", "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListChats(false)
	if err != nil || len(list) != 1 || list[0].Title != "Renamed" || list[0].Workspace != "/tmp/work" {
		t.Fatalf("list = %#v (%v)", list, err)
	}
	if err := st.AddMessage(&Message{ID: "m1", ChatID: "c1", Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteChat("c1"); err != nil {
		t.Fatal(err)
	}
	messages, _ := st.ListMessages("c1")
	if len(messages) != 0 {
		t.Errorf("messages should be gone: %#v", messages)
	}
}

func TestMessageSequence(t *testing.T) {
	st := openTest(t)
	_ = st.CreateChat(&Chat{ID: "c1"})
	for i := 0; i < 3; i++ {
		if err := st.AddMessage(&Message{ID: string(rune('a' + i)), ChatID: "c1", Role: "user", Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := st.ListMessages("c1")
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range messages {
		if m.Seq != int64(i+1) {
			t.Fatalf("message %d has seq %d", i, m.Seq)
		}
	}
}

func TestStepsSinceRevision(t *testing.T) {
	st := openTest(t)
	_ = st.CreateChat(&Chat{ID: "c1"})
	_ = st.CreateRun(&Run{ID: "r1", ChatID: "c1", Status: RunRunning})

	first := &Step{ID: "s1", RunID: "r1", ChatID: "c1", Seq: 1, Kind: StepDraft, Body: "one", Status: StatusDone}
	if err := st.PutStep(first); err != nil {
		t.Fatal(err)
	}
	mark := st.Rev()
	second := &Step{ID: "s2", RunID: "r1", ChatID: "c1", Seq: 2, Kind: StepDraft, Body: "two", Status: StatusDone}
	if err := st.PutStep(second); err != nil {
		t.Fatal(err)
	}
	changed, err := st.StepsSince("c1", mark)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].ID != "s2" {
		t.Fatalf("expected only the new step, got %#v", changed)
	}

	// Updating an old step must bring it back with a fresh revision.
	first.Body = "one updated"
	if err := st.PutStep(first); err != nil {
		t.Fatal(err)
	}
	changed, _ = st.StepsSince("c1", mark)
	if len(changed) != 2 {
		t.Fatalf("expected both steps, got %#v", changed)
	}
	all, _ := st.ListSteps("c1")
	if len(all) != 2 || all[0].Body != "one updated" {
		t.Fatalf("upsert failed: %#v", all)
	}
}

func TestRecoverRuns(t *testing.T) {
	st := openTest(t)
	_ = st.CreateChat(&Chat{ID: "c1"})
	_ = st.CreateRun(&Run{ID: "r1", ChatID: "c1", Status: RunRunning})
	_ = st.PutStep(&Step{ID: "s1", RunID: "r1", ChatID: "c1", Kind: StepDraft, Status: StatusRunning})

	if err := st.RecoverRuns(); err != nil {
		t.Fatal(err)
	}
	run, _ := st.GetRun("r1")
	if run.Status != RunInterrupted {
		t.Errorf("run status = %q", run.Status)
	}
	steps, _ := st.ListSteps("c1")
	if steps[0].Status != StatusInterrupted {
		t.Errorf("step status = %q", steps[0].Status)
	}
}

func TestSessions(t *testing.T) {
	st := openTest(t)
	if err := st.CreateSession("token", time.Hour); err != nil {
		t.Fatal(err)
	}
	if !st.ValidSession("token") {
		t.Fatal("session should be valid")
	}
	if st.ValidSession("other") {
		t.Fatal("unknown token must not validate")
	}
	if err := st.DeleteSession("token"); err != nil {
		t.Fatal(err)
	}
	if st.ValidSession("token") {
		t.Fatal("deleted session still valid")
	}
	_ = st.CreateSession("expired", -time.Hour)
	if st.ValidSession("expired") {
		t.Fatal("expired session must not validate")
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

// The reconnect story rests on one promise: a client that knows the last
// revision it saw can ask for everything after it and get exactly that.
func TestMessagesCarryRevisionsAndReplay(t *testing.T) {
	st := openTest(t)
	if err := st.CreateChat(&Chat{ID: "c1"}); err != nil {
		t.Fatal(err)
	}
	first := &Message{ID: "m1", ChatID: "c1", Role: "user", Content: "one"}
	if err := st.AddMessage(first); err != nil {
		t.Fatal(err)
	}
	if first.Rev == 0 {
		t.Fatal("a message must be given a revision")
	}
	mark := st.Rev()
	second := &Message{ID: "m2", ChatID: "c1", Role: "assistant", Content: "two"}
	if err := st.AddMessage(second); err != nil {
		t.Fatal(err)
	}
	if second.Rev <= first.Rev {
		t.Fatalf("revisions must increase: %d then %d", first.Rev, second.Rev)
	}

	missed, err := st.MessagesSince("c1", mark)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 1 || missed[0].ID != "m2" {
		t.Fatalf("expected only the newer message, got %#v", missed)
	}
	if all, err := st.MessagesSince("c1", 0); err != nil || len(all) != 2 {
		t.Fatalf("from zero every message should come back, got %#v (%v)", all, err)
	}
}

// Reopening must not hand out revisions that were already used, or a browser
// would be told that rows it has already seen are new.
func TestRevisionSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateChat(&Chat{ID: "c1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMessage(&Message{ID: "m1", ChatID: "c1", Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	high := st.Rev()
	st.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.Rev() < high {
		t.Fatalf("revision went backwards on reopen: %d then %d", high, again.Rev())
	}
}

// The idempotency keys are what make a retry over a bad connection safe.
func TestClientIDsFindExistingRows(t *testing.T) {
	st := openTest(t)
	if err := st.CreateChat(&Chat{ID: "c1", ClientID: "key-chat"}); err != nil {
		t.Fatal(err)
	}
	found, err := st.ChatByClientID("key-chat")
	if err != nil || found.ID != "c1" {
		t.Fatalf("chat by client id = %#v (%v)", found, err)
	}
	if _, err := st.ChatByClientID(""); err != ErrNotFound {
		t.Fatalf("an empty key must never match: %v", err)
	}
	if err := st.CreateChat(&Chat{ID: "c2", ClientID: "key-chat"}); err == nil {
		t.Fatal("the same key must not be able to create a second chat")
	}

	if err := st.AddMessage(&Message{ID: "m1", ChatID: "c1", Role: "user", Content: "hi", ClientID: "key-msg"}); err != nil {
		t.Fatal(err)
	}
	msg, err := st.MessageByClientID("c1", "key-msg")
	if err != nil || msg.ID != "m1" {
		t.Fatalf("message by client id = %#v (%v)", msg, err)
	}
	if _, err := st.MessageByClientID("c1", "other"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := st.AddMessage(&Message{ID: "m2", ChatID: "c1", Role: "user", Content: "hi", ClientID: "key-msg"}); err == nil {
		t.Fatal("the same key must not be able to store the message twice")
	}
	// Two chats may well use the same key for their own first message.
	if err := st.CreateChat(&Chat{ID: "c3"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMessage(&Message{ID: "m3", ChatID: "c3", Role: "user", Content: "hi", ClientID: "key-msg"}); err != nil {
		t.Fatalf("a key is only unique inside its chat: %v", err)
	}
}

// A deletion carries no revision, so a client that was away is told which
// steps still exist instead of showing the removed ones forever.
func TestStepIDsReportWhatIsLeft(t *testing.T) {
	st := openTest(t)
	if err := st.CreateChat(&Chat{ID: "c1"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2"} {
		if err := st.PutStep(&Step{ID: id, ChatID: "c1", RunID: "r1", Kind: "text", Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteStep("s1"); err != nil {
		t.Fatal(err)
	}
	ids, err := st.StepIDs("c1")
	if err != nil || len(ids) != 1 || ids[0] != "s2" {
		t.Fatalf("step ids = %#v (%v)", ids, err)
	}
}

// Databases created before these columns existed have to keep working.
func TestMigrationAddsColumnsToAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
CREATE TABLE chats (id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE messages (id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, run_id TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL, content TEXT NOT NULL, seq INTEGER NOT NULL, created_at INTEGER NOT NULL);
INSERT INTO chats VALUES ('c1', 'Old chat', '', 1, 1);
INSERT INTO messages VALUES ('m1', 'c1', '', 'user', 'from before', 1, 1);
`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("opening an old database must migrate it, got: %v", err)
	}
	defer st.Close()
	chat, err := st.GetChat("c1")
	if err != nil || chat.Title != "Old chat" {
		t.Fatalf("chat = %#v (%v)", chat, err)
	}
	msgs, err := st.ListMessages("c1")
	if err != nil || len(msgs) != 1 || msgs[0].Content != "from before" {
		t.Fatalf("messages = %#v (%v)", msgs, err)
	}
	// The new columns are usable straight away.
	if err := st.AddMessage(&Message{ID: "m2", ChatID: "c1", Role: "user", Content: "after", ClientID: "k"}); err != nil {
		t.Fatalf("writing with the new columns: %v", err)
	}
	if found, err := st.MessageByClientID("c1", "k"); err != nil || found.ID != "m2" {
		t.Fatalf("lookup after migration = %#v (%v)", found, err)
	}
}

// Archiving is the half way house between keeping a chat and deleting it: the
// transcript survives, and the sidebar stops showing it until it is asked for.
func TestArchiveChat(t *testing.T) {
	st := openTest(t)
	if err := st.CreateChat(&Chat{ID: "c1", Title: "Kept"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateChat(&Chat{ID: "c2", Title: "Put away"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMessage(&Message{ID: "m1", ChatID: "c2", Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatArchived("c2", true); err != nil {
		t.Fatal(err)
	}

	active, err := st.ListChats(false)
	if err != nil || len(active) != 1 || active[0].ID != "c1" {
		t.Fatalf("active list = %#v (%v)", active, err)
	}
	all, err := st.ListChats(true)
	if err != nil || len(all) != 2 {
		t.Fatalf("full list = %#v (%v)", all, err)
	}
	got, err := st.GetChat("c2")
	if err != nil || !got.Archived || got.ArchivedAt == 0 {
		t.Fatalf("archived chat = %#v (%v)", got, err)
	}
	// The conversation is the whole point of archiving rather than deleting.
	if messages, err := st.ListMessages("c2"); err != nil || len(messages) != 1 {
		t.Fatalf("messages = %#v (%v)", messages, err)
	}

	if err := st.SetChatArchived("c2", false); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetChat("c2")
	if err != nil || got.Archived || got.ArchivedAt != 0 {
		t.Fatalf("restored chat = %#v (%v)", got, err)
	}
	if active, err := st.ListChats(false); err != nil || len(active) != 2 {
		t.Fatalf("restored chat missing from the active list: %#v (%v)", active, err)
	}
	if err := st.SetChatArchived("missing", true); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// A chat is bound to one agent when it is created, and the binding, the native
// session id and the host bookkeeping all have to survive a reload - the
// restart promise is built on the last two.
func TestChatCarriesItsAgentBinding(t *testing.T) {
	st := openTest(t)
	if err := st.CreateChat(&Chat{ID: "c1", Agent: "claude", Model: "sonnet", Effort: "medium"}); err != nil {
		t.Fatal(err)
	}
	chat, err := st.GetChat("c1")
	if err != nil {
		t.Fatal(err)
	}
	if chat.Agent != "claude" || chat.Model != "sonnet" || chat.Effort != "medium" {
		t.Fatalf("binding = %#v", chat)
	}
	if chat.AgentSession != "" || chat.HostDir != "" || chat.HostSeq != 0 {
		t.Fatalf("a fresh chat already has host bookkeeping: %#v", chat)
	}

	if err := st.UpdateChatModel("c1", "opus", "high"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatSession("c1", "sess_abc"); err != nil {
		t.Fatal(err)
	}
	chat, _ = st.GetChat("c1")
	if chat.Model != "opus" || chat.Effort != "high" || chat.AgentSession != "sess_abc" {
		t.Fatalf("after the change: %#v", chat)
	}
	if chat.Agent != "claude" {
		t.Fatal("changing the model changed the agent")
	}
	if err := st.UpdateChatModel("nothing", "opus", ""); err != ErrNotFound {
		t.Errorf("updating a chat that is not there = %v", err)
	}

	// The session id is internal plumbing and a session id is not something to
	// hand out, so none of the three reach the browser.
	raw, err := json.Marshal(chat)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"sess_abc", "agent_session", "host_dir", "host_seq"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("%s is written to the wire: %s", gone, raw)
		}
	}
}

// host_seq means "where this turn began". A new host has a new journal that
// starts at seq 1, so carrying an old position over would make the chat skip
// the first events of its own fresh journal and wait forever for an end it had
// already gone past.
func TestSetChatHostResetsTheTurnPosition(t *testing.T) {
	st := openTest(t)
	if err := st.CreateChat(&Chat{ID: "c1", Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatHost("c1", "/data/agents/host_a"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChatHostSeq("c1", 412); err != nil {
		t.Fatal(err)
	}
	chat, _ := st.GetChat("c1")
	if chat.HostDir != "/data/agents/host_a" || chat.HostSeq != 412 {
		t.Fatalf("chat = %#v", chat)
	}

	if err := st.SetChatHost("c1", "/data/agents/host_b"); err != nil {
		t.Fatal(err)
	}
	chat, _ = st.GetChat("c1")
	if chat.HostDir != "/data/agents/host_b" || chat.HostSeq != 0 {
		t.Fatalf("a new host kept the old turn position: %#v", chat)
	}

	// Clearing the host clears both.
	if err := st.SetChatHost("c1", ""); err != nil {
		t.Fatal(err)
	}
	chat, _ = st.GetChat("c1")
	if chat.HostDir != "" || chat.HostSeq != 0 {
		t.Fatalf("clearing left something behind: %#v", chat)
	}
}

// Without the exclusion list the run Adopt has just claimed is marked
// interrupted one line after being adopted, and its running tool steps are
// interrupted underneath the pump that is still filling them in.
func TestRecoverRunsSkipsWhatWasAdopted(t *testing.T) {
	st := openTest(t)
	_ = st.CreateChat(&Chat{ID: "c1"})
	_ = st.CreateRun(&Run{ID: "kept", ChatID: "c1", Status: RunRunning})
	_ = st.CreateRun(&Run{ID: "lost", ChatID: "c1", Status: RunRunning})
	_ = st.PutStep(&Step{ID: "s_kept", RunID: "kept", ChatID: "c1", Kind: StepTool, Status: StatusRunning})
	_ = st.PutStep(&Step{ID: "s_lost", RunID: "lost", ChatID: "c1", Kind: StepTool, Status: StatusRunning})

	if err := st.RecoverRuns("kept"); err != nil {
		t.Fatal(err)
	}
	if run, _ := st.GetRun("kept"); run.Status != RunRunning {
		t.Errorf("the adopted run was interrupted anyway: %q", run.Status)
	}
	if run, _ := st.GetRun("lost"); run.Status != RunInterrupted {
		t.Errorf("the orphaned run was not recovered: %q", run.Status)
	}
	steps := map[string]string{}
	all, _ := st.ListSteps("c1")
	for _, s := range all {
		steps[s.ID] = s.Status
	}
	if steps["s_kept"] != StatusRunning {
		t.Errorf("a step of the adopted run was interrupted underneath its pump: %q", steps["s_kept"])
	}
	if steps["s_lost"] != StatusInterrupted {
		t.Errorf("a step of the orphaned run was not recovered: %q", steps["s_lost"])
	}
}
