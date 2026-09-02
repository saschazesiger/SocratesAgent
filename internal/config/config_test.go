package config

import "testing"

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
	if s.Agent.WorkspaceRoot == "" {
		t.Error("workspace root was left empty")
	}
	// Reading an answer out loud takes no configuration at all now, so the
	// voice section is a language and a pace and nothing else.
	if s.Voice.Language != DefaultLanguage || s.Voice.TTSRate != 1 {
		t.Errorf("voice defaults = %#v", s.Voice)
	}
}

// A fresh installation may talk to all three programs. Whether one of them is
// actually on this machine is not a setting - /api/agents reports that - so
// the default is on and the dashboard is where it is switched off.
func TestAgentsDefaultToEnabled(t *testing.T) {
	s := Default()
	for _, id := range []string{"claude", "codex", "opencode"} {
		entry, ok := s.Agents.Entry(id)
		if !ok {
			t.Fatalf("%s is not a known agent", id)
		}
		if !entry.Enabled {
			t.Errorf("%s is off by default", id)
		}
		if entry.Binary != "" || len(entry.ExtraArgs) != 0 {
			t.Errorf("%s ships with an override: %#v", id, entry)
		}
	}
	if _, ok := s.Agents.Entry("invented"); ok {
		t.Error("an agent nobody ships was reported as known")
	}
	// Normalize only tidies this section - it does not fill it in. An upgraded
	// installation keeps all three enabled anyway, because the server decodes
	// its stored document into config.Default() rather than into a zero value.
	var blank Settings
	blank.Normalize()
	blank.Agents.Claude.Binary = "  /usr/local/bin/claude  "
	blank.Agents.Claude.ExtraArgs = []string{" --debug ", "  "}
	blank.Normalize()
	if blank.Agents.Claude.Binary != "/usr/local/bin/claude" {
		t.Errorf("binary was not trimmed: %q", blank.Agents.Claude.Binary)
	}
	if len(blank.Agents.Claude.ExtraArgs) != 1 || blank.Agents.Claude.ExtraArgs[0] != "--debug" {
		t.Errorf("extra args were not trimmed: %#v", blank.Agents.Claude.ExtraArgs)
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
// blanks, and maps every effort onto a level the agents understand.
func TestModelPicksAreNormalized(t *testing.T) {
	var s Settings
	s.Normalize()
	s.Agents.OpenCode.Models = []ModelPick{
		{ID: " openai|gpt-5.6 ", Effort: "HIGH"},
		{ID: "", Effort: "low"},
		{ID: "opencode|big-pickle", Effort: "bogus"},
		{ID: "openai|gpt-5.6", Effort: "low"},
	}
	s.Normalize()
	got := s.Agents.OpenCode.Models
	if len(got) != 2 {
		t.Fatalf("picks = %#v", got)
	}
	if got[0] != (ModelPick{ID: "openai|gpt-5.6", Effort: EffortHigh}) {
		t.Errorf("first pick = %#v", got[0])
	}
	if got[1] != (ModelPick{ID: "opencode|big-pickle", Effort: EffortDefault}) {
		t.Errorf("second pick = %#v", got[1])
	}
	if s.Agents.Claude.Models != nil {
		t.Errorf("an empty list became %#v", s.Agents.Claude.Models)
	}
}
