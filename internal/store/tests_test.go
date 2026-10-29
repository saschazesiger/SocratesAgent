package store

import (
	"encoding/json"
	"testing"
)

// A test session is a chat its run owns: ListSessions leaves it out,
// ListAllSessions has it, and its reference is read from its options.
func TestTestSessionsAreLeftOutOfTheList(t *testing.T) {
	st := openTest(t)
	for _, s := range []*Session{
		{ID: "s-plain", Kind: KindChat, Harness: "claude", Workdir: "/w"},
		{ID: "s-empty", Kind: KindTerminal, Harness: "shell", Workdir: "/w", Options: json.RawMessage(`{"account":"a"}`)},
		{ID: "s-test", Kind: KindChat, Harness: "claude", Workdir: "/w", Options: json.RawMessage(`{"test":{"kind":"test-cell","run":"r"}}`)},
	} {
		if err := st.CreateSession(s); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSessions: %d rows", len(list))
	}
	for _, s := range list {
		if s.IsTest() {
			t.Fatalf("%s is a test session", s.ID)
		}
	}
	all, err := st.ListAllSessions()
	if err != nil || len(all) != 3 {
		t.Fatalf("ListAllSessions: %d rows, %v", len(all), err)
	}
	row, err := st.GetSession("s-test")
	if err != nil || !row.IsTest() || string(row.TestRef()) != `{"kind":"test-cell","run":"r"}` {
		t.Fatalf("test row: %v %s", err, row.TestRef())
	}
}
