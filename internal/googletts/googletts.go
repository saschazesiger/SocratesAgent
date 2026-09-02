// Package googletts reads text out loud with the Google Cloud Text-to-Speech
// REST API.
//
// It is deliberately small and dependency free: one POST to
// text:synthesize with an API key in a header, and the base64 audio that
// comes back. There is no service account, no OAuth dance and no Google SDK,
// because an API key restricted to this one API is the shortest path from
// "I have a Google account" to a voice, and it is the credential the admin
// dashboard can ask for in one field.
//
// Only Standard voices are offered by default. They are the tier with four
// million characters a month free; WaveNet and Neural2 voices sound better and
// have a far smaller allowance of their own - a million characters a month,
// Studio voices 100,000 bytes - and cost more per character beyond it.
package googletts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBaseURL is where the API lives. EnvBaseURL replaces it, which is
	// what lets the tests and the browser suite put a stand in there instead
	// of spending somebody's quota.
	DefaultBaseURL = "https://texttospeech.googleapis.com/v1"
	EnvBaseURL     = "SOCRATES_GOOGLE_TTS_URL"

	// ContentType is what Synthesize returns: MP3 is asked for because it is
	// the one encoding every browser plays from a blob without ceremony.
	ContentType = "audio/mpeg"

	// MaxInputBytes is the API's own limit on one request, counted in bytes of
	// UTF-8 and not in characters - which matters the moment a German umlaut
	// is involved. Longer text is cut before it is sent, because the API
	// answers a longer one with a 400 and no audio at all.
	MaxInputBytes = 5000

	// MinRate and MaxRate are the speaking rates the API accepts. Anything
	// outside them is a 400, so the setting is clamped rather than passed on.
	MinRate = 0.25
	MaxRate = 4.0

	// The voices a fresh installation speaks with. Both are Standard voices,
	// which is the free tier, and both are female.
	DefaultVoiceEN = "en-US-Standard-C"
	DefaultVoiceDE = "de-DE-Standard-A"

	// requestTimeout bounds one render. The API answers a sentence in well
	// under a second, so a request still running after twenty seconds is a
	// network that has gone away rather than a voice that is thinking.
	requestTimeout = 20 * time.Second
)

// LanguageCode is the BCP-47 tag the API wants for one of the two languages
// Socrates speaks.
func LanguageCode(language string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "de") {
		return "de-DE"
	}
	return "en-US"
}

// DefaultVoice is the Standard voice of a language.
func DefaultVoice(language string) string {
	if LanguageCode(language) == "de-DE" {
		return DefaultVoiceDE
	}
	return DefaultVoiceEN
}

// IsStandardVoice reports whether a voice name is in the free tier. It is a
// name check because that is all there is to go on without asking Google, and
// the naming is the API's own documented scheme.
func IsStandardVoice(name string) bool {
	return strings.Contains(strings.ToLower(name), "-standard-")
}

// ClampRate keeps a speaking rate inside what the API accepts. A zero is a
// number field that was left empty rather than a request for silence, so it
// becomes the normal pace.
func ClampRate(rate float64) float64 {
	if rate <= 0 {
		return 1
	}
	if rate < MinRate {
		return MinRate
	}
	if rate > MaxRate {
		return MaxRate
	}
	return rate
}

// Truncate cuts text down to what one request may carry. It prefers to stop at
// the end of a sentence: an answer that ends mid-word sounds like the voice
// broke, and one that ends a sentence early sounds like it was done.
func Truncate(text string) string {
	if len(text) <= MaxInputBytes {
		return text
	}
	cut := text[:MaxInputBytes]
	// Never end inside a multi-byte rune: the API would see invalid UTF-8.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	// A sentence boundary is worth losing a few hundred bytes for, but not
	// half the answer, so only one in the last part of the text counts.
	if end := lastSentenceEnd(cut); end > len(cut)/2 {
		return strings.TrimSpace(cut[:end])
	}
	if space := strings.LastIndexAny(cut, " \n\t"); space > len(cut)/2 {
		return strings.TrimSpace(cut[:space])
	}
	return strings.TrimSpace(cut)
}

// lastSentenceEnd is the offset just past the last ".", "!", "?", ":" or ";"
// that is followed by a space - the end of a sentence, or of a clause long
// enough to stop on, rather than a decimal point or an abbreviation in the
// middle of one.
func lastSentenceEnd(text string) int {
	for i := len(text) - 1; i > 0; i-- {
		switch text[i] {
		case ' ', '\n', '\t':
			switch text[i-1] {
			case '.', '!', '?', ':', ';':
				return i
			}
		}
	}
	return 0
}

