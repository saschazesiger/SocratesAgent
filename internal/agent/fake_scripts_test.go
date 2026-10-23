//go:build !windows

package agent

import (
	"encoding/json"
	"testing"
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
	var commands []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(fakeCommands, &commands); err != nil || len(commands) == 0 {
		t.Fatalf("the commands: %v %v", commands, err)
	}
	for done := 0; done <= 3; done++ {
		if !json.Valid(todoContent(done)) {
			t.Fatalf("the task list after %d", done)
		}
	}
}

// A commands event is kept by the projection, the last one whole.
func TestTheProjectionKeepsTheCommands(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeCommands, Content: json.RawMessage(`[{"name":"a"}]`)},
		{Seq: 2, Type: TypeCommands, Content: fakeCommands},
	})
	if string(p.Commands) != string(fakeCommands) {
		t.Fatalf("commands %s", p.Commands)
	}
}
