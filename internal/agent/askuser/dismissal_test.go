package askuser

import (
	"encoding/json"
	"strings"
	"testing"
)

// The answer that releases a call the person wrote past skips every question
// and says why, and never approves a plan.
func TestDismissalSkipsEveryQuestionAndKeepsPlanning(t *testing.T) {
	payload := json.RawMessage(`{"questions":[{"id":"db","question":"Which database?","kind":"single",` +
		`"options":[{"id":"pg","label":"PostgreSQL"}]},{"id":"ok","question":"Go?","kind":"confirm"}]}`)
	got := ToolResult(Decode(payload), Dismissal(payload))
	if strings.Count(got, `"skipped":true`) != 2 || !strings.Contains(got, "wrote a new message instead") {
		t.Fatalf("the tool result %q", got)
	}
	plan := Dismissal(json.RawMessage(`{"kind":"plan","plan":"Do it."}`))
	if text := MessageText(json.RawMessage(`{"kind":"plan"}`), plan); !strings.Contains(text, "keep planning") {
		t.Fatalf("the plan's dismissal reads %q", text)
	}
}
