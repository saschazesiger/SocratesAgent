package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/googletts"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

// transcriptionHint is appended to the transcription instruction. A
// multilingual model handed German audio and an English instruction will
// cheerfully answer in English, and a transcript that translates is not a
// transcript, so the language is spelled out.
func transcriptionHint(language string) string {
	name := config.LanguageName(language)
	return " The audio is spoken in " + name + ". Write the transcript in " + name + ", never translate it."
}

// upstreamStatus turns an OpenRouter failure into the status the page should
// see. The page retries a 502 three times before it says anything, which is
// right for a gateway that hiccuped and wrong for a model that does not exist:
// the same refusal would come back three times, after three uploads of the
// same recording on a connection that has better things to do. So a refusal
// OpenRouter blames on the request is reported as one.
func upstreamStatus(err error) int {
	var routed *openrouter.StatusError
	if !errors.As(err, &routed) {
		return http.StatusBadGateway
	}
	if routed.Status >= 400 && routed.Status < 500 &&
		routed.Status != http.StatusRequestTimeout && routed.Status != http.StatusTooManyRequests {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// handleTranscribe converts recorded audio into text with the transcription
// model chosen in the dashboard.
//
// The browser records raw PCM and sends a 16 kHz mono WAV, which both kinds of
// model OpenRouter serves - a dedicated transcriber and an audio capable chat
// model - accept.
func (s *Server) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Audio  string `json:"audio"`  // base64, no data: prefix
		Format string `json:"format"` // wav | mp3
	}
	if !readJSON(w, r, &body) {
		return
	}
	if i := strings.Index(body.Audio, ","); strings.HasPrefix(body.Audio, "data:") && i > 0 {
		body.Audio = body.Audio[i+1:]
	}
	if strings.TrimSpace(body.Audio) == "" {
		writeError(w, http.StatusBadRequest, "no audio was sent")
		return
	}
	format := strings.ToLower(strings.TrimSpace(body.Format))
	if format == "" {
		format = "wav"
	}
	raw, decodeErr := base64.StdEncoding.DecodeString(body.Audio)
	if decodeErr != nil {
		writeError(w, http.StatusBadRequest, "the audio is not valid base64")
		return
	}
	if len(raw) < 2000 {
		writeError(w, http.StatusBadRequest, "the recording is too short")
		return
	}

	settings := s.Settings()
	language := config.NormalizeLanguage(settings.Voice.Language)
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "no OpenRouter API key is configured")
		return
	}
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	prompt := strings.TrimSpace(settings.Voice.STTPrompt) + transcriptionHint(language)
	text, err := client.Transcribe(r.Context(), settings.OpenRouter.TranscribeModel,
		prompt, raw, format, language)
	if err != nil {
		code := upstreamStatus(err)
		message := err.Error()
		if code == http.StatusBadRequest {
			message += " - open /admin and pick a transcription model"
		}
		writeError(w, code, message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": strings.TrimSpace(text)})
}

// handleSpeak reads an answer out loud with Google Cloud Text-to-Speech.
// There is nothing for the page to fall back on, so a failure here is
// reported rather than swallowed: either the audio comes back or the page is
// told why not, in Google's own words where Google is the one refusing.
func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "no text was sent")
		return
	}
	// The client truncates as well. Doing it here too is what keeps the
	// browser's deadline, which grows with the length of the text it sent,
	// honest about what it is waiting for.
	text = googletts.Truncate(text)

	settings := s.Settings()
	language := config.NormalizeLanguage(settings.Voice.Language)
	client := googletts.New(settings.Voice.GoogleAPIKey)
	if !client.Configured() {
		writeError(w, http.StatusBadRequest, notConfigured)
		return
	}
	audio, err := client.Synthesize(r.Context(), text, language,
		settings.Voice.GoogleVoice(language), settings.Voice.TTSRate)
	if err != nil {
		status, message := speechFailure(err)
		writeError(w, status, message)
		return
	}
	w.Header().Set("Content-Type", googletts.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}

// notConfigured is the one failure that is a setup step. It names the place
// the key goes, because "not configured" without that is a dead end.
const notConfigured = "Google Cloud Text-to-Speech is not configured. " +
	"Add an API key in Admin → Voice."

// speechFailure turns a render that did not happen into a status and a
// sentence. A refusal Google blames on the request - the API not enabled, a
// voice that does not exist, a quota that ran out - is reported as a 400 with
// Google's message in it, because retrying it would fail identically and the
// message is the whole answer. Everything else is the network between here and
// Google, which is a 502.
func speechFailure(err error) (int, string) {
	if errors.Is(err, googletts.ErrNoKey) {
		return http.StatusBadRequest, notConfigured
	}
	var refusal *googletts.APIError
	if errors.As(err, &refusal) {
		if refusal.Status >= 400 && refusal.Status < 500 {
			return http.StatusBadRequest, refusal.Error()
		}
		return http.StatusBadGateway, refusal.Error()
	}
	return http.StatusBadGateway, "The answer could not be read out loud: " + err.Error()
}

// handleVoiceCheck is the dashboard's "Check key": it asks Google for the
// voice list of the stored language, which needs the same API enabled and the
// same key restriction as speaking does and renders nothing. It answers with
// the same sentence a failed render would, so the two buttons never disagree
// about what is wrong.
func (s *Server) handleVoiceCheck(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	client := googletts.New(settings.Voice.GoogleAPIKey)
	if !client.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": notConfigured})
		return
	}
	if err := client.CheckKey(r.Context(), settings.Voice.Language); err != nil {
		_, message := speechFailure(err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true,
		"detail": "The key works: Google answered for " +
			googletts.LanguageCode(settings.Voice.Language) + "."})
}
