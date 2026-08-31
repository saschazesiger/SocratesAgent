package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// configureVoice saves a voice section through the public API, the same way
// the admin dashboard does.
func (e *testEnv) configureVoice(t *testing.T, voice map[string]any) {
	t.Helper()
	_, data := e.do(t, e.client, "GET", "/api/settings", "")
	settings := data["settings"].(map[string]any)
	current := settings["voice"].(map[string]any)
	for key, value := range voice {
		current[key] = value
	}
	body, _ := json.Marshal(map[string]any{"settings": settings})
	res, _ := e.do(t, e.client, "PUT", "/api/settings", string(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("saving the voice settings failed: %d", res.StatusCode)
	}
}

// fakeSpeech stands in for an OpenAI compatible /audio/speech endpoint and
// records the payload it was sent.
func fakeSpeech(t *testing.T, seen *map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*seen = body
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0xff, 0xfb, 0x00})
	}))
	t.Cleanup(server.Close)
	return server
}

func (e *testEnv) speak(t *testing.T, body string) *http.Response {
	t.Helper()
	res, _ := e.do(t, e.client, "POST", "/api/voice/speak", body)
	return res
}

// A language chosen in the dashboard is the whole point of the setting, so it
// beats anything the page believes about the text it is about to hear.
func TestSpeakReadsInTheConfiguredLanguage(t *testing.T) {
	var payload map[string]any
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	tts := fakeSpeech(t, &payload)
	env.configureVoice(t, map[string]any{
		"language":     "de",
		"tts_provider": "endpoint",
		"tts_base_url": tts.URL,
		"tts_model":    "gpt-4o-mini-tts",
		"tts_api_key":  "k",
	})

	res := env.speak(t, `{"text":"Alles erledigt.","lang":"en"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	instructions, _ := payload["instructions"].(string)
	if !strings.Contains(instructions, "German") {
		t.Fatalf("instructions = %q", instructions)
	}
}

// On automatic the page is the only side that has the text in front of it, so
// the language it worked out is what gets used.
func TestSpeakFallsBackToTheLanguageThePageDetected(t *testing.T) {
	var payload map[string]any
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	tts := fakeSpeech(t, &payload)
	env.configureVoice(t, map[string]any{
		"language":     "auto",
		"tts_provider": "endpoint",
		"tts_base_url": tts.URL,
		"tts_model":    "gpt-4o-mini-tts",
		"tts_api_key":  "k",
	})

	if res := env.speak(t, `{"text":"Alles erledigt.","lang":"de"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if instructions, _ := payload["instructions"].(string); !strings.Contains(instructions, "German") {
		t.Fatalf("instructions = %q", instructions)
	}

	// And with nothing to go on, nothing is claimed: the voice model reads the
	// text as it finds it rather than being pointed at the wrong language.
	payload = nil
	if res := env.speak(t, `{"text":"All done."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if _, ok := payload["instructions"]; ok {
		t.Fatalf("instructions were invented: %#v", payload)
	}
}

// With the browser provider there is nothing to render server side. The page
// gets a 204 and reads the answer with its own voice.
func TestSpeakLeavesTheBrowserToItsOwnVoice(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.configureVoice(t, map[string]any{"language": "de", "tts_provider": "browser"})
	if res := env.speak(t, `{"text":"Alles erledigt.","lang":"de"}`); res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestTranscribeNamesTheLanguageForTheEndpoint(t *testing.T) {
	var fields map[string]string
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		fields = map[string]string{}
		for key, values := range r.MultipartForm.Value {
			fields[key] = values[0]
		}
		io.WriteString(w, `{"text":"hallo zusammen"}`)
	}))
	defer stt.Close()

	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.configureVoice(t, map[string]any{
		"language":     "de",
		"stt_provider": "endpoint",
		"stt_base_url": stt.URL,
		"stt_model":    "whisper-1",
		"stt_api_key":  "k",
	})

	audio := base64.StdEncoding.EncodeToString(make([]byte, 4000))
	body, _ := json.Marshal(map[string]any{"audio": audio, "format": "wav"})
	res, decoded := env.do(t, env.client, "POST", "/api/voice/transcribe", string(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("transcribe failed: %d", res.StatusCode)
	}
	if decoded["text"] != "hallo zusammen" {
		t.Fatalf("text = %#v", decoded["text"])
	}
	if fields["language"] != "de" {
		t.Fatalf("language = %#v", fields)
	}
}

// The page reads the language out of the preferences, so it has to be there.
func TestPreferencesCarryTheLanguage(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, prefs := env.do(t, env.client, "GET", "/api/preferences", "")
	if prefs["language"] != config.LanguageAuto {
		t.Fatalf("language = %#v", prefs["language"])
	}
	env.configureVoice(t, map[string]any{"language": "de"})
	_, prefs = env.do(t, env.client, "GET", "/api/preferences", "")
	if prefs["language"] != "de" {
		t.Fatalf("language = %#v", prefs["language"])
	}
}

// A transcript that translates is not a transcript. The instruction says so
// whether or not a language was pinned.
func TestTranscriptionHintAlwaysForbidsTranslating(t *testing.T) {
	pinned := transcriptionHint(config.LanguageDE)
	if !strings.Contains(pinned, "German") || !strings.Contains(pinned, "never translate") {
		t.Fatalf("pinned hint = %q", pinned)
	}
	auto := transcriptionHint(config.LanguageAuto)
	if strings.Contains(auto, "German") || !strings.Contains(auto, "never translate") {
		t.Fatalf("automatic hint = %q", auto)
	}
	if speechInstructions(config.LanguageAuto) != "" {
		t.Fatalf("automatic must not instruct a language")
	}
	if !strings.Contains(speechInstructions(config.LanguageEN), "English") {
		t.Fatalf("english = %q", speechInstructions(config.LanguageEN))
	}
}
