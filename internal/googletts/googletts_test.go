package googletts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stub is a stand in for the API: it records the one request it was sent and
// answers with whatever the test told it to.
type stub struct {
	server  *httptest.Server
	path    string
	key     string
	body    map[string]any
	rawBody string
}

func newStub(t *testing.T, status int, answer string) *stub {
	t.Helper()
	s := &stub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path = r.URL.RequestURI()
		s.key = r.Header.Get("X-Goog-Api-Key")
		raw, _ := io.ReadAll(r.Body)
		s.rawBody = string(raw)
		_ = json.Unmarshal(raw, &s.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(s.server.Close)
	t.Setenv(EnvBaseURL, s.server.URL)
	return s
}

// audioAnswer is what a successful synthesis looks like: base64 in a JSON
// field, which the client has to decode before the browser sees it.
func audioAnswer(audio string) string {
	body, _ := json.Marshal(map[string]any{
		"audioContent": base64.StdEncoding.EncodeToString([]byte(audio)),
	})
	return string(body)
}

func voiceOf(body map[string]any, field string) string {
	voice, _ := body["voice"].(map[string]any)
	value, _ := voice[field].(string)
	return value
}

// The whole point: text goes out as JSON, base64 audio comes back decoded.
func TestSynthesizeReturnsTheDecodedAudio(t *testing.T) {
	stub := newStub(t, http.StatusOK, audioAnswer("ID3 pretend mp3"))
	audio, err := New("test-key").Synthesize(context.Background(),
		"Hello there.", "en", DefaultVoiceEN, 1)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if string(audio) != "ID3 pretend mp3" {
		t.Fatalf("audio = %q", audio)
	}
	if stub.key != "test-key" {
		t.Errorf("the key travelled as %q", stub.key)
	}
	if stub.path != "/text:synthesize" {
		t.Errorf("path = %q", stub.path)
	}
	input, _ := stub.body["input"].(map[string]any)
	if input["text"] != "Hello there." {
		t.Errorf("text = %v", input["text"])
	}
	if got := voiceOf(stub.body, "languageCode"); got != "en-US" {
		t.Errorf("languageCode = %q", got)
	}
	if got := voiceOf(stub.body, "name"); got != DefaultVoiceEN {
		t.Errorf("voice = %q", got)
	}
	audioConfig, _ := stub.body["audioConfig"].(map[string]any)
	if audioConfig["audioEncoding"] != "MP3" {
		t.Errorf("encoding = %v", audioConfig["audioEncoding"])
	}
}

// The language decides the tag, and an empty voice name falls back to the
// Standard voice of that language rather than letting Google pick one.
func TestGermanUsesTheGermanTagAndItsStandardVoice(t *testing.T) {
	stub := newStub(t, http.StatusOK, audioAnswer("mp3"))
	if _, err := New("k").Synthesize(context.Background(), "Guten Tag.", "de", "", 1); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if got := voiceOf(stub.body, "languageCode"); got != "de-DE" {
		t.Errorf("languageCode = %q", got)
	}
	if got := voiceOf(stub.body, "name"); got != DefaultVoiceDE {
		t.Errorf("voice = %q", got)
	}
}

// Without a key nothing is sent at all: this is a setup step, and a request
// that could only come back 401 is not worth making.
func TestWithoutAKeyNothingIsSent(t *testing.T) {
	stub := newStub(t, http.StatusOK, audioAnswer("mp3"))
	_, err := New("   ").Synthesize(context.Background(), "anything", "en", "", 1)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v", err)
	}
	if stub.path != "" {
		t.Errorf("a request was made anyway: %q", stub.path)
	}
}

// What Google says is what the person reads. A 403 for an API that was never
// enabled is a sentence with an answer in it, and swallowing it would leave
// "the voice did not work" and nothing else.
func TestGoogleErrorsArePassedThrough(t *testing.T) {
	newStub(t, http.StatusForbidden, `{"error":{"code":403,`+
		`"message":"Cloud Text-to-Speech API has not been used in project 42 before or it is disabled.",`+
		`"status":"PERMISSION_DENIED"}}`)
	_, err := New("k").Synthesize(context.Background(), "hello", "en", "", 1)
	if err == nil {
		t.Fatal("a refusal came back as success")
	}
	var refusal *APIError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %T %v", err, err)
	}
	if refusal.Status != http.StatusForbidden {
		t.Errorf("status = %d", refusal.Status)
	}
	if !strings.Contains(refusal.Error(), "has not been used in project 42") {
		t.Errorf("message = %q", refusal.Error())
	}
}

