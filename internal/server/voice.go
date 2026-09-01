package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
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

// handleSpeak reads an answer out loud with the Piper engine on this machine.
// There is no provider to pick, no key to get wrong and nothing for the page
// to fall back on, so a failure here is reported rather than swallowed: either
// the audio comes back or the page is told why not.
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
	if runes := []rune(text); len(runes) > piper.MaxTextRunes {
		// The engine trims to the same length itself. Doing it here as well is
		// what keeps the browser's deadline, which grows with the length of
		// the text it sent, honest about what it is waiting for.
		text = string(runes[:piper.MaxTextRunes])
	}
	settings := s.Settings()
	audio, contentType, err := s.voice.Speak(r.Context(), text,
		config.NormalizeLanguage(settings.Voice.Language), settings.Voice.TTSRate)
	if err != nil {
		if errors.Is(err, piper.ErrInstalling) {
			// The first answer of a fresh installation lands here, and it is
			// not a failure: 150 MB are on their way, this request started or
			// joined them and the page retries. So this says how far they have
			// got rather than what went wrong.
			message := "The voice is still being installed, so this answer cannot be read out loud yet."
			status := s.voice.Status()
			switch {
			case status.Err != "":
				// An earlier attempt gave up and this one is the retry. Saying
				// only "still being installed" would hide a reason that comes
				// back every single time - no published build for this
				// platform, say - behind a sentence that reads like patience
				// will fix it.
				message = "The voice is being installed again after the last attempt failed, " +
					"so this answer cannot be read out loud yet. The last attempt said: " + status.Err
			case status.State == piper.StateInstalling && status.Detail != "":
				message += " " + status.Detail
			}
			writeError(w, http.StatusServiceUnavailable, message)
			return
		}
		writeError(w, http.StatusInternalServerError,
			"The answer could not be read out loud: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}

// handleVoiceStatus reports what the voice is doing. It exists for the setup
// check in the dashboard, which polls it while Piper installs itself: without
// it an installation in progress is indistinguishable from a voice that is
// simply broken, and the difference is the whole answer.
func (s *Server) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"voice": s.voice.Status()})
}
