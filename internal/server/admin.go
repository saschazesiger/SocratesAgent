package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
)

func orLocal(url string) string {
	if url == "" {
		return "http://127.0.0.1:8080"
	}
	return url
}

// handlePreferences exposes the few settings the chat page needs at runtime.
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"speak_in_auto_mode": settings.Voice.SpeakInAutoMode,
		"speak_in_chat_mode": settings.Voice.SpeakInChatMode,
		"language":           settings.Voice.Language,
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":  settings,
		"defaults":  config.Default(),
		"version":   Version,
		"local_url": s.LocalURL(),
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings config.Settings `json:"settings"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	next := body.Settings
	next.Normalize()
	if err := s.saveSettings(next); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.Settings()})
}

type checkResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// handleDiagnostics powers the "check my setup" button in the admin dashboard.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	results := []checkResult{}

	// OpenRouter. Whether the key works decides more than its own row: speech
	// to text further down is the very same key.
	openrouterOK := false
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		results = append(results, checkResult{Name: "OpenRouter", OK: false, Detail: "no API key set"})
	} else {
		client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
		if info, err := client.CheckKey(r.Context()); err != nil {
			results = append(results, checkResult{Name: "OpenRouter", OK: false, Detail: err.Error()})
		} else {
			detail := "key accepted"
			if info.Label != "" {
				detail += " · " + info.Label
			}
			openrouterOK = true
			results = append(results, checkResult{Name: "OpenRouter", OK: true, Detail: detail})
		}
	}

	// Workspace root
	root := settings.Workspace.Root
	if err := os.MkdirAll(root, 0o755); err != nil {
		results = append(results, checkResult{Name: "Workspace", OK: false, Detail: err.Error()})
	} else {
		probe := filepath.Join(root, ".socrates-write-test")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			results = append(results, checkResult{Name: "Workspace", OK: false, Detail: err.Error()})
		} else {
			os.Remove(probe)
			results = append(results, checkResult{Name: "Workspace", OK: true, Detail: root})
		}
	}

	// Remote access
	installed, version, _ := s.tunnel.Probe()
	switch {
	case !settings.Tunnel.Enabled:
		results = append(results, checkResult{Name: "Remote access", OK: true,
			Detail: "tunnel off, reachable at " + orLocal(s.LocalURL())})
	case !installed:
		results = append(results, checkResult{Name: "Remote access", OK: false,
			Detail: "cloudflared is not installed yet, it is downloaded when the tunnel starts"})
	default:
		status := s.tunnel.Status()
		detail := status.State
		if status.URL != "" {
			detail += " · " + status.URL
		}
		if version != "" {
			detail += " · " + version
		}
		if status.Error != "" {
			detail += " · " + status.Error
		}
		results = append(results, checkResult{
			Name:   "Remote access",
			OK:     status.State == tunnel.StateRunning,
			Detail: detail,
		})
	}

	// Voice. Listening happens at OpenRouter and speaking happens here, so the
	// two halves are checked quite differently. Speech to text has nothing of
	// its own to try: it is the key that was just tested and a model to spend
	// it on, and saying it is fine regardless is how a green dot ends up next
	// to a red one about the very same key.
	transcribe := strings.TrimSpace(settings.OpenRouter.TranscribeModel)
	switch {
	case !openrouterOK:
		results = append(results, checkResult{Name: "Speech to text", OK: false,
			Detail: "it listens through OpenRouter, so that check has to pass first"})
	case transcribe == "":
		results = append(results, checkResult{Name: "Speech to text", OK: false,
			Detail: "no transcription model is picked"})
	default:
		results = append(results, checkResult{Name: "Speech to text", OK: true,
			Detail: "OpenRouter · " + transcribe})
	}
	results = append(results, voiceCheck(s.voice.Status()))

	writeJSON(w, http.StatusOK, map[string]any{"checks": results})
}

// voiceCheck says what the local voice can do right now. An install that is
// still running is the interesting case: it is neither working nor broken, and
// reporting it as either is what would send someone looking for a setting to
// fix when the honest answer is that 150 MB are on their way.
func voiceCheck(voice piper.Status) checkResult {
	switch {
	case voice.Ready:
		detail := voice.Detail
		if len(voice.Voices) > 0 {
			detail += " · " + strings.Join(voice.Voices, ", ")
		}
		return checkResult{Name: "Text to speech", OK: true, Detail: detail}
	case voice.Err != "":
		return checkResult{Name: "Text to speech", OK: false, Detail: voice.Detail + " · " + voice.Err}
	default:
		return checkResult{Name: "Text to speech", OK: false, Detail: voice.Detail}
	}
}

// escape sequence parser states.
const (
	stText = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stCharset
)

// stripper removes terminal escape sequences from a byte stream that arrives
// in arbitrary chunks, which is why it has to remember where it stopped. The
// pseudo terminal it was written for is gone; a coding agent that prints its
// version with a colour in it is what is left, and that is enough to keep it.
type stripper struct {
	state int
}

func (s *stripper) filter(in string) string {
	var out strings.Builder
	out.Grow(len(in))
	for _, r := range in {
		switch s.state {
		case stText:
			switch r {
			case 0x1b:
				s.state = stEsc
			case 0x00, 0x07:
				// NUL and BEL carry no text.
			default:
				out.WriteRune(r)
			}
		case stEsc:
			switch r {
			case '[':
				s.state = stCSI
			case ']':
				s.state = stOSC
			case 'P', '^', '_': // DCS, PM, APC all end like an OSC string
				s.state = stOSC
			case '(', ')', '*', '+': // character set selection, one more byte
				s.state = stCharset
			default:
				s.state = stText
			}
		case stCSI:
			// Parameter and intermediate bytes, then a final byte.
			if r >= 0x40 && r <= 0x7e {
				s.state = stText
			}
		case stOSC:
			switch r {
			case 0x07:
				s.state = stText
			case 0x1b:
				s.state = stOSCEsc
			}
		case stOSCEsc:
			// ESC \ terminates the string; anything else was part of it.
			s.state = stText
			if r != '\\' {
				s.state = stOSC
			}
		case stCharset:
			s.state = stText
		}
	}
	return out.String()
}

// StripANSI removes escape sequences from a complete string.
func StripANSI(s string) string {
	var st stripper
	return st.filter(s)
}
