package config

import "testing"

// A busy pattern now comes from the app itself, so the only thing left to
// guard is the failure mode: a pattern that does not compile must not stop a
// program from ever being driven.
func TestAnUnusableBusyPatternNeverMatches(t *testing.T) {
	skill := Skill{ID: "broken", BusyPattern: "esc to (interrupt"}
	if skill.BusyText() != "esc to (interrupt" {
		t.Fatalf("the pattern was rewritten to %q", skill.BusyText())
	}
	if skill.Busy() != nil {
		t.Fatal("an uncompilable pattern was used anyway")
	}
}

// Every shipped skill says when it is working, and holds the reply until it
// has stopped. Both are what keeps Socrates from answering mid task.
func TestShippedSkillsSayWhenTheyAreBusy(t *testing.T) {
	for _, p := range Presets() {
		if p.BusyText() == "" {
			t.Errorf("%s has no busy pattern", p.ID)
		}
		if p.Busy() == nil {
			t.Errorf("%s has a busy pattern that does not compile: %q", p.ID, p.BusyText())
		}
		if !p.HoldsReply() {
			t.Errorf("%s does not hold the reply while it is working", p.ID)
		}
	}
}
