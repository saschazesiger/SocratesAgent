package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOutboxIsIdempotentAndOrdered(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"a", "b", "a"} {
		if _, err := st.PutOutbox("chat", id, json.RawMessage(`{"op":"send","client_id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, _ := st.PutOutbox("chat", "a", json.RawMessage(`{}`)); inserted {
		t.Fatal("a repeated client_id was inserted again")
	}
	st.PutOutbox("other", "a", json.RawMessage(`{}`))
	got, err := st.Outbox("chat")
	if err != nil || len(got) != 2 || got[0].ClientID != "a" || got[1].ClientID != "b" {
		t.Fatalf("outbox %+v %v", got, err)
	}
	st.DeleteOutbox("chat", "a")
	if got, _ := st.Outbox("chat"); len(got) != 1 || got[0].ClientID != "b" {
		t.Fatalf("after delete %+v", got)
	}
	st.ClearOutbox("chat")
	if got, _ := st.Outbox("chat"); len(got) != 0 {
		t.Fatalf("after clear %+v", got)
	}
	if got, _ := st.Outbox("other"); len(got) != 1 {
		t.Fatalf("another chat's outbox %+v", got)
	}
}

func TestSessionKindDefaultsAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	term := &Session{ID: "t", Harness: "shell", Workdir: "/tmp", WorkdirMode: WorkdirCustom}
	chat := &Session{ID: "c", Harness: "fake", Workdir: "/tmp", WorkdirMode: WorkdirCustom, Kind: KindChat}
	if err := st.CreateSession(term); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(chat); err != nil {
		t.Fatal(err)
	}
	if term.Kind != KindTerminal || !chat.IsChat() {
		t.Fatalf("kinds %q %q", term.Kind, chat.Kind)
	}
	if err := st.SetChatConversation("c", "conv-1"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSession("c")
	if got.CLISessionID != "conv-1" || got.CLISessionState != CLIKnown || !got.IsChat() {
		t.Fatalf("chat row %+v", got)
	}
	// A database from before chats existed has no kind column: every row in
	// it is a terminal.
	for _, q := range []string{`ALTER TABLE sessions DROP COLUMN kind`, `PRAGMA user_version = 9`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err = st.GetSession("t")
	if err != nil || got.Kind != KindTerminal {
		t.Fatalf("migrated row %+v %v", got, err)
	}
}
