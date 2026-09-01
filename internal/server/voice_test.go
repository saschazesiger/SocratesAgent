package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
)

/* ------------------------------------------------------------ a fake piper */

// requireShell skips where the stand in cannot run: it is a POSIX shell
// script, the same one internal/piper uses to test its own rendering.
func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake piper is a POSIX shell script")
	}
}

// fakePiper is a shell script that stands in for the binary. It writes down
// the arguments and the text it was handed, so a test can ask what actually
// reached piper, and prints a RIFF header, which is the only thing the engine
// checks before calling the bytes audio.
func fakePiper(record string) string {
	return "#!/bin/sh\n" +
		": > \"" + record + "/args\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + record + "/args\"; done\n" +
		"while IFS= read -r line || [ -n \"$line\" ]; do printf '%s\\n' \"$line\"; done > \"" + record + "/stdin\"\n" +
		"printf 'RIFFxxxxWAVEfmt xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'\n"
}

// installPiper lays out an installation where the engine looks for one that is
// already there - piper/ beside voices/, exactly what the Docker image bakes
// in - and returns the root to point SOCRATES_PIPER_DIR at, together with the
// directory the stand in records into.
//
// The models are made the size the engine insists on by truncating an empty
// file: it treats anything under a megabyte as a transfer that was cut short,
// and a megabyte of real bytes per test would be a megabyte for nothing.
func installPiper(t *testing.T, script string) (root, record string) {
	t.Helper()
	root, record = t.TempDir(), t.TempDir()
	binary := filepath.Join(root, "piper", "piper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// The espeak data is part of being installed: piper names it on every
	// command line, and the engine refuses a tree without it.
	if err := os.MkdirAll(filepath.Join(root, "piper", "espeak-ng-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	voices := filepath.Join(root, "voices")
	if err := os.MkdirAll(voices, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, voice := range []string{piper.VoiceEnglish, piper.VoiceGerman} {
		model := filepath.Join(voices, voice+".onnx")
		if err := os.WriteFile(model, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(model, 2<<20); err != nil {
			t.Fatal(err)
		}
		config := `{"audio":{"sample_rate":22050},"espeak":{"voice":"` + voice + `"},"padding":"` +
			strings.Repeat("x", 200) + `"}`
		if err := os.WriteFile(model+".json", []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, record
}

// installedVoice puts a piper where a server built in a test will find one.
// Without it every such server starts pulling 150 MB off GitHub in the
// background, because that is what a fresh installation does and what these
// tests are emphatically not about. A test that has put a piper of its own in
// place keeps it.
func installedVoice(t *testing.T) {
	t.Helper()
	if os.Getenv(piper.EnvDir) != "" {
		return
	}
	root, _ := installPiper(t, "#!/bin/sh\nexit 0\n")
	t.Setenv(piper.EnvDir, root)
}

// arguments is the command line the stand in was called with, one entry per
// line the way it wrote them.
func arguments(t *testing.T, record string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(record, "args"))
	if err != nil {
		t.Fatalf("the fake piper was never called: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// after is the value of a flag, or "" when it was not passed at all.
func after(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// speakingEnv is a server whose voice is the stand in piper, already set up
// and logged in.
func speakingEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	requireShell(t)
	record := t.TempDir()
	root, _ := installPiper(t, fakePiper(record))
	t.Setenv(piper.EnvDir, root)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	return env, record
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

// The whole point of the change: an answer is read out loud by the piper on
// this machine, and what comes back is audio rather than a hint that the
// browser should say it itself.
func TestSpeakRendersTheAnswerLocally(t *testing.T) {
	env, record := speakingEnv(t)

	res := env.speak(t, `{"text":"All done."}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != piper.ContentType {
		t.Fatalf("content type = %q", got)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q", got)
	}
	audio, _ := io.ReadAll(res.Body)
	if len(audio) < 44 || string(audio[:4]) != "RIFF" {
		t.Fatalf("the answer is not a WAV: %d bytes, %q", len(audio), audio)
	}
	// The text has to arrive on piper's stdin, because that is the only way it
	// gets read: an answer that is rendered without it is silence.
	raw, err := os.ReadFile(filepath.Join(record, "stdin"))
	if err != nil {
		t.Fatalf("piper was handed no text: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "All done." {
		t.Fatalf("piper was handed %q", raw)
	}
}

// The spoken language is the one setting that decides which voice reads, so
// switching it has to switch the model piper loads and nothing else.
func TestSpeakUsesTheVoiceOfTheSpokenLanguage(t *testing.T) {
	env, record := speakingEnv(t)
	env.configureVoice(t, map[string]any{"language": "de"})

	if res := env.speak(t, `{"text":"Alles erledigt."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if got := after(arguments(t, record), "--model"); filepath.Base(got) != piper.VoiceGerman+".onnx" {
		t.Fatalf("--model = %q, want the German voice", got)
	}

	env.configureVoice(t, map[string]any{"language": "en"})
	if res := env.speak(t, `{"text":"All done."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if got := after(arguments(t, record), "--model"); filepath.Base(got) != piper.VoiceEnglish+".onnx" {
		t.Fatalf("--model = %q, want the English voice", got)
	}
}

// The speaking rate used to be a slider the server ignored. It reaches piper
// as the length of a phoneme, which is the same idea upside down: twice the
// rate is half the length.
func TestSpeakPassesTheSpeakingRateOn(t *testing.T) {
	env, record := speakingEnv(t)
	env.configureVoice(t, map[string]any{"tts_rate": 2})

	if res := env.speak(t, `{"text":"All done."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if got := after(arguments(t, record), "--length_scale"); got != "0.5" {
		t.Fatalf("--length_scale = %q", got)
	}

	// The normal rate is passed as no flag at all, which is what piper does
	// with a voice left alone.
	env.configureVoice(t, map[string]any{"tts_rate": 1})
	if res := env.speak(t, `{"text":"All done."}`); res.StatusCode != http.StatusOK {
		t.Fatalf("speak failed: %d", res.StatusCode)
	}
	if got := after(arguments(t, record), "--length_scale"); got != "" {
		t.Fatalf("--length_scale = %q, want the voice's own pace", got)
	}
}

// A piper that is there and does not work has to be reported. There is no
// browser voice to fall back on any more, so a 200 with nothing playable in it
// would be an answer that is silently never read.
func TestSpeakReportsAVoiceThatCannotRender(t *testing.T) {
	requireShell(t)
	root, _ := installPiper(t, "#!/bin/sh\necho 'libonnxruntime.so.1.14.1: cannot open shared object file' >&2\nexit 1\n")
	t.Setenv(piper.EnvDir, root)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	res, data := env.do(t, env.client, "POST", "/api/voice/speak", `{"text":"All done."}`)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d (%v)", res.StatusCode, data)
	}
	message, _ := data["error"].(string)
	if !strings.Contains(message, "could not be read out loud") ||
		!strings.Contains(message, "cannot open shared object file") {
		t.Fatalf("error = %q", message)
	}
}

// An installation that cannot be finished still has to be said out loud, and
// the request cannot be what says it: no render waits for a download, so the
// first press is told to come back and the reason arrives with the next one.
// A page that only ever heard "still being installed" would wait for ever on a
// platform where the install cannot work at all.
func TestSpeakSaysWhyTheVoiceCouldNotBeInstalled(t *testing.T) {
	t.Setenv(piper.EnvDir, "")
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such release", http.StatusNotFound)
	}))
	defer refusing.Close()

	engine := piper.New(t.TempDir())
	engine.ReleaseURL, engine.VoicesURL = refusing.URL, refusing.URL
	server := &Server{voice: engine}

	// The first press starts the install and is answered immediately.
	res, data := speakDirectly(t, context.Background(), server)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("the first press = %d (%v)", res.Code, data)
	}

	// It fails, on its own context rather than on the request's.
	deadline := time.Now().Add(10 * time.Second)
	for engine.Status().State != piper.StateFailed {
		if time.Now().After(deadline) {
			t.Fatalf("the install never finished: %#v", engine.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}

	res, data = speakDirectly(t, context.Background(), server)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%v)", res.Code, data)
	}
	message, _ := data["error"].(string)
	if !strings.Contains(message, "after the last attempt failed") || !strings.Contains(message, "404") {
		t.Fatalf("error = %q, want the reason the install keeps failing", message)
	}
}

// A fresh installation downloads 150 MB, and the first answer may well be
// asked for while that is still happening. That is not a failure: the page
// retries, so it is told to wait and how far the download has got - at once,
// while somebody is still listening for it, rather than after the download.
func TestSpeakAsksThePageToWaitWhileTheVoiceInstalls(t *testing.T) {
	t.Setenv(piper.EnvDir, "")
	// A download that never answers, so the install is still running when the
	// request arrives.
	blocked := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer func() { close(blocked); stalled.Close() }()

	root, _ := installPiper(t, "#!/bin/sh\nexit 0\n")
	// The binary is installed and the voices are not, which is what makes the
	// install reach for the models rather than for a release archive that this
	// platform may not even have.
	if err := os.RemoveAll(filepath.Join(root, "voices")); err != nil {
		t.Fatal(err)
	}
	engine := piper.New(root)
	engine.VoicesURL = stalled.URL
	go func() { _ = engine.Ensure(context.Background()) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(engine.Status().Detail, "Downloading") {
		if time.Now().After(deadline) {
			t.Fatalf("the install never started: %#v", engine.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The request behaves like a browser that is still there: its context is
	// alive, and it has to be answered now. Waiting for the install would mean
	// answering after the download, by which time the page has long given up
	// and said the voice is broken.
	server := &Server{voice: engine}
	begin := time.Now()
	res, data := speakDirectly(t, context.Background(), server)
	if waited := time.Since(begin); waited > 2*time.Second {
		t.Fatalf("the handler took %s to answer, it has to answer immediately", waited)
	}
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%v)", res.Code, data)
	}
	message, _ := data["error"].(string)
	if !strings.Contains(message, "still being installed") || !strings.Contains(message, "Downloading") {
		t.Fatalf("error = %q, want a sentence saying how far the install has got", message)
	}

	// The dashboard asks the same question through the status endpoint, and
	// has to get the same answer rather than "not ready".
	status := httptest.NewRecorder()
	server.handleVoiceStatus(status, httptest.NewRequest(http.MethodGet, "/api/voice/status", nil))
	var reported struct {
		Voice piper.Status `json:"voice"`
	}
	if err := json.NewDecoder(status.Body).Decode(&reported); err != nil {
		t.Fatal(err)
	}
	if reported.Voice.State != piper.StateInstalling || reported.Voice.Ready {
		t.Fatalf("status = %#v", reported.Voice)
	}
}

// speakDirectly asks a server built by hand to read something out loud. The
// engines these tests need - one that cannot install, one that is halfway
// through installing - cannot be reached through the settings, so the handler
// is called with the server they belong to.
func speakDirectly(t *testing.T, ctx context.Context, s *Server) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/voice/speak", strings.NewReader(`{"text":"All done."}`))
	res := httptest.NewRecorder()
	s.handleSpeak(res, req.WithContext(ctx))
	var decoded map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &decoded)
	return res, decoded
}

// The setup check has three answers to give and only one of them is "fix
// something". An install that is still running must not be reported as a
// broken voice: the person would go looking for a setting that does not exist
// while the download they are waiting for is running.
func TestTheSetupCheckTellsAnInstallFromAFailure(t *testing.T) {
	ready := voiceCheck(piper.Status{
		Ready: true, State: piper.StateReady,
		Detail: "Piper is ready, using the copy Socrates installed at /data/voice/piper/piper.",
		Voices: []string{piper.VoiceEnglish, piper.VoiceGerman},
	})
	if !ready.OK || !strings.Contains(ready.Detail, "/data/voice/piper/piper") ||
		!strings.Contains(ready.Detail, piper.VoiceGerman) {
		t.Fatalf("ready = %#v", ready)
	}

	installing := voiceCheck(piper.Status{
		State:  piper.StateInstalling,
		Detail: "Downloading the German voice… 42% (25.6 of 61.0 MB)",
	})
	if installing.OK || !strings.Contains(installing.Detail, "42%") {
		t.Fatalf("installing = %#v", installing)
	}

	failed := voiceCheck(piper.Status{
		State:  piper.StateFailed,
		Detail: "Installing Piper failed, it is tried again the next time an answer is read out loud.",
		Err:    "download Piper: 404",
	})
	if failed.OK || !strings.Contains(failed.Detail, "404") {
		t.Fatalf("failed = %#v", failed)
	}
}

// The setup check in the dashboard reports the voice, so the endpoint behind
// it has to say what is installed and where it came from.
func TestVoiceStatusReportsAReadyEngine(t *testing.T) {
	env, _ := speakingEnv(t)

	res, data := env.do(t, env.client, "GET", "/api/voice/status", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status failed: %d", res.StatusCode)
	}
	voice, _ := data["voice"].(map[string]any)
	if voice["ready"] != true || voice["state"] != piper.StateReady {
		t.Fatalf("voice = %#v", voice)
	}
	// Both voices are installed or neither is: the language is a setting
	// someone flips, and a flip that starts a download is a broken answer.
	if voices, _ := voice["voices"].([]any); len(voices) != 2 {
		t.Fatalf("voices = %#v", voice["voices"])
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
// is not its business either: the server hands the rate to piper itself, so a
// page given one would only be carrying a number it cannot use.
func TestPreferencesCarryTheLanguage(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, prefs := env.do(t, env.client, "GET", "/api/preferences", "")
	if prefs["language"] != config.DefaultLanguage {
		t.Fatalf("language = %#v", prefs["language"])
	}
	for _, gone := range []string{"tts_provider", "tts_rate"} {
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
