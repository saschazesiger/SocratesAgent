// Package server exposes the web interface and the JSON API.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/term"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Version is stamped at build time.
var Version = "dev"

const settingsKey = "settings"

// Server wires storage, the agent engine and the HTTP handlers together.
type Server struct {
	store     *store.Store
	bus       *agent.Bus
	engine    *agent.Engine
	tunnel    *tunnel.Manager
	terminals *term.Manager

	localURL string

	mu       sync.RWMutex
	settings config.Settings

	mux *http.ServeMux

	loginMu   sync.Mutex
	loginFail map[string]*attempt
}

type attempt struct {
	count int
	until time.Time
}

// New builds a server around an open store. dataDir is where Socrates keeps
// its own files, including a cloudflared it downloads itself.
func New(st *store.Store, dataDir string) (*Server, error) {
	s := &Server{
		store:     st,
		bus:       agent.NewBus(),
		loginFail: map[string]*attempt{},
	}

	settings := config.Default()
	if err := st.GetJSON(settingsKey, &settings); err != nil && err != store.ErrNotFound {
		return nil, err
	}
	settings.Normalize()
	s.settings = settings
	if err := st.SetJSON(settingsKey, settings); err != nil {
		return nil, err
	}

	// Terminal sessions run in their own processes, started by re-executing
	// this binary, so that they survive a restart of the web server.
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("could not locate the Socrates binary: %w", err)
	}
	s.terminals, err = term.NewManager(filepath.Join(dataDir, "terminals"), self)
	if err != nil {
		return nil, err
	}

	s.engine = agent.New(st, s.bus, s.Settings, s.terminals)
	s.tunnel = tunnel.New(s.Settings, s.LocalURL, filepath.Join(dataDir, "bin"))

	s.routes()
	return s, nil
}

// Settings returns a copy of the live configuration.
func (s *Server) Settings() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Server) saveSettings(next config.Settings) error {
	next.Normalize()
	previous := s.Settings().Tunnel
	if err := s.store.SetJSON(settingsKey, next); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	s.reconcileTunnel(previous, next.Tunnel)
	return nil
}

// reconcileTunnel applies a settings change to the running tunnel.
func (s *Server) reconcileTunnel(previous, next config.TunnelSettings) {
	if s.tunnel == nil {
		return
	}
	switch {
	case !next.Enabled:
		if s.tunnel.Running() {
			s.tunnel.Stop()
		}
	case !s.tunnel.Running(), !sameTunnel(previous, next):
		if err := s.tunnel.Start(); err != nil {
			log.Printf("cloudflare tunnel: %v", err)
		}
	}
}

func sameTunnel(a, b config.TunnelSettings) bool {
	return a.Enabled == b.Enabled && a.Mode == b.Mode && a.Token == b.Token &&
		a.Hostname == b.Hostname && a.Command == b.Command &&
		strings.Join(a.ExtraArgs, "\x00") == strings.Join(b.ExtraArgs, "\x00")
}

// SetLocalURL records the loopback address Socrates listens on. It is what the
// Cloudflare tunnel publishes and what you enter when configuring a named
// tunnel in the Cloudflare dashboard.
func (s *Server) SetLocalURL(url string) {
	s.mu.Lock()
	s.localURL = url
	s.mu.Unlock()
}

// LocalURL returns the loopback address of this server.
func (s *Server) LocalURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.localURL
}

// StartTunnelIfEnabled brings the Cloudflare tunnel up when it is configured.
func (s *Server) StartTunnelIfEnabled() {
	if !s.Settings().Tunnel.Enabled {
		return
	}
	if err := s.tunnel.Start(); err != nil {
		log.Printf("cloudflare tunnel: %v", err)
		return
	}
	log.Print("cloudflare tunnel: starting")
}

// ResumeTerminals reconnects to the terminal sessions that kept running while
// Socrates was restarted, and puts them back into the process view.
func (s *Server) ResumeTerminals() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.terminals.Restore(ctx)
	s.terminals.Prune()
	s.engine.AdoptSessions()
}

// DetachTerminals lets go of the running sessions without stopping them, so a
// restart does not interrupt work in progress.
func (s *Server) DetachTerminals() { s.terminals.Detach() }

