package config

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
)

// ids returns the stored skill ids in order, which is the order the dashboard
// lists them in.
func ids(s Settings) []string {
	out := []string{}
	for _, sk := range s.Skills {
		out = append(out, sk.ID)
	}
	return out
}

// A fresh installation has exactly the shipped skills, in the shipped order,
// with the shipped enabled flags and nothing else stored.
func TestFreshSettingsStoreOnlyTheDecisions(t *testing.T) {
	s := Default()
	s.Normalize()
	if got, want := strings.Join(ids(s), ","), "claude,codex,opencode"; got != want {
		t.Fatalf("skills = %s, want %s", got, want)
	}
	for i, sk := range s.Skills {
		preset := Presets()[i]
		if sk.Enabled != preset.Enabled {
			t.Errorf("%s enabled = %v, want %v", sk.ID, sk.Enabled, preset.Enabled)
		}
		if sk.Description != "" {
			t.Errorf("%s stored a copy of the shipped description: %q", sk.ID, sk.Description)
		}
	}
	raw, err := json.Marshal(s.Skills)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "command") || strings.Contains(string(raw), "startup") {
		t.Fatalf("the settings document still stores how a skill is run: %s", raw)
	}
}

// Turning a skill on and off is the first of the two decisions, and it has to
// survive the round trip through the stored document.
func TestEnabledRoundTrips(t *testing.T) {
	s := Default()
	for i := range s.Skills {
		s.Skills[i].Enabled = s.Skills[i].ID == "opencode"
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Settings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()
	enabled := back.EnabledSkills()
	if len(enabled) != 1 || enabled[0].ID != "opencode" {
		t.Fatalf("enabled skills = %#v", enabled)
	}
	if claude, _ := back.Skill("claude"); claude.Enabled {
		t.Error("a skill that was switched off came back on")
	}
}

// The second decision: what Socrates should use the program for. A wording of
// the user's own is kept; the shipped wording is not stored, so improvements
// to it arrive with the next version.
func TestDescriptionOverrideIsKeptOnlyWhenItDiffers(t *testing.T) {
	claude, _ := PresetByID("claude")
	s := Default()
	for i := range s.Skills {
		switch s.Skills[i].ID {
		case "claude":
			s.Skills[i].Description = "  " + claude.Description + "  "
		case "codex":
			s.Skills[i].Description = "only for reading, never for writing"
		}
	}
	s.Normalize()

	stored := map[string]SkillSetting{}
	for _, sk := range s.Skills {
		stored[sk.ID] = sk
	}
	if stored["claude"].Description != "" {
		t.Errorf("the shipped wording was stored anyway: %q", stored["claude"].Description)
	}
	if stored["codex"].Description != "only for reading, never for writing" {
		t.Errorf("the user's own wording was lost: %q", stored["codex"].Description)
	}

	merged, _ := s.Skill("claude")
	if merged.Description != claude.Description {
		t.Errorf("an empty override should read as the shipped wording, got %q", merged.Description)
	}
	if merged, _ := s.Skill("codex"); merged.Description != "only for reading, never for writing" {
		t.Errorf("the override did not reach the merged skill: %q", merged.Description)
	}
}

// The upside of predefining skills: an upgrade that changes a command, a
// pattern or a manual reaches an installation that was set up long ago.
func TestTheAppWinsOverAStoredCopyOfAnOlderVersion(t *testing.T) {
	// A settings document from the version where a skill was stored whole,
	// with the wording and the command line of that older release.
	raw := `{"skills":[
		{"id":"claude","name":"Claude Code","enabled":true,"preset":"claude",
		 "description":"Best for writing code.","command":"claude-old","args":["--legacy"],
		 "env":["OLD=1"],"busy_pattern":"working…","startup":"stale instructions",
		 "idle_seconds":90,"timeout_seconds":60,"cols":300},
		{"id":"codex","name":"Codex","enabled":false,"preset":"codex","command":"codex"}
	]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()

	if got, want := strings.Join(ids(s), ","), "claude,codex,opencode"; got != want {
		t.Fatalf("skills = %s, want %s", got, want)
	}
	preset, _ := PresetByID("claude")
	claude, ok := s.Skill("claude")
	if !ok {
		t.Fatal("claude disappeared")
	}
	if !claude.Enabled {
		t.Error("the user's own choice to enable it was lost")
	}
	if claude.Description != "Best for writing code." {
		t.Errorf("the user's own wording was lost: %q", claude.Description)
	}
	if claude.Command != preset.Command || claude.BusyText() != preset.BusyText() ||
		claude.Startup != preset.Startup || claude.IdleSeconds != preset.IdleSeconds ||
		len(claude.Env) != len(preset.Env) {
		t.Errorf("a stored copy of an older version survived: %#v", claude)
	}
	if codex, _ := s.Skill("codex"); codex.Enabled {
		t.Error("a skill that was switched off came back on")
	}
	// A skill this installation had never seen arrives with its own default.
	if opencode, _ := s.Skill("opencode"); opencode.Enabled {
		t.Error("opencode ships switched off")
	}
}

// Skills someone wrote themselves in the dashboard have nothing left to run
// them, so they are dropped rather than kept as a broken entry.
func TestCustomSkillsAreDropped(t *testing.T) {
	raw := `{"skills":[
		{"id":"claude","enabled":true,"command":"claude"},
		{"id":"my-agent","name":"Mine","enabled":true,"command":"/opt/bin/aider"},
		{"id":"homegrown","name":"Homegrown","enabled":true,"command":"my-tui"}
	]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if got, want := strings.Join(ids(s), ","), "claude,codex,opencode"; got != want {
		t.Fatalf("skills = %s, want %s", got, want)
	}
	if _, ok := s.Skill("my-agent"); ok {
		t.Error("a skill of the user's own survived")
	}
}

// A renamed copy of a shipped program is that program: /opt/bin/claude is
// still Claude Code, and the decisions made about it are kept.
func TestARenamedShippedProgramIsRecognised(t *testing.T) {
	raw := `{"skills":[{"id":"implementer","name":"Implementer","enabled":true,
		"command":"/opt/bin/claude","description":"my own words"}]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	claude, ok := s.Skill("claude")
	if !ok {
		t.Fatal("a renamed Claude Code was not recognised")
	}
	if !claude.Enabled || claude.Description != "my own words" {
		t.Errorf("the decisions about it were lost: %#v", claude)
	}
	if _, ok := s.Skill("implementer"); ok {
		t.Error("the old id is still in the list")
	}
}

// An installation from before skills existed keeps the programs it had turned
// on, and gains everything the app now knows about driving them.
func TestNormalizeMigratesOldTools(t *testing.T) {
	raw := `{"tools":[
		{"id":"claude","name":"Claude Code","enabled":true,"command":"claude","description":"my own words",
		 "driving":"press enter twice","skip_permissions":true,"timeout_seconds":900},
		{"id":"my-own-tui","name":"Mine","enabled":true,"command":"my-tui","driving":"just type"}
	]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	// What was dropped has to be in the log, by name: it is the only notice
	// the user gets that a program of their own is gone.
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	s.Normalize()
	if !strings.Contains(logged.String(), "my-own-tui") {
		t.Errorf("the dropped skill was not named in the log: %q", logged.String())
	}
	if s.Tools != nil {
		t.Error("the legacy list should be cleared after migration")
	}
	if got, want := strings.Join(ids(s), ","), "claude,codex,opencode"; got != want {
		t.Fatalf("skills = %s, want %s", got, want)
	}
	claude, _ := s.Skill("claude")
	if !claude.Enabled || claude.Description != "my own words" {
		t.Errorf("the user's own settings were lost: %#v", claude)
	}
	if claude.Startup == "" || claude.Answering == "" || claude.TimeoutSeconds != 1800 {
		t.Error("the migrated skill did not pick up how the app drives the program")
	}
	if _, ok := s.Skill("my-own-tui"); ok {
		t.Error("a program of the user's own survived the migration")
	}
	// Codex was not in the old list, so it arrives with its shipped default.
	if codex, _ := s.Skill("codex"); !codex.Enabled {
		t.Error("codex ships switched on")
	}
}

// An installation from before the terminal rework has to keep its agents.
func TestNormalizeMigratesOldBackends(t *testing.T) {
	raw := `{"backends":[
		{"id":"claude","kind":"claude","name":"Claude Code","enabled":true,
		 "description":"my own description","command":"claude","approval":"auto","timeout_seconds":900},
		{"id":"codex","kind":"codex","name":"Codex","enabled":false,"command":"codex","approval":"ask"}
	]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if s.Backends != nil {
		t.Error("the legacy list should be cleared after migration")
	}
	claude, _ := s.Skill("claude")
	if !claude.Enabled || claude.Description != "my own description" {
		t.Errorf("the decisions about claude were lost: %#v", claude)
	}
	if !claude.SkipPermissions || len(claude.SkipArgs) == 0 || claude.Startup == "" {
		t.Error("the migrated skill did not pick up how the app drives the program")
	}
	if codex, _ := s.Skill("codex"); codex.Enabled {
		t.Error("an agent that was switched off came back on")
	}
}

// Two entries for the same program, which a duplicated id in an older document
// could produce, collapse into the one entry the app ships.
func TestDuplicateEntriesCollapse(t *testing.T) {
	raw := `{"skills":[
		{"id":"claude","enabled":true,"command":"claude"},
		{"id":"claude","enabled":false,"command":"claude"}
	]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if got, want := strings.Join(ids(s), ","), "claude,codex,opencode"; got != want {
		t.Fatalf("skills = %s, want %s", got, want)
	}
	if claude, _ := s.Skill("claude"); !claude.Enabled {
		t.Error("the first entry should win")
	}
}

// An emptied list is no longer a decision: skills are predefined, and turning
// them all off is what "none of them" means now.
func TestAnEmptyListComesBackAsTheShippedSkills(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"skills":[]}`), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if len(s.Skills) != len(Presets()) {
		t.Fatalf("skills = %#v", s.Skills)
	}
}

// The third decision: which models a skill may be started on. A list of the
// user's own is kept; a copy of the shipped list is not stored, so a better
// list in a later version reaches an installation that never touched it.
func TestModelListOverrideIsKeptOnlyWhenItDiffers(t *testing.T) {
	claude, _ := PresetByID("claude")
	mine := []ModelChoice{
		{ID: "opus", Effort: EffortHigh, UseWhen: "everything, I am not counting"},
	}
	s := Default()
	for i := range s.Skills {
		switch s.Skills[i].ID {
		case "claude":
			// The dashboard sends the shipped list back when nobody touched it.
			s.Skills[i].Models = append([]ModelChoice(nil), claude.Models...)
		case "codex":
			s.Skills[i].Models = mine
		}
	}
	s.Normalize()

	stored := map[string]SkillSetting{}
	for _, sk := range s.Skills {
		stored[sk.ID] = sk
	}
	if stored["claude"].Models != nil {
		t.Errorf("the shipped list was stored anyway: %#v", stored["claude"].Models)
	}
	if !sameModels(stored["codex"].Models, mine) {
		t.Errorf("the user's own list was lost: %#v", stored["codex"].Models)
	}

	merged, _ := s.Skill("claude")
	if !sameModels(merged.Models, claude.Models) {
		t.Errorf("an empty override should read as the shipped list, got %#v", merged.Models)
	}
	if merged, _ := s.Skill("codex"); !sameModels(merged.Models, mine) {
		t.Errorf("the override did not reach the merged skill: %#v", merged.Models)
	}
}

// The list is what the orchestrator picks from, so an entry it could never ask
// for by name has no business being in it.
func TestModelListIsNormalized(t *testing.T) {
	s := Default()
	for i := range s.Skills {
		if s.Skills[i].ID != "claude" {
			continue
		}
		s.Skills[i].Models = []ModelChoice{
			{ID: "  sonnet  ", Effort: " HIGH ", UseWhen: "  the everyday one  "},
			{ID: "", Effort: "low", UseWhen: "nameless"},
			{ID: "   ", UseWhen: "also nameless"},
			{ID: "sonnet", Effort: "low", UseWhen: "a second entry under the same id"},
			{ID: "haiku", Effort: "ludicrous", UseWhen: "an effort nobody has"},
		}
	}
	s.Normalize()

	claude, _ := s.Skill("claude")
	want := []ModelChoice{
		{ID: "sonnet", Effort: EffortHigh, UseWhen: "the everyday one"},
		{ID: "haiku", Effort: EffortDefault, UseWhen: "an effort nobody has"},
	}
	if !sameModels(claude.Models, want) {
		t.Fatalf("models = %#v, want %#v", claude.Models, want)
	}
	if got, ok := claude.ModelByID("SONNET"); !ok || got.Effort != EffortHigh {
		t.Errorf("a model should be findable by its id whatever the case: %#v", got)
	}
	if _, ok := claude.ModelByID("opus"); ok {
		t.Error("a model that is not on the list was found anyway")
	}
	if def := claude.DefaultModel(); def.ID != "sonnet" {
		t.Errorf("the first entry is the default, got %#v", def)
	}
}

// A settings document written before skills had model lists must not lose its
// decisions, and must arrive with the models the app now ships.
func TestASettingsDocumentWithoutModelsGetsTheShippedList(t *testing.T) {
	raw := `{"skills":[
		{"id":"claude","enabled":true,"description":"my own words"},
		{"id":"codex","enabled":false}
	]}`
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	preset, _ := PresetByID("claude")
	claude, _ := s.Skill("claude")
	if claude.Description != "my own words" {
		t.Errorf("the user's own wording was lost: %q", claude.Description)
	}
	if !sameModels(claude.Models, preset.Models) {
		t.Errorf("models = %#v, want the shipped list %#v", claude.Models, preset.Models)
	}
	if len(claude.Models) == 0 {
		t.Fatal("the shipped claude preset has no models at all")
	}
}

// A user's list survives being written, read back and normalized again, which
// is the round trip every save makes.
func TestModelListRoundTrips(t *testing.T) {
	mine := []ModelChoice{
		{ID: "haiku", Effort: EffortLow, UseWhen: "small chores"},
		{ID: "opus", Effort: EffortHigh, UseWhen: "the hard ones"},
	}
	s := Default()
	for i := range s.Skills {
		if s.Skills[i].ID == "claude" {
			s.Skills[i].Models = mine
		}
	}
	s.Normalize()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Settings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()
	claude, _ := back.Skill("claude")
	if !sameModels(claude.Models, mine) {
		t.Fatalf("models did not survive the round trip: %#v", claude.Models)
	}
	if def := claude.DefaultModel(); def.ID != "haiku" || def.Effort != EffortLow {
		t.Errorf("the default should be the first entry, got %#v", def)
	}
}

// Every shipped skill has to say what it does with a model and an effort, and
// the flags it says it uses have to have somewhere to put the value.
func TestPresetsRecordHowModelAndEffortAreApplied(t *testing.T) {
	for _, p := range Presets() {
		if strings.TrimSpace(p.Applying) == "" {
			t.Errorf("%s does not say how a model reaches the program", p.ID)
		}
		if len(p.Models) == 0 {
			t.Errorf("%s ships without a single model to choose from", p.ID)
		}
		if !sameModels(p.Models, normalizeModels(p.Models)) {
			t.Errorf("%s ships a model list that normalization would change: %#v", p.ID, p.Models)
		}
		if len(p.ModelArgs) > 0 && !strings.Contains(strings.Join(p.ModelArgs, " "), "{model}") {
			t.Errorf("%s has model arguments with nowhere to put the id: %#v", p.ID, p.ModelArgs)
		}
		if len(p.EffortArgs) > 0 && !strings.Contains(strings.Join(p.EffortArgs, " "), "{effort}") {
			t.Errorf("%s has effort arguments with nowhere to put the level: %#v", p.ID, p.EffortArgs)
		}
		// A program with no effort mechanism has to say so, because the
		// dashboard offers the setting either way.
		if len(p.EffortArgs) == 0 && !strings.Contains(p.Applying, "no flag") {
			t.Errorf("%s applies no effort at launch and does not say so: %q", p.ID, p.Applying)
		}
	}
}

// The mechanism itself, per shipped program, exactly as the installed versions
// take it. These are the command lines a session is really started with.
func TestShippedApplicationMechanism(t *testing.T) {
	cases := []struct {
		id     string
		choice ModelChoice
		want   []string
	}{
		// claude 2.1.251: --model takes an alias or a full id, --effort takes
		// low, medium, high, xhigh or max.
		{"claude", ModelChoice{ID: "opus", Effort: EffortHigh}, []string{"--model", "opus", "--effort", "high"}},
		// codex 0.146.0: -m for the model, and the effort is a config key
		// overridden for this run, its value written as TOML.
		{"codex", ModelChoice{ID: "gpt-5.6-sol", Effort: EffortMedium},
			[]string{"-m", "gpt-5.6-sol", "-c", `model_reasoning_effort="medium"`}},
		// opencode 1.17.13: -m takes provider/model, and there is no
		// command line form for the effort at all.
		{"opencode", ModelChoice{ID: "opencode/big-pickle", Effort: EffortHigh},
			[]string{"-m", "opencode/big-pickle"}},
	}
	for _, tc := range cases {
		p, ok := PresetByID(tc.id)
		if !ok {
			t.Fatalf("no %s preset", tc.id)
		}
		got := p.LaunchArgs(tc.choice)
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("%s launch arguments = %#v, want %#v", tc.id, got, tc.want)
		}
		if args := p.LaunchArgs(ModelChoice{}); len(args) != 0 {
			t.Errorf("%s says something about models even when nobody chose one: %#v", tc.id, args)
		}
	}
}

// SwapPresets is what lets a test pretend the app ships something else. It has
// to put the real catalogue back.
func TestSwapPresets(t *testing.T) {
	restore := SwapPresets([]Skill{{ID: "fake", Name: "Fake", Enabled: true, Command: "fake"}})
	s := Default()
	s.Normalize()
	if got := strings.Join(ids(s), ","); got != "fake" {
		t.Fatalf("skills = %s", got)
	}
	restore()
	if _, ok := PresetByID("claude"); !ok {
		t.Fatal("the shipped catalogue did not come back")
	}
}