// APIError is a refusal from Google, kept whole so the page can show what
// Google said. The status is what tells "the API is not enabled on this
// project" (403) apart from "that voice does not exist" (400) and "you are out
// of quota" (429), and all three are things the person at the dashboard can
// act on.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return "Google Cloud Text-to-Speech answered " + strconv.Itoa(e.Status)
	}
	return e.Message
}

// Client talks to the API with one API key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New builds a client for a key. The address is the real API unless the
// environment names another one.
func New(apiKey string) *Client {
	base := strings.TrimSpace(os.Getenv(EnvBaseURL))
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		APIKey:  strings.TrimSpace(apiKey),
		HTTP:    &http.Client{Timeout: requestTimeout},
	}
}

// Configured reports whether there is a key to speak with at all.
func (c *Client) Configured() bool { return c != nil && c.APIKey != "" }

type synthesizeRequest struct {
	Input struct {
		Text string `json:"text"`
	} `json:"input"`
	Voice struct {
		LanguageCode string `json:"languageCode"`
		Name         string `json:"name,omitempty"`
	} `json:"voice"`
	AudioConfig struct {
		AudioEncoding string  `json:"audioEncoding"`
		SpeakingRate  float64 `json:"speakingRate"`
	} `json:"audioConfig"`
}

// Synthesize renders one line and returns the MP3 bytes.
//
// The text is truncated to what the API accepts and the rate is clamped to
// what it allows, because both limits are the API's and a request that breaks
// one comes back as a 400 with no audio - which is a worse answer than a
// slightly shorter one read at a slightly slower pace.
func (c *Client) Synthesize(ctx context.Context, text, language, voice string, rate float64) ([]byte, error) {
	if !c.Configured() {
		return nil, ErrNoKey
	}
	text = Truncate(strings.TrimSpace(text))
	if text == "" {
		return nil, ErrNoText
	}
	code := LanguageCode(language)
	if strings.TrimSpace(voice) == "" {
		voice = DefaultVoice(language)
	}
	var payload synthesizeRequest
	payload.Input.Text = text
	payload.Voice.LanguageCode = code
	payload.Voice.Name = strings.TrimSpace(voice)
	payload.AudioConfig.AudioEncoding = "MP3"
	payload.AudioConfig.SpeakingRate = ClampRate(rate)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodPost, "/text:synthesize", body)
	if err != nil {
		return nil, err
	}
	var answer struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("Google Cloud Text-to-Speech sent an answer that could not be read: %w", err)
	}
	audio, err := base64.StdEncoding.DecodeString(strings.TrimSpace(answer.AudioContent))
	if err != nil || len(audio) == 0 {
		return nil, errNoAudio
	}
	return audio, nil
}

// CheckKey asks for the voice list of one language, which is the cheapest
// request that proves a key works: it needs the same API enabled and the same
// key restriction as speaking does, and it renders nothing.
func (c *Client) CheckKey(ctx context.Context, language string) error {
	if !c.Configured() {
		return ErrNoKey
	}
	_, err := c.do(ctx, http.MethodGet, "/voices?languageCode="+LanguageCode(language), nil)
	return err
}

// do makes one request and turns anything that is not a 200 into an APIError
// carrying Google's own sentence.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Goog-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Google Cloud Text-to-Speech could not be reached: %w", err)
	}
	defer res.Body.Close()
	// A megabyte and a half is far more than a rendered sentence and far less
	// than anything that could exhaust this machine.
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<21))
	if err != nil {
		return nil, fmt.Errorf("Google Cloud Text-to-Speech could not be read: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, &APIError{Status: res.StatusCode, Message: googleMessage(res.StatusCode, raw)}
	}
	return raw, nil
}

// googleMessage digs the human sentence out of an error body. Google answers
// {"error":{"code":403,"message":"…","status":"PERMISSION_DENIED"}}, and that
// message is the useful half: it names the project, the API to enable or the
// quota that ran out.
func googleMessage(status int, raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return "Google Cloud Text-to-Speech refused the request (" +
			strconv.Itoa(status) + "): " + strings.TrimSpace(envelope.Error.Message)
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	if text == "" {
		return "Google Cloud Text-to-Speech answered " + strconv.Itoa(status) + " with nothing to say."
	}
	return "Google Cloud Text-to-Speech answered " + strconv.Itoa(status) + ": " + text
}
