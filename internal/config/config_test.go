package config

import (
	"encoding/json"
	"testing"
)

func TestNormalizeFillsDefaults(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.OpenRouter.BaseURL != DefaultOpenRouterBaseURL {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
	// OpenRouter transcribes and writes titles and does nothing else, so the
	// two models it still has are the two that must always be filled in.
	if s.OpenRouter.TranscribeModel != DefaultTranscribeModel {
		t.Errorf("transcribe model = %q", s.OpenRouter.TranscribeModel)
	}
	if s.OpenRouter.TitleModel != DefaultTitleModel {
		t.Errorf("title model = %q", s.OpenRouter.TitleModel)
	}
	if s.Workspace.Root == "" {
		t.Error("workspace root was left empty")
	}
	if s.Workspace.DefaultHarness != HarnessClaude || !IsHarness(s.Workspace.DefaultHarness) {
		t.Errorf("default harness = %q", s.Workspace.DefaultHarness)
	}
	// A terminal with no scrollback and no font size is a form that was never
	// filled in, not a terminal anybody wants.
	if s.Terminal.WindowSize != "manual" || s.Terminal.Scrollback == 0 || s.Terminal.FontSize == 0 || s.Terminal.HistoryLimit == 0 {
		t.Errorf("terminal defaults = %#v", s.Terminal)
	}
	// Reading an answer out loud takes no configuration at all now, so the
	// voice section is a language and a pace and nothing else.
	if s.Voice.Language != DefaultLanguage || s.Voice.TTSRate != 1 {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

// A fresh installation offers all four session kinds. Whether one of them is
// actually on this machine is not a setting - discovery reports that - so the
// default is on and the dashboard is where it is switched off.
func TestHarnessesDefaultToEnabled(t *testing.T) {
	s := Default()
	for _, id := range KnownHarnesses {
		entry, ok := s.Harnesses.Entry(id)
		if !ok {
			t.Fatalf("%s is not a known harness", id)
		}
		if !entry.Enabled {
			t.Errorf("%s is off by default", id)
		}
		if entry.Binary != "" || len(entry.Models) != 0 {
			t.Errorf("%s ships with an override: %#v", id, entry)
		}
	}
	if _, ok := s.Harnesses.Entry("invented"); ok {
		t.Error("a harness nobody ships was reported as known")
	}
	// What used to be a page of terminal switches is now fixed policy in the
	// launchers, so a fresh document says nothing about a model or an effort
	// either: a session that was started without a choice of its own gets the
	// program's own default.
	if s.Harnesses.Claude.DefaultModel != "" || s.Harnesses.Claude.DefaultEffort != "" ||
		s.Harnesses.Codex.DefaultModel != "" || s.Harnesses.Codex.DefaultEffort != "" ||
		s.Harnesses.OpenCode.DefaultModel != "" {
		t.Errorf("a fresh installation ships with a model or an effort: %#v", s.Harnesses)
	}
}

// Normalize only tidies the harness section - it does not fill it in. An
// upgraded installation keeps its switches anyway, because the server decodes
// its stored document into config.Default() rather than into a zero value.
func TestHarnessNormalizeTidiesWithoutFillingIn(t *testing.T) {
	var s Settings
	s.Normalize()
	if s.Harnesses.Claude.Enabled {
		t.Error("Normalize turned a switch on by itself")
	}
	s.Harnesses.Claude.Binary = "  /usr/local/bin/claude  "
	s.Harnesses.Claude.DefaultModel = "  sonnet  "
	s.Normalize()
	if s.Harnesses.Claude.Binary != "/usr/local/bin/claude" {
		t.Errorf("binary was not trimmed: %q", s.Harnesses.Claude.Binary)
	}
	if s.Harnesses.Claude.DefaultModel != "sonnet" {
		t.Errorf("model was not trimmed: %q", s.Harnesses.Claude.DefaultModel)
	}
}

// A stored document written before the dashboard was cut back is read without
// complaint: the settings that are now fixed policy decode into nothing at
// all, and what is left of the harness section survives untouched.
func TestOldHarnessKeysAreIgnored(t *testing.T) {
	old := []byte(`{"harnesses":{"claude":{"enabled":true,"binary":"/opt/claude",
		"default_model":"sonnet","default_effort":"high","permission_mode":"plan",
		"skip_permissions":"force","remote_control":true,"extra_args":["--verbose"],
		"extra_env":["FOO=bar"],"settings_overrides":"{not json"},
		"codex":{"enabled":true,"sandbox":"danger-full-access","approval":"never",
		"remote_addr":"ws://example.test","config_overrides":["not-an-assignment"]},
		"opencode":{"enabled":true,"auto":true,"permission_json":"[","tui_theme":"nord"},
		"shell":{"enabled":true,"login":false}}}`)
	s := Default()
	if err := json.Unmarshal(old, &s); err != nil {
		t.Fatalf("an old document would not decode: %v", err)
	}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatalf("an old document was refused: %v", err)
	}
	if s.Harnesses.Claude.Binary != "/opt/claude" || s.Harnesses.Claude.DefaultModel != "sonnet" ||
		s.Harnesses.Claude.DefaultEffort != "high" {
		t.Errorf("the settings that are still settings were lost: %#v", s.Harnesses.Claude)
	}
	if !s.Harnesses.Shell.Enabled || !s.Harnesses.OpenCode.Enabled {
		t.Errorf("a harness was turned off by the migration: %#v", s.Harnesses)
	}
}

// The closed lists in the dashboard are only closed if the server says so too:
// the API takes whatever is sent to it.
func TestHarnessClosedListsFallBack(t *testing.T) {
	s := Default()
	s.Harnesses.Codex.DefaultEffort = "ludicrous"
	s.Harnesses.Claude.DefaultEffort = "ludicrous"
	s.Terminal.WindowSize = "whatever"
	s.Normalize()
	// Codex does not validate the effort itself, so a typo would reach the
	// model as configuration and be quietly ignored.
	if s.Harnesses.Codex.DefaultEffort != "" {
		t.Errorf("codex effort = %q", s.Harnesses.Codex.DefaultEffort)
	}
	if s.Harnesses.Claude.DefaultEffort != "" {
		t.Errorf("claude effort = %q", s.Harnesses.Claude.DefaultEffort)
	}
	if s.Terminal.WindowSize != "manual" {
		t.Errorf("window size = %q", s.Terminal.WindowSize)
	}
}

// A preset with no path is not a place; one with no label is named after the
// directory it points at, so the picker never shows a blank row.
func TestWorkspacePresetsAreTidied(t *testing.T) {
	s := Default()
	s.Workspace.Presets = []PresetDir{
		{Path: " /srv/socrates "},
		{Label: "  ", Path: ""},
		{Label: "Repo", Path: "/srv/repo"},
		{Label: "Again", Path: "/srv/repo"},
	}
	s.Normalize()
	if len(s.Workspace.Presets) != 2 {
		t.Fatalf("presets = %#v", s.Workspace.Presets)
	}
	if s.Workspace.Presets[0] != (PresetDir{Label: "socrates", Path: "/srv/socrates"}) {
		t.Errorf("first preset = %#v", s.Workspace.Presets[0])
	}
	if s.Workspace.Presets[1].Label != "Repo" {
		t.Errorf("second preset = %#v", s.Workspace.Presets[1])
	}
}

func TestNormalizeTrimsBaseURL(t *testing.T) {
	s := Settings{}
	s.OpenRouter.BaseURL = "https://example.com/v1/"
	s.Normalize()
	if s.OpenRouter.BaseURL != "https://example.com/v1" {
		t.Errorf("base url = %q", s.OpenRouter.BaseURL)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Claude Code":  "claude-code",
		"  Weird__ID ": "weird-id",
		"???":          "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
func TestNormalizeDefaultsTheLanguage(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.Voice.Language != DefaultLanguage {
		t.Fatalf("language = %q", s.Voice.Language)
	}
	if LanguageName(s.Voice.Language) != "English" {
		t.Fatalf("default = %q", LanguageName(s.Voice.Language))
	}
}

// There are two languages and no third state, so everything else has to land
// on one of them.
func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"de":       LanguageDE,
		"DE":       LanguageDE,
		" de-CH ":  LanguageDE,
		"de_DE":    LanguageDE,
		"en":       LanguageEN,
		"en-GB":    LanguageEN,
		"":         DefaultLanguage,
		"fr":       DefaultLanguage,
		"deutsch":  DefaultLanguage,
		"-":        DefaultLanguage,
		"nonsense": DefaultLanguage,
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
		// Every language a settings document can hold has a name, because it
		// goes out to a model unchecked.
		if LanguageName(in) == "" {
			t.Errorf("%q has no name", in)
		}
	}
}

// The model short list keeps the order a person arranged, drops repeats and
// blanks, and maps every effort onto a level the harnesses understand.
func TestModelPicksAreNormalized(t *testing.T) {
	var s Settings
	s.Normalize()
	s.Harnesses.OpenCode.Models = []ModelPick{
		{ID: " openai|gpt-5.6 ", Effort: "HIGH"},
		{ID: "", Effort: "low"},
		{ID: "opencode|big-pickle", Effort: "bogus"},
		{ID: "openai|gpt-5.6", Effort: "low"},
	}
	s.Normalize()
	got := s.Harnesses.OpenCode.Models
	if len(got) != 2 {
		t.Fatalf("picks = %#v", got)
	}
	if got[0] != (ModelPick{ID: "openai|gpt-5.6", Effort: EffortHigh}) {
		t.Errorf("first pick = %#v", got[0])
	}
	if got[1] != (ModelPick{ID: "opencode|big-pickle", Effort: EffortDefault}) {
		t.Errorf("second pick = %#v", got[1])
	}
	if s.Harnesses.Claude.Models != nil {
		t.Errorf("an empty list became %#v", s.Harnesses.Claude.Models)
	}
}

// There is nothing left in the harness section for a person to type wrong.
// Every field is a switch, a path, a model id or a value Normalize closes onto
// a known list, so a fresh document and a filled-in one both validate.
func TestHarnessSettingsAlwaysValidate(t *testing.T) {
	s := Default()
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatalf("a fresh installation does not validate: %v", err)
	}
	s.Harnesses.Claude.Binary = "/usr/local/bin/claude"
	s.Harnesses.Claude.DefaultModel = "sonnet"
	s.Harnesses.Claude.DefaultEffort = "high"
	s.Harnesses.Codex.DefaultEffort = "xhigh"
	s.Harnesses.OpenCode.DefaultModel = "anthropic/claude-sonnet-4-5"
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Errorf("a filled-in document was refused: %v", err)
	}
}

