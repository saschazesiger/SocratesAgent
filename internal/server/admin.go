package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/term"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
)

func orLocal(url string) string {
	if url == "" {
		return "http://127.0.0.1:8080"
	}
	return url
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b)
}

// handlePreferences exposes the few settings the chat page needs at runtime.
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"speak_in_auto_mode": settings.Voice.SpeakInAutoMode,
		"speak_in_chat_mode": settings.Voice.SpeakInChatMode,
		"tts_rate":           settings.Voice.TTSRate,
		"tts_provider":       settings.Voice.TTSProvider,
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
	// Normalize gives every tool a unique, usable id, so a half filled form
	// from the dashboard can never break the orchestrator.
	next.Normalize()
	if err := s.saveSettings(next); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.Settings()})
}

var (
	modelsMu    sync.Mutex
	modelsCache []openrouter.Model
	modelsAt    time.Time
	modelsKey   string
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	modelsMu.Lock()
	fresh := time.Since(modelsAt) < 10*time.Minute && modelsKey == settings.OpenRouter.APIKey && len(modelsCache) > 0
	cached := modelsCache
	modelsMu.Unlock()
	if fresh {
		writeJSON(w, http.StatusOK, map[string]any{"models": cached})
		return
	}
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	models, err := client.Models(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	modelsMu.Lock()
	modelsCache, modelsAt, modelsKey = models, time.Now(), settings.OpenRouter.APIKey
	modelsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
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

	// OpenRouter
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
			results = append(results, checkResult{Name: "OpenRouter", OK: true, Detail: detail})
		}
	}

	// Workspace root
	root := settings.Agent.WorkspaceRoot
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

	// A real terminal is what makes driving the coding agents possible.
	if term.HasPTY {
		results = append(results, checkResult{Name: "Terminal", OK: true,
			Detail: fmt.Sprintf("interactive, %d session(s) open", len(s.terminals.States("")))})
	} else {
		results = append(results, checkResult{Name: "Terminal", OK: false,
			Detail: "this build has no pseudo terminal, so full screen programs will not run interactively"})
	}

	// The programs Socrates can run
	for _, tool := range settings.Tools {
		if !tool.Enabled {
			continue
		}
		path, err := exec.LookPath(tool.Command)
		if err != nil {
			results = append(results, checkResult{Name: tool.Name, OK: false,
				Detail: "command " + tool.Command + " not found in PATH"})
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
		cancel()
		version := strings.TrimSpace(strings.SplitN(stripControl(string(out)), "\n", 2)[0])
		if err != nil {
			results = append(results, checkResult{Name: tool.Name, OK: false,
				Detail: path + " failed to report a version: " + strings.TrimSpace(err.Error()+" "+version)})
			continue
		}
		results = append(results, checkResult{Name: tool.Name, OK: true, Detail: version})
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

	// Voice
	switch settings.Voice.STTProvider {
	case "endpoint":
		if settings.Voice.STTBaseURL == "" {
			results = append(results, checkResult{Name: "Speech to text", OK: false, Detail: "no endpoint URL set"})
		} else {
			results = append(results, checkResult{Name: "Speech to text", OK: true,
				Detail: settings.Voice.STTBaseURL + " · " + settings.Voice.STTModel})
		}
	default:
		results = append(results, checkResult{Name: "Speech to text", OK: true,
			Detail: "OpenRouter · " + settings.OpenRouter.TranscribeModel})
	}
	if settings.Voice.TTSProvider == "endpoint" {
		if settings.Voice.TTSBaseURL == "" || settings.Voice.TTSModel == "" {
			results = append(results, checkResult{Name: "Text to speech", OK: false, Detail: "endpoint or model missing"})
		} else {
			results = append(results, checkResult{Name: "Text to speech", OK: true,
				Detail: settings.Voice.TTSBaseURL + " · " + settings.Voice.TTSModel})
		}
	} else {
		results = append(results, checkResult{Name: "Text to speech", OK: true, Detail: "browser speech synthesis"})
	}

	writeJSON(w, http.StatusOK, map[string]any{"checks": results})
}

// stripControl keeps a version banner on one readable line.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, term.StripANSI(s))
}
