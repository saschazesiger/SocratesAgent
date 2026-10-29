//go:build !windows

package opencode

import (
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// Every prompt carries the instructions of the model it runs on as its
// system, which OpenCode adds to its own: a switch needs nothing more, and a
// model without instructions sends none.
func TestAPromptCarriesItsModelsInstructions(t *testing.T) {
	a := &Adapter{spec: agent.StartSpec{Instructions: map[string]string{"anthropic/claude-sonnet-4-5": "NOTE-SONNET"}}}
	j := job{kind: jobPrompt, msg: agent.Outgoing{MessageID: "m1", Text: "hello"}}
	system := func(model string) any {
		a.model = model
		body, err := a.bodyLocked(j, "msg_1")
		if err != nil {
			t.Fatal(err)
		}
		return body.(map[string]any)["system"]
	}
	want := "The person's instructions for anthropic/claude-sonnet-4-5, the model this chat runs on:\n\nNOTE-SONNET"
	if got := system("anthropic/claude-sonnet-4-5"); got != want {
		t.Fatalf("system = %q, want %q", got, want)
	}
	if got := system("openai/gpt-5.5"); got != nil {
		t.Fatalf("a model without instructions sent %q", got)
	}
}

// A test session's prompts carry the catalog's instructions after the
// model's, and alone when the model has none.
func TestATestSessionsPromptCarriesTheCatalogsInstructions(t *testing.T) {
	a := &Adapter{spec: agent.StartSpec{Instructions: map[string]string{"anthropic/claude-sonnet-4-5": "NOTE-SONNET"},
		TestInstructions: "CATALOG-NOTES"}}
	j := job{kind: jobPrompt, msg: agent.Outgoing{MessageID: "m1", Text: "hello"}}
	system := func(model string) any {
		a.model = model
		body, err := a.bodyLocked(j, "msg_1")
		if err != nil {
			t.Fatal(err)
		}
		return body.(map[string]any)["system"]
	}
	want := agent.ModelPrompt("anthropic/claude-sonnet-4-5", "NOTE-SONNET") + "\n\nCATALOG-NOTES"
	if got := system("anthropic/claude-sonnet-4-5"); got != want {
		t.Fatalf("system = %q, want %q", got, want)
	}
	if got := system("openai/gpt-5.5"); got != "CATALOG-NOTES" {
		t.Fatalf("without the model's instructions: %q", got)
	}
}
