//go:build !windows

package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The scripted agent's payloads are what the chat view's scenarios are built
// on: a payload that is not JSON would be journaled as it is and break every
// reader of the journal.
func TestFakePayloadsAreWholeJSON(t *testing.T) {
	var ask struct {
		Title     string `json:"title"`
		Questions []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Options []struct {
				ID string `json:"id"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(fakeAskPayload, &ask); err != nil {
		t.Fatalf("the ask payload: %v", err)
	}
	kinds := map[string]bool{}
	for _, q := range ask.Questions {
		kinds[q.Kind] = true
	}
	for _, kind := range []string{"single", "multi", "text", "confirm", "number"} {
		if !kinds[kind] {
			t.Errorf("the ask has no %s question", kind)
		}
	}
	var info struct {
		Commands []CommandInfo `json:"commands"`
	}
	if err := json.Unmarshal(fakeInfo, &info); err != nil || len(info.Commands) != 2 {
		t.Fatalf("the agent info: %v %v", info, err)
	}
	for done := 0; done <= 3; done++ {
		if !json.Valid(todoContent(done)) {
			t.Fatalf("the task list after %d", done)
		}
	}
}

// Socrates' note before a message is the agent's alone: the scripted agent
// echoes the person's text without it, reads its commands past it, and tells
// what the note of the message before was only when asked (/note).
func TestFakeEchoesTheTextWithoutItsNote(t *testing.T) {
	out := &emits{}
	f := &fakeAdapter{}
	note := "This message is from another agent, not from your person."
	spec := StartSpec{ChatID: "c1", Workdir: t.TempDir(), StateDir: t.TempDir(), Model: "fake-small",
		First: &Outgoing{MessageID: "m1", Text: AgentText(note, "please check the login route")}}
	if err := f.Start(t.Context(), spec, out); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	for i, text := range []string{AgentText("Other chats are active.", "/note"), "/note"} {
		if err := f.Send(t.Context(), Outgoing{MessageID: "m" + itoa(i+2), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	var replies []string
	for deadline := time.Now().Add(20 * time.Second); len(replies) < 3; {
		if time.Now().After(deadline) {
			t.Fatalf("the turns did not end: %q", replies)
		}
		time.Sleep(5 * time.Millisecond)
		out.mu.Lock()
		replies = replies[:0]
		for _, e := range out.events {
			if e.Type == TypePart && e.Kind == PartText && e.State == PartDone {
				replies = append(replies, e.Text)
			}
		}
		out.mu.Unlock()
	}
	if !strings.HasPrefix(replies[0], "you said: please check the login route (turn 1 of ") {
		t.Errorf("the echo: %q", replies[0])
	}
	if replies[1] != "note: "+note {
		t.Errorf("the note of the message before: %q", replies[1])
	}
	if replies[2] != "note: Other chats are active." {
		t.Errorf("the note of the /note before: %q", replies[2])
	}
	f.mu.Lock()
	user := f.history[0].User
	f.mu.Unlock()
	if user != "please check the login route" {
		t.Errorf("the transcript keeps the note: %q", user)
	}
}

// A message without a note has none to tell.
func TestFakeAgentNote(t *testing.T) {
	for text, want := range map[string]string{
		"hello":                        "",
		AgentText("a note", "hello"):   "a note",
		AgentText("", "hello"):         "",
		"<socrates-note> never closed": "",
	} {
		if got := fakeAgentNote(text); got != want {
			t.Errorf("fakeAgentNote(%q) = %q, want %q", text, got, want)
		}
	}
}
