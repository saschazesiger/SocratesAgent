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