// A body that is not Google's error envelope still has to say something.
func TestAnUnreadableRefusalStillSaysTheStatus(t *testing.T) {
	newStub(t, http.StatusTooManyRequests, "quota exceeded")
	_, err := New("k").Synthesize(context.Background(), "hello", "en", "", 1)
	if err == nil || !strings.Contains(err.Error(), "429") ||
		!strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("err = %v", err)
	}
}

// The API refuses more than 5000 bytes outright, so the text is cut here - at
// a sentence end where there is one, and never through a rune.
func TestLongTextIsTruncatedAtASentence(t *testing.T) {
	stub := newStub(t, http.StatusOK, audioAnswer("mp3"))
	sentence := strings.Repeat("Dies ist ein Satz über Größen. ", 400)
	if _, err := New("k").Synthesize(context.Background(), sentence, "de", "", 1); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	input, _ := stub.body["input"].(map[string]any)
	sent, _ := input["text"].(string)
	if len(sent) == 0 || len(sent) > MaxInputBytes {
		t.Fatalf("sent %d bytes", len(sent))
	}
	if len(sent) < MaxInputBytes/2 {
		t.Fatalf("far too much was thrown away: %d bytes", len(sent))
	}
	if !strings.HasSuffix(sent, "Größen.") {
		t.Errorf("the cut is not at a sentence end: %q", sent[max(0, len(sent)-40):])
	}
}

// A single word longer than the limit has no sentence and no space in it. It
// still has to come back as valid UTF-8 rather than half a rune.
func TestATextWithNoBoundaryIsCutOnARune(t *testing.T) {
	long := strings.Repeat("ä", MaxInputBytes)
	cut := Truncate(long)
	if len(cut) > MaxInputBytes {
		t.Fatalf("%d bytes", len(cut))
	}
	if strings.ContainsRune(cut, '�') || !isValidUTF8(cut) {
		t.Fatalf("the cut broke a rune")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// Text that fits is sent exactly as it is.
func TestShortTextIsLeftAlone(t *testing.T) {
	if got := Truncate("One sentence."); got != "One sentence." {
		t.Fatalf("got %q", got)
	}
}

// The rate is the API's range and not the dashboard's: anything outside it is
// a 400, so it is clamped on the way out.
func TestTheSpeakingRateIsClamped(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0, 1}, {-3, 1}, {0.1, MinRate}, {1, 1}, {1.4, 1.4}, {9, MaxRate},
	} {
		if got := ClampRate(c.in); got != c.want {
			t.Errorf("ClampRate(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	stub := newStub(t, http.StatusOK, audioAnswer("mp3"))
	if _, err := New("k").Synthesize(context.Background(), "hello", "en", "", 99); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	audioConfig, _ := stub.body["audioConfig"].(map[string]any)
	if audioConfig["speakingRate"] != MaxRate {
		t.Errorf("speakingRate = %v", audioConfig["speakingRate"])
	}
}

// An answer with no audio in it is a failure and not a moment of silence.
func TestAnEmptyAnswerIsAFailure(t *testing.T) {
	newStub(t, http.StatusOK, `{"audioContent":""}`)
	if _, err := New("k").Synthesize(context.Background(), "hello", "en", "", 1); err == nil {
		t.Fatal("silence came back as success")
	}
}

// CheckKey is the cheap proof that a key works, and it asks for voices.
func TestCheckKeyAsksForTheVoiceList(t *testing.T) {
	stub := newStub(t, http.StatusOK, `{"voices":[]}`)
	if err := New("k").CheckKey(context.Background(), "de"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if stub.path != "/voices?languageCode=de-DE" {
		t.Fatalf("path = %q", stub.path)
	}
}

// Only the Standard voices are free, and the dashboard says so from this.
func TestOnlyStandardVoicesCountAsFree(t *testing.T) {
	if !IsStandardVoice("de-DE-Standard-A") || !IsStandardVoice("en-US-Standard-C") {
		t.Error("a Standard voice was not recognised")
	}
	if IsStandardVoice("en-US-Neural2-C") || IsStandardVoice("de-DE-Wavenet-B") {
		t.Error("a billed voice passed as free")
	}
}
