package server

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

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
		text, err = client.TranscribeEndpoint(r.Context(), settings.Voice.STTModel, raw, "audio."+format)
	} else {
		if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
			writeError(w, http.StatusBadRequest, "no OpenRouter API key is configured")
			return
		}
		client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
		text, err = client.TranscribeChat(r.Context(), settings.OpenRouter.TranscribeModel,
			settings.Voice.STTPrompt, body.Audio, format)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
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
	key := settings.Voice.TTSAPIKey
	if key == "" {
		key = settings.OpenRouter.APIKey
	}
	client := openrouter.New(settings.Voice.TTSBaseURL, key)
	audio, contentType, err := client.Speech(r.Context(), settings.Voice.TTSModel, settings.Voice.TTSVoice, text)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}
