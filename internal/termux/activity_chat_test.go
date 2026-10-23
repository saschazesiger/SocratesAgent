package termux

import (
	"context"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// A chat has no pane: its activity is reported exactly, committed without a
// debounce, and kept through ticks that know nothing about it - with the
// unread rule, the row moving and the filing of the terminals.
func TestReportedChatActivityFollowsTheTerminalRules(t *testing.T) {
	b := newBench(t)
	row := &store.Session{ID: "chat1", Harness: "fake", Workdir: b.dataDir, WorkdirMode: store.WorkdirCustom,
		Kind: store.KindChat, State: store.StateRunning}
	if err := b.st.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if err := b.st.SetSessionStatus(row.ID, store.StatusPending); err != nil {
		t.Fatal(err)
	}
	b.m.ReportActivity(row.ID, StateIdle, "", false)
	if a := b.m.ActivityOf(row.ID); a.State != StateIdle || a.Unread || a.Source != sourceExact {
		t.Fatalf("a new chat %+v", a)
	}
	before, _ := b.st.GetSession(row.ID)
	b.now = b.now.Add(time.Minute)
	b.m.ReportActivity(row.ID, StateBusy, "", true)
	if a := b.m.ActivityOf(row.ID); a.State != StateBusy {
		t.Fatalf("busy was not committed at once: %+v", a)
	}
	got, _ := b.st.GetSession(row.ID)
	if got.Status != store.StatusInProgress || got.ActiveAt == before.ActiveAt {
		t.Fatalf("a chat that starts working must be in progress and move: %+v", got)
	}
	rows, _ := b.st.ListSessions()
	b.m.act.apply(context.Background(), rows, map[string]paneLine{}, b.now)
	if a := b.m.ActivityOf(row.ID); a.State != StateBusy {
		t.Fatalf("a tick without a pane changed the chat: %+v", a)
	}
	b.m.ReportActivity(row.ID, StateIdle, "", true)
	if a := b.m.ActivityOf(row.ID); a.State != StateIdle || !a.Unread {
		t.Fatalf("a finished turn nobody saw must be unread: %+v", a)
	}
	b.m.ReportActivity(row.ID, StateWaiting, "question", true)
	if a := b.m.ActivityOf(row.ID); a.State != StateWaiting || a.Note != "question" {
		t.Fatalf("waiting %+v", a)
	}
	if _, ok := b.m.Activities()[row.ID]; !ok {
		t.Fatal("the chat is missing from the activity map")
	}
	// Adopt's reconciliation must never turn a chat into a terminal to resume.
	b.m.markLost(rows)
	if got, _ := b.st.GetSession(row.ID); got.State != store.StateRunning {
		t.Fatalf("markLost touched the chat: %s", got.State)
	}
}
