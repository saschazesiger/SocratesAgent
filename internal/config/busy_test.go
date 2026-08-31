package config

import (
	"encoding/json"
	"testing"
)

// An installation that predates busy patterns has no busy_pattern key at all.
// Its skills must gain the pattern of the preset they plainly are, or the
// orchestrator stays blind to "esc to interrupt" on every existing install.
func TestNormalizeBackfillsTheBusyPatternOfAPreset(t *testing.T) {
	var settings Settings
	raw := `{"skills":[
		{"id":"claude","name":"Claude Code","enabled":true,"command":"claude"},
		{"id":"my-agent","name":"Mine","enabled":true,"command":"/opt/bin/opencode"},
		{"id":"homegrown","name":"Homegrown","enabled":true,"command":"my-tui"}
	]}`
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	settings.Normalize()

	claude, _ := PresetByID("claude")
	opencode, _ := PresetByID("opencode")
	for _, want := range []struct{ id, pattern string }{
		{"claude", claude.BusyText()},
		// Recognised by the program it runs, not by its id.
		{"my-agent", opencode.BusyText()},
		// Nothing to recognise: a skill someone wrote themselves keeps none.
		{"homegrown", ""},
	} {
		skill, ok := settings.Skill(want.id)
		if !ok {
			t.Fatalf("skill %s disappeared", want.id)
		}
		if skill.BusyText() != want.pattern {
			t.Errorf("skill %s busy pattern = %q, want %q", want.id, skill.BusyText(), want.pattern)
		}
	}
}

// Clearing the field in the dashboard is a decision, and it has to survive
// every save. That is the difference between an absent key and an empty one.
func TestNormalizeKeepsAClearedBusyPattern(t *testing.T) {
	var settings Settings
	raw := `{"skills":[{"id":"claude","name":"Claude Code","enabled":true,"command":"claude","busy_pattern":""}]}`
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	settings.Normalize()
	skill, _ := settings.Skill("claude")
	if skill.BusyText() != "" {
		t.Fatalf("a cleared busy pattern came back as %q", skill.BusyText())
	}
	if skill.Busy() != nil {
		t.Fatal("a cleared busy pattern still compiled to something")
	}
	// And it survives the round trip through the stored document.
	stored, err := json.Marshal(skill)
	if err != nil {
		t.Fatal(err)
	}
	var again Skill
	if err := json.Unmarshal(stored, &again); err != nil {
		t.Fatal(err)
	}
	if again.BusyPattern == nil {
		t.Fatal("the cleared field was dropped from the saved settings, so the preset would come back")
	}
}

// A typo must not be swallowed - the user has to see what they typed in order
// to fix it - and it must not match anything either.
func TestAnUnusableBusyPatternIsKeptButNeverMatches(t *testing.T) {
	var settings Settings
	raw := `{"skills":[{"id":"claude","name":"Claude Code","enabled":true,"command":"claude","busy_pattern":"esc to (interrupt"}]}`
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	settings.Normalize()
	skill, _ := settings.Skill("claude")
	if skill.BusyText() != "esc to (interrupt" {
		t.Fatalf("the pattern the user typed was changed to %q", skill.BusyText())
	}
	if skill.Busy() != nil {
		t.Fatal("an uncompilable pattern was used anyway")
	}
}

// Not saying anything about holding replies means "hold them", because a
// settings document written before the field existed must get the safe answer.
func TestHoldReplyWhileBusyDefaultsToOn(t *testing.T) {
	var settings Settings
	raw := `{"skills":[
		{"id":"claude","name":"Claude Code","enabled":true,"command":"claude"},
		{"id":"loose","name":"Loose","enabled":true,"command":"loose","hold_reply_while_busy":false}
	]}`
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	settings.Normalize()
	if skill, _ := settings.Skill("claude"); !skill.HoldsReply() {
		t.Error("a skill that says nothing does not hold replies")
	}
	if skill, _ := settings.Skill("loose"); skill.HoldsReply() {
		t.Error("a skill that switched holding off still holds replies")
	}
	if (Skill{}).HoldsReply() != true {
		t.Error("an unset skill does not default to holding")
	}
}
