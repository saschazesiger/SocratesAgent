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

// The language chosen in the dashboard is the whole point of the setting: it
// is what the voice model is told to read in.
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

	res := env.speak(t, `{"text":"Alles erledigt."}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	instructions, _ := payload["instructions"].(string)
	if !strings.Contains(instructions, "German") {
		t.Fatalf("instructions = %q", instructions)
	}

	// Switching the setting switches the accent, and nothing else has to be
	// touched for that to happen.
	env.configureVoice(t, map[string]any{"language": "en"})
	if res := env.speak(t, `{"text":"All done."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if instructions, _ := payload["instructions"].(string); !strings.Contains(instructions, "English") {
		t.Fatalf("instructions = %q", instructions)
	}
}

// An older settings document says "auto". There is no such language any more,
// so it has to land on the default rather than leaving the model to guess.
func TestSpeakFallsBackToTheDefaultLanguage(t *testing.T) {
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

	if res := env.speak(t, `{"text":"All done."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if instructions, _ := payload["instructions"].(string); !strings.Contains(instructions, "English") {
		t.Fatalf("instructions = %q", instructions)
	}
}

// With the browser provider there is nothing to render server side. The page
// gets a 204 and reads the answer with its own voice.
func TestSpeakLeavesTheBrowserToItsOwnVoice(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.configureVoice(t, map[string]any{"language": "de", "tts_provider": "browser"})
	if res := env.speak(t, `{"text":"Alles erledigt."}`); res.StatusCode != http.StatusNoContent {
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
	if prefs["language"] != config.DefaultLanguage {
		t.Fatalf("language = %#v", prefs["language"])
	}
	env.configureVoice(t, map[string]any{"language": "de"})
	_, prefs = env.do(t, env.client, "GET", "/api/preferences", "")
	if prefs["language"] != "de" {
		t.Fatalf("language = %#v", prefs["language"])
	}
}

// A transcript that translates is not a transcript, so the instruction says so
// in both languages - and both instructions name a language, because there is
// no setting left that means "work it out yourself".
func TestTranscriptionHintNamesTheLanguageAndForbidsTranslating(t *testing.T) {
	german := transcriptionHint(config.LanguageDE)
	if !strings.Contains(german, "German") || !strings.Contains(german, "never translate") {
		t.Fatalf("german hint = %q", german)
	}
	english := transcriptionHint(config.LanguageEN)
	if !strings.Contains(english, "English") || strings.Contains(english, "German") {
		t.Fatalf("english hint = %q", english)
	}
	if !strings.Contains(speechInstructions(config.LanguageDE), "German") {
		t.Fatalf("german speech = %q", speechInstructions(config.LanguageDE))
	}
	if !strings.Contains(speechInstructions(config.LanguageEN), "English") {
		t.Fatalf("english speech = %q", speechInstructions(config.LanguageEN))
	}
}

// configureOpenRouter points the server at a stand in gateway, the same way
// the dashboard's OpenRouter section does.
func (e *testEnv) configureOpenRouter(t *testing.T, fields map[string]any) {
	t.Helper()
	_, data := e.do(t, e.client, "GET", "/api/settings", "")
	settings := data["settings"].(map[string]any)
	current := settings["openrouter"].(map[string]any)
	for key, value := range fields {
		current[key] = value
	}
	body, _ := json.Marshal(map[string]any{"settings": settings})
	if res, _ := e.do(t, e.client, "PUT", "/api/settings", string(body)); res.StatusCode != http.StatusOK {
		t.Fatalf("saving the openrouter settings failed: %d", res.StatusCode)
	}
}

func (e *testEnv) transcribe(t *testing.T) (*http.Response, map[string]any) {
	t.Helper()
	audio := base64.StdEncoding.EncodeToString(make([]byte, 4000))
	body, _ := json.Marshal(map[string]any{"audio": audio, "format": "wav"})
	return e.do(t, e.client, "POST", "/api/voice/transcribe", string(body))
}

// A dedicated transcription model is served on /audio/transcriptions and
// refuses /chat/completions. Sending the recording to the chat endpoint anyway
// is what turned the microphone into a 502, so the refusal has to be followed.
func TestTranscribeThroughOpenRouterUsesTheTranscriptionEndpoint(t *testing.T) {
	var seen []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/chat/completions" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"openai/gpt-transcribe is a transcription model and cannot be used with the chat/completions endpoint."}}`)
			return
		}
		io.WriteString(w, `{"text":"hallo zusammen"}`)
	}))
	defer gateway.Close()

	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.configureVoice(t, map[string]any{"language": "de", "stt_provider": "openrouter"})
	env.configureOpenRouter(t, map[string]any{
		"base_url": gateway.URL, "api_key": "k", "transcribe_model": "openai/gpt-transcribe",
	})

	res, decoded := env.transcribe(t)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("transcribe failed: %d (%v)", res.StatusCode, decoded)
	}
	if decoded["text"] != "hallo zusammen" {
		t.Fatalf("text = %#v", decoded["text"])
	}
	if len(seen) == 0 || seen[len(seen)-1] != "/audio/transcriptions" {
		t.Fatalf("requests = %v", seen)
	}
}

// A model that does not exist will not exist on the next attempt either. The
// page retries a 502 three times before it says anything, so a refusal the
// provider blames on the settings has to arrive as one - once, with a sentence
// that says where to fix it.
func TestTranscribeReportsAConfigurationRefusalWithoutRetries(t *testing.T) {
	attempts := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Model vendor/nope does not exist"}}`)
	}))
	defer gateway.Close()

	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.configureVoice(t, map[string]any{"language": "de", "stt_provider": "openrouter"})
	env.configureOpenRouter(t, map[string]any{
		"base_url": gateway.URL, "api_key": "k", "transcribe_model": "vendor/nope",
	})

	res, decoded := env.transcribe(t)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d (%v)", res.StatusCode, decoded)
	}
	message, _ := decoded["error"].(string)
	if !strings.Contains(message, "does not exist") || !strings.Contains(message, "/admin") {
		t.Fatalf("error = %q", message)
	}
	// Both endpoints were tried once, and neither was tried again.
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
}
