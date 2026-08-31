package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
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

// speechInstructions tell a voice model which language it is reading. Without
// them a model handed German text reads it with an English accent, which is
// the exact complaint this setting exists to answer.
func speechInstructions(language string) string {
	name := config.LanguageName(language)
	return "Read the text aloud in " + name + ", as a native speaker of " + name +
		" would, with natural pronunciation and no foreign accent."
}

// upstreamStatus turns a provider failure into the status the page should see.
// The page retries a 502 three times before it says anything, which is right
// for a gateway that hiccuped and wrong for a model that does not exist: the
// same refusal would come back three times, after three uploads of the same
// recording on a connection that has better things to do. So a refusal the
// provider blames on the request is reported as one.
func upstreamStatus(err error) int {
	var status *openrouter.StatusError
	if errors.As(err, &status) && status.Status >= 400 && status.Status < 500 &&
		status.Status != http.StatusRequestTimeout && status.Status != http.StatusTooManyRequests {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// handleTranscribe converts recorded audio into text.
//
// The browser records raw PCM and sends a 16 kHz mono WAV, which both
// OpenRouter's audio capable chat models and every OpenAI compatible
// transcription endpoint accept.
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
	raw, err := base64.StdEncoding.DecodeString(body.Audio)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the audio is not valid base64")
		return
	}
	if len(raw) < 2000 {
		writeError(w, http.StatusBadRequest, "the recording is too short")
		return
	}

	settings := s.Settings()
	language := config.NormalizeLanguage(settings.Voice.Language)
	var text string
	if settings.Voice.STTProvider == "endpoint" {
		if settings.Voice.STTBaseURL == "" {
			writeError(w, http.StatusBadRequest, "no transcription endpoint is configured")
			return
		}
		key := settings.Voice.STTAPIKey
		if key == "" {
			key = settings.OpenRouter.APIKey
		}
		client := openrouter.New(settings.Voice.STTBaseURL, key)
		text, err = client.TranscribeEndpoint(r.Context(), settings.Voice.STTModel, raw, "audio."+format, language)
	} else {
		if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
			writeError(w, http.StatusBadRequest, "no OpenRouter API key is configured")
			return
		}
		client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
		prompt := strings.TrimSpace(settings.Voice.STTPrompt) + transcriptionHint(language)
		text, err = client.Transcribe(r.Context(), settings.OpenRouter.TranscribeModel,
			prompt, raw, format, language)
	}
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

// handleSpeak renders text to audio when an external TTS endpoint is
// configured. With the default browser provider it answers 204 and the page
// uses the built in speech synthesis instead.
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
	if len([]rune(text)) > 6000 {
		text = string([]rune(text)[:6000])
	}
	settings := s.Settings()
	if settings.Voice.TTSProvider != "endpoint" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	language := config.NormalizeLanguage(settings.Voice.Language)
	key := settings.Voice.TTSAPIKey
	if key == "" {
		key = settings.OpenRouter.APIKey
	}
	client := openrouter.New(settings.Voice.TTSBaseURL, key)
	audio, contentType, err := client.Speech(r.Context(), settings.Voice.TTSModel, settings.Voice.TTSVoice,
		text, speechInstructions(language))
	if err != nil {
		writeError(w, upstreamStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}
