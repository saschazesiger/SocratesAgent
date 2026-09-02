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
	"github.com/saschazesiger/SocratesAgent/internal/googletts"
)

/* ------------------------------------------------------ a fake Google API */

// googleStub stands in for Google Cloud Text-to-Speech. It records the one
// request it was sent - the key, the voice, the text, the rate - so a test can
// ask what actually left this machine, and answers with base64 audio or with
// whatever refusal the test asked for.
type googleStub struct {
	requests []map[string]any
	keys     []string
	status   int
	answer   string
}

// newGoogleStub points the whole process at a stand in. googletts.New reads
// the address per call, so setting it here is enough for every handler a test
// reaches afterwards.
func newGoogleStub(t *testing.T) *googleStub {
	t.Helper()
	stub := &googleStub{status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.keys = append(stub.keys, r.Header.Get("X-Goog-Api-Key"))
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		stub.requests = append(stub.requests, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stub.status)
		answer := stub.answer
		if answer == "" {
			payload, _ := json.Marshal(map[string]any{
				"audioContent": base64.StdEncoding.EncodeToString([]byte("ID3 pretend mp3")),
			})
			answer = string(payload)
		}
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	t.Setenv(googletts.EnvBaseURL, server.URL)
	return stub
}

// last is the request the stub was sent most recently, flattened to the four
// things a test asks about.
func (g *googleStub) last(t *testing.T) (text, language, voice string, rate float64) {
	t.Helper()
	if len(g.requests) == 0 {
		t.Fatal("Google was never called")
	}
	body := g.requests[len(g.requests)-1]
	input, _ := body["input"].(map[string]any)
	text, _ = input["text"].(string)
	voiceBlock, _ := body["voice"].(map[string]any)
	language, _ = voiceBlock["languageCode"].(string)
	voice, _ = voiceBlock["name"].(string)
	audio, _ := body["audioConfig"].(map[string]any)
	rate, _ = audio["speakingRate"].(float64)
	return text, language, voice, rate
}

// speakingEnv is a logged in server whose voice is the stand in above, with a
// key saved the way the dashboard saves one.
func speakingEnv(t *testing.T) (*testEnv, *googleStub) {
	t.Helper()
	stub := newGoogleStub(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.configureVoice(t, map[string]any{"google_api_key": "a-google-key"})
	return env, stub
}

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

func (e *testEnv) speak(t *testing.T, body string) *http.Response {
	t.Helper()
	res, _ := e.do(t, e.client, "POST", "/api/voice/speak", body)
	return res
}

/* ------------------------------------------------------------------ speech */

// The whole point: an answer comes back as audio the browser can play, and the
// key never leaves this machine except in the header Google wants it in.
func TestSpeakReturnsTheAudioGoogleRendered(t *testing.T) {
	env, stub := speakingEnv(t)

	res := env.speak(t, `{"text":"All done."}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d (%s)", res.StatusCode, body)
	}
	if got := res.Header.Get("Content-Type"); got != googletts.ContentType {
		t.Fatalf("content type = %q", got)
	}
	audio, _ := io.ReadAll(res.Body)
	if string(audio) != "ID3 pretend mp3" {
		t.Fatalf("audio = %q", audio)
	}
	if len(stub.keys) != 1 || stub.keys[0] != "a-google-key" {
		t.Fatalf("keys = %v", stub.keys)
	}
	text, language, voice, rate := stub.last(t)
	if text != "All done." {
		t.Errorf("text = %q", text)
	}
	if language != "en-US" || voice != googletts.DefaultVoiceEN {
		t.Errorf("voice = %s / %s", language, voice)
	}
	if rate != 1 {
		t.Errorf("rate = %v", rate)
	}
}

// The spoken language is the one setting that decides which voice reads, and a
// voice name typed into the dashboard is what actually goes to Google.
func TestSpeakUsesTheVoiceOfTheSpokenLanguage(t *testing.T) {
	env, stub := speakingEnv(t)

	env.configureVoice(t, map[string]any{"language": "de"})
	env.speak(t, `{"text":"Fertig."}`).Body.Close()
	if _, language, voice, _ := stub.last(t); language != "de-DE" || voice != googletts.DefaultVoiceDE {
		t.Fatalf("german = %s / %s", language, voice)
	}

	env.configureVoice(t, map[string]any{"google_voice_de": "de-DE-Standard-F"})
	env.speak(t, `{"text":"Fertig."}`).Body.Close()
	if _, _, voice, _ := stub.last(t); voice != "de-DE-Standard-F" {
		t.Fatalf("voice = %q", voice)
	}

	env.configureVoice(t, map[string]any{"language": "en"})
	env.speak(t, `{"text":"Done."}`).Body.Close()
	if _, language, voice, _ := stub.last(t); language != "en-US" || voice != googletts.DefaultVoiceEN {
		t.Fatalf("english = %s / %s", language, voice)
	}
}

// The speaking rate reaches Google as speakingRate, and a rate outside what
// the API accepts is clamped rather than sent and refused.
func TestSpeakPassesTheSpeakingRateOn(t *testing.T) {
	env, stub := speakingEnv(t)

	env.configureVoice(t, map[string]any{"tts_rate": 1.5})
	env.speak(t, `{"text":"All done."}`).Body.Close()
	if _, _, _, rate := stub.last(t); rate != 1.5 {
		t.Fatalf("rate = %v", rate)
	}

	env.configureVoice(t, map[string]any{"tts_rate": 99})
	env.speak(t, `{"text":"All done."}`).Body.Close()
	if _, _, _, rate := stub.last(t); rate != googletts.MaxRate {
		t.Fatalf("rate = %v, want it clamped", rate)
	}
}

// Without a key nothing is sent and the page is told where the key goes. This
// is the first run of every installation, so it has to read as a setup step
// and not as a fault.
func TestSpeakWithoutAKeySaysWhereToAddOne(t *testing.T) {
	stub := newGoogleStub(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	res, data := env.do(t, env.client, "POST", "/api/voice/speak", `{"text":"All done."}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d (%v)", res.StatusCode, data)
	}
	message, _ := data["error"].(string)
	if !strings.Contains(message, "not configured") || !strings.Contains(message, "Admin") {
		t.Fatalf("error = %q", message)
	}
	if len(stub.requests) != 0 {
		t.Fatalf("a request went out anyway: %v", stub.requests)
	}
}

// Google's own sentence is the answer. An API that was never enabled on the
// project is the commonest first failure there is, and it comes with an
// instruction in it - so it is passed through rather than replaced with "the
// voice did not work".
func TestSpeakPassesGooglesRefusalThrough(t *testing.T) {
	env, stub := speakingEnv(t)
	stub.status = http.StatusForbidden
	stub.answer = `{"error":{"code":403,"message":"Cloud Text-to-Speech API has not been used in ` +
		`project 42 before or it is disabled.","status":"PERMISSION_DENIED"}}`

	res, data := env.do(t, env.client, "POST", "/api/voice/speak", `{"text":"All done."}`)
	// A refusal that would come back identically on the next attempt is a 400,
	// because the page retries a 502 three times before it says anything.
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d (%v)", res.StatusCode, data)
	}
	message, _ := data["error"].(string)
	if !strings.Contains(message, "has not been used in project 42") {
		t.Fatalf("error = %q", message)
	}
}

// The API refuses more than 5000 bytes outright, and a status summary of a
// full screen can be longer than that. It is cut here instead of turning into
// an answer nobody hears.
func TestSpeakTruncatesAnswersThatAreTooLongForTheAPI(t *testing.T) {
	env, stub := speakingEnv(t)

	long := strings.Repeat("Dies ist ein Satz über Größen. ", 400)
	body, _ := json.Marshal(map[string]any{"text": long})
	res := env.speak(t, string(body))
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	text, _, _, _ := stub.last(t)
	if len(text) == 0 || len(text) > googletts.MaxInputBytes {
		t.Fatalf("Google was sent %d bytes", len(text))
	}
	if !strings.HasSuffix(text, ".") {
		t.Errorf("the text was cut mid sentence: %q", text[max(0, len(text)-40):])
	}
}

// "Check key" is the one button that proves a key before anybody waits on an
// answer, and it reports what Google said either way.
func TestTheKeyCheckReportsWhatGoogleSaid(t *testing.T) {
	env, stub := speakingEnv(t)

	_, data := env.do(t, env.client, "POST", "/api/voice/check", "")
	if data["ok"] != true {
		t.Fatalf("check = %#v", data)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %d, the check makes exactly one", len(stub.requests))
	}

	stub.status = http.StatusForbidden
	stub.answer = `{"error":{"code":403,"message":"API key not valid.","status":"PERMISSION_DENIED"}}`
	_, data = env.do(t, env.client, "POST", "/api/voice/check", "")
	if data["ok"] != false {
		t.Fatalf("check = %#v", data)
	}
	if detail, _ := data["detail"].(string); !strings.Contains(detail, "API key not valid") {
		t.Fatalf("detail = %#v", data["detail"])
	}
}

// A key check with no key does not go to the network at all, and says the same
// sentence the speak endpoint does.
func TestTheKeyCheckWithoutAKeySaysSo(t *testing.T) {
	stub := newGoogleStub(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	_, data := env.do(t, env.client, "POST", "/api/voice/check", "")
	if data["ok"] != false {
		t.Fatalf("check = %#v", data)
	}
	if detail, _ := data["detail"].(string); !strings.Contains(detail, "not configured") {
		t.Fatalf("detail = %#v", data["detail"])
	}
	if len(stub.requests) != 0 {
		t.Fatalf("a request went out anyway: %v", stub.requests)
	}
}

// The setup check row must not cost anything: it is drawn every time the
// dashboard opens, and a paid API call per draw would be a surprise on a bill.
// What it can say for free is whether there is a key and whether the voice is
// one of the free ones.
func TestTheSetupCheckReportsTheKeyAndTheVoice(t *testing.T) {
	missing := voiceCheck(config.VoiceSettings{Language: "en"})
	if missing.OK || !strings.Contains(missing.Detail, "not configured") {
		t.Fatalf("missing = %#v", missing)
	}

	standard := voiceCheck(config.VoiceSettings{
		Language: "de", GoogleAPIKey: "k", GoogleVoiceDE: googletts.DefaultVoiceDE})
	if !standard.OK || !strings.Contains(standard.Detail, googletts.DefaultVoiceDE) {
		t.Fatalf("standard = %#v", standard)
	}
	if strings.Contains(standard.Detail, "free tier") {
		t.Fatalf("a free voice was reported as outside the free tier: %#v", standard)
	}

	billed := voiceCheck(config.VoiceSettings{
		Language: "en", GoogleAPIKey: "k", GoogleVoiceEN: "en-US-Neural2-C"})
	if !billed.OK || !strings.Contains(billed.Detail, "outside the 4M-character free tier") {
		t.Fatalf("billed = %#v", billed)
	}
}

// A dedicated transcription model is told the language up front, which is both
// faster and more accurate than letting it guess from the first second.
func TestTranscribeNamesTheLanguageForATranscriptionModel(t *testing.T) {
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
	env.configureVoice(t, map[string]any{"language": "de"})
	env.configureOpenRouter(t, map[string]any{
		"base_url": stt.URL, "api_key": "k", "transcribe_model": "openai/whisper-1",
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

// The page reads the language out of the preferences, and nothing else about
// the voice. There is no provider left to tell it about, and the speaking rate
// is not its business either: the server hands the rate to Google itself, so a
// page given one would only be carrying a number it cannot use.
func TestPreferencesCarryTheLanguage(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, prefs := env.do(t, env.client, "GET", "/api/preferences", "")
	if prefs["language"] != config.DefaultLanguage {
		t.Fatalf("language = %#v", prefs["language"])
	}
	for _, gone := range []string{"tts_provider", "tts_rate", "speak_in_auto_mode"} {
		if _, present := prefs[gone]; present {
			t.Fatalf("%s is still offered to the page: %#v", gone, prefs)
		}
	}
	env.configureVoice(t, map[string]any{"language": "de"})
	_, prefs = env.do(t, env.client, "GET", "/api/preferences", "")
	if prefs["language"] != "de" {
		t.Fatalf("preferences = %#v", prefs)
	}
}

// A transcript that translates is not a transcript, so the instruction says so
// in both languages - and both name a language, because there is no setting
// left that means "work it out yourself".
func TestTranscriptionHintNamesTheLanguageAndForbidsTranslating(t *testing.T) {
	german := transcriptionHint(config.LanguageDE)
	if !strings.Contains(german, "German") || !strings.Contains(german, "never translate") {
		t.Fatalf("german hint = %q", german)
	}
	english := transcriptionHint(config.LanguageEN)
	if !strings.Contains(english, "English") || strings.Contains(english, "German") {
		t.Fatalf("english hint = %q", english)
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
	env.configureVoice(t, map[string]any{"language": "de"})
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
	env.configureVoice(t, map[string]any{"language": "de"})
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