// The Status button and the operator loop each pick their own model, and
// neither may be left without one: an empty field is a form nobody filled in,
// and a button that answers "pick a model first" is a setup step nobody asked
// for.
func TestAssistModelsAndAgentBoundsAreDefaulted(t *testing.T) {
	s := Settings{}
	s.Normalize()
	if s.OpenRouter.StatusModel != DefaultStatusModel {
		t.Errorf("status model = %q", s.OpenRouter.StatusModel)
	}
	if s.OpenRouter.AgentModel != DefaultAgentModel {
		t.Errorf("agent model = %q", s.OpenRouter.AgentModel)
	}
	// Zero steps is an empty number field, not a run that may take none.
	if s.Agent.MaxSteps != DefaultAgentMaxSteps {
		t.Errorf("max steps = %d", s.Agent.MaxSteps)
	}
	// A fresh installation is one person on one machine, so the operator may
	// drive a shell until somebody says otherwise.
	if !Default().Agent.AllowShell {
		t.Error("the agent may not drive a shell out of the box")
	}

	// A chosen model is kept, and the ceiling is a ceiling.
	s = Settings{}
	s.OpenRouter.StatusModel, s.OpenRouter.AgentModel = "someone/cheap", "someone/careful"
	s.Agent.MaxSteps = 400
	s.Normalize()
	if s.OpenRouter.StatusModel != "someone/cheap" || s.OpenRouter.AgentModel != "someone/careful" {
		t.Errorf("a chosen model was replaced: %#v", s.OpenRouter)
	}
	if s.Agent.MaxSteps != MaxAgentMaxSteps {
		t.Errorf("max steps = %d, want the ceiling", s.Agent.MaxSteps)
	}

	// And "one step, then stop" is a legitimate thing to ask for.
	s = Settings{}
	s.Agent.MaxSteps = 1
	s.Normalize()
	if s.Agent.MaxSteps != 1 {
		t.Errorf("max steps = %d", s.Agent.MaxSteps)
	}
}
