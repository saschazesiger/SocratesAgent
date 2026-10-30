package agent

import (
	"strings"
	"testing"
)

// A conversation that keeps its instructions (Codex) is told about a change
// once, across starts: a switch of model, instructions changed or withdrawn
// in the dashboard, and nothing when they are what it has.
func TestModelNotesAreKeptAcrossStarts(t *testing.T) {
	dir := t.TempDir()
	all := map[string]string{"a": "NOTE-A", "b": "NOTE-B"}
	n := LoadModelNotes(StartSpec{StateDir: dir, Instructions: all})
	if got := n.Fresh("a"); got != "The person's instructions for a, the model this chat runs on:\n\nNOTE-A" {
		t.Fatalf("fresh = %q", got)
	}
	if got := n.Switch("a"); got != "" {
		t.Fatalf("the same model again = %q", got)
	}
	if got := n.Switch(""); got != "" {
		t.Fatalf("the CLI's own model = %q", got)
	}

	resumed := func(all map[string]string) *ModelNotes {
		return LoadModelNotes(StartSpec{StateDir: dir, ResumeID: "t1", Instructions: all})
	}
	if got := resumed(all).Switch("a"); got != "" {
		t.Fatalf("a resume on the model it was told about = %q", got)
	}
	if got := resumed(all).Switch("b"); !strings.Contains(got, "This chat now runs on b. The person's instructions for a no longer apply. "+
		"The person's instructions for b:\n\nNOTE-B") {
		t.Fatalf("switched = %q", got)
	}
	changed := map[string]string{"b": "NOTE-B2"}
	if got := resumed(changed).Switch("b"); !strings.Contains(got, "The person changed their instructions for b") ||
		!strings.Contains(got, "NOTE-B2") {
		t.Fatalf("changed = %q", got)
	}
	if got := resumed(nil).Switch("b"); !strings.Contains(got, "The person withdrew their instructions for b") {
		t.Fatalf("withdrawn = %q", got)
	}
	if got := resumed(nil).Switch("c"); got != "" {
		t.Fatalf("from none to none = %q", got)
	}
	if got := LoadModelNotes(StartSpec{StateDir: dir, Instructions: all}).Switch("a"); !strings.Contains(got, "NOTE-A") {
		t.Fatalf("a new conversation took what an old one was told: %q", got)
	}
}

// The note before a message comes off again when a transcript is read back,
// and a message that only looks like one stays as it is.
func TestStripModelNote(t *testing.T) {
	n := NewModelNotes(StartSpec{Model: "a", Instructions: map[string]string{"b": "NOTE-B"}})
	if got := StripModelNote(WithNote(n.Switch("b"), "hello\nthere")); got != "hello\nthere" {
		t.Fatalf("stripped = %q", got)
	}
	// Socrates' own note comes off as well, after the model's or alone.
	if got := StripModelNote(WithNote(n.Switch("a"), AgentText("From the chat “API”.", "go on"))); got != "go on" {
		t.Fatalf("stripped both = %q", got)
	}
	if got := StripModelNote(AgentText("From the chat “API”.", "go on")); got != "go on" {
		t.Fatalf("stripped Socrates' note = %q", got)
	}
	if got := AgentText("  ", "go on"); got != "go on" {
		t.Fatalf("an empty note = %q", got)
	}
	for _, text := range []string{"hello", "<model-instructions> never closed", "x <model-instructions></model-instructions>",
		"<socrates-note> never closed"} {
		if got := StripModelNote(text); got != text {
			t.Errorf("%q became %q", text, got)
		}
	}
}