// StopTunnel shuts the tunnel down, used on graceful shutdown.
func (s *Server) StopTunnel() { s.tunnel.Stop() }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /{$}", s.page("index.html", true))
	mux.HandleFunc("GET /admin", s.page("admin.html", true))
	mux.HandleFunc("GET /login", s.pageLogin)
	mux.HandleFunc("GET /setup", s.pageSetup)
	mux.Handle("GET /static/", http.StripPrefix("/static/", web.Static()))
	mux.HandleFunc("GET /favicon.svg", web.Favicon)

	// Session
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Chats
	mux.HandleFunc("GET /api/chats", s.auth(s.handleListChats))
	mux.HandleFunc("POST /api/chats", s.auth(s.handleCreateChat))
	mux.HandleFunc("GET /api/chats/{id}", s.auth(s.handleGetChat))
	mux.HandleFunc("PATCH /api/chats/{id}", s.auth(s.handleUpdateChat))
	mux.HandleFunc("DELETE /api/chats/{id}", s.auth(s.handleDeleteChat))
	mux.HandleFunc("POST /api/chats/{id}/messages", s.auth(s.handleSendMessage))
	mux.HandleFunc("POST /api/chats/{id}/stop", s.auth(s.handleStopRun))
	mux.HandleFunc("GET /api/chats/{id}/events", s.auth(s.handleEvents))
	mux.HandleFunc("GET /api/chats/{id}/terminals", s.auth(s.handleListTerminals))
	mux.HandleFunc("POST /api/questions/{id}/answer", s.auth(s.handleAnswer))

	// Terminal sessions: watch one live, or take the keyboard yourself.
	mux.HandleFunc("GET /api/terminals/{id}", s.auth(s.handleGetTerminal))
	mux.HandleFunc("GET /api/terminals/{id}/events", s.auth(s.handleTerminalEvents))
	mux.HandleFunc("POST /api/terminals/{id}/input", s.auth(s.handleTerminalInput))
	mux.HandleFunc("POST /api/terminals/{id}/resize", s.auth(s.handleTerminalResize))
	mux.HandleFunc("POST /api/terminals/{id}/close", s.auth(s.handleTerminalClose))

	// Admin
	mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	mux.HandleFunc("POST /api/settings/password", s.auth(s.handleChangePassword))
	mux.HandleFunc("GET /api/preferences", s.auth(s.handlePreferences))
	mux.HandleFunc("GET /api/models", s.auth(s.handleModels))
	mux.HandleFunc("POST /api/diagnostics", s.auth(s.handleDiagnostics))

	// Cloudflare tunnel
	mux.HandleFunc("GET /api/tunnel", s.auth(s.handleTunnelStatus))
	mux.HandleFunc("POST /api/tunnel/start", s.auth(s.handleTunnelStart))
	mux.HandleFunc("POST /api/tunnel/stop", s.auth(s.handleTunnelStop))
	mux.HandleFunc("POST /api/tunnel/install", s.auth(s.handleTunnelInstall))

	// Voice
	mux.HandleFunc("POST /api/voice/transcribe", s.auth(s.handleTranscribe))
	mux.HandleFunc("POST /api/voice/speak", s.auth(s.handleSpeak))

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
	})

	s.mux = mux
}

// ------------------------------------------------------------------- pages

func (s *Server) page(name string, requireAuth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasPassword() {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		if requireAuth && !s.authenticated(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		web.ServePage(w, r, name)
	}
}

func (s *Server) pageLogin(w http.ResponseWriter, r *http.Request) {
	if !s.hasPassword() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if s.authenticated(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	web.ServePage(w, r, "login.html")
}

func (s *Server) pageSetup(w http.ResponseWriter, r *http.Request) {
	if s.hasPassword() {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	web.ServePage(w, r, "setup.html")
}

// ------------------------------------------------------------------ helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("server: write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return false
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return false
	}
	return true
}

// sameOrigin is a lightweight CSRF guard for state changing requests. The
// session cookie is SameSite=Lax, so this is a second line of defence.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non browser client or a same origin navigation
	}
	host := r.Host
	trimmed := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return trimmed == host
}
