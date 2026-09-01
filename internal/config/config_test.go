package config

import (
	"encoding/json"
	"strings"
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

// The settings document of an installation that is upgraded carries every
// provider Socrates ever read an answer out loud with: an endpoint of its own,
// an OpenRouter voice model with its voice, and a whole Google section with a
// service account key in it. Not one of those keys has a field left to land
// in, and that is the point: encoding/json drops what it does not recognise,
// so the document loads, keeps the two settings that survived - the spoken
// language and the speaking rate - and is written back without the rest.
//
// This is the upgrade path, and it is the test that matters most here: a
// document that fails to load is an installation that will not start.
func TestNormalizeDropsEverythingTheOldProvidersNeeded(t *testing.T) {
	document := []byte(`{
		"openrouter": {"api_key": "sk-or", "chat_model": "anthropic/claude-sonnet-4.5"},
		"google": {"credentials": "{\"type\":\"service_account\"}", "base_url": "https://texttospeech.googleapis.com"},
		"voice": {
			"language": "de",
			"stt_prompt": "Transcribe it.",
			"tts_rate": 1.25,
			"speak_in_auto_mode": true,
			"tts_provider": "google",
			"tts_model": "deepgram/aura-2",
			"tts_voice": "aura-2-thalia-en",
			"google_voice": "de-DE-Chirp3-HD-Charon",
			"tts_base_url": "https://api.openai.com/v1",
			"tts_api_key": "sk-openai",
			"stt_provider": "endpoint",
			"stt_base_url": "https://api.openai.com/v1",
			"stt_api_key": "sk-openai",
			"stt_model": "whisper-1"
		}
	}`)
	var s Settings
	if err := json.Unmarshal(document, &s); err != nil {
		t.Fatalf("a settings document from the previous version does not load: %v", err)
	}
	s.Normalize()

	if s.Voice.Language != LanguageDE {
		t.Fatalf("language = %q", s.Voice.Language)
	}
	if s.Voice.TTSRate != 1.25 {
		t.Fatalf("rate = %v", s.Voice.TTSRate)
	}
	if s.Voice.STTPrompt != "Transcribe it." || s.OpenRouter.APIKey != "sk-or" {
		t.Fatalf("the rest of the document was not kept: %#v", s)
	}

	// Nothing of the old shape may reach the dashboard, or the database, again
	// - including the Google credentials, which are a private key nobody has a
	// use for any more.
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{
		"tts_provider", "tts_model", "tts_voice", "google_voice", `"google":`, "chat_model",
		"tts_base_url", "tts_api_key", "stt_provider", "stt_base_url", "stt_api_key", "stt_model",
		"service_account", "sk-openai",
	} {
		if strings.Contains(string(encoded), gone) {
			t.Fatalf("%s is still written: %s", gone, encoded)
		}
	}
}

// There are two languages and no third state, so everything else - including
// the "auto" an older version wrote - has to land on one of them.
func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"de":       LanguageDE,
		"DE":       LanguageDE,
		" de-CH ":  LanguageDE,
		"de_DE":    LanguageDE,
		"en":       LanguageEN,
		"en-GB":    LanguageEN,
		"":         DefaultLanguage,
		"auto":     DefaultLanguage,
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
