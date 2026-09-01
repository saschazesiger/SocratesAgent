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

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/engine"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Version is stamped at build time.
var Version = "dev"

const settingsKey = "settings"

// Server wires storage, the harness engine and the HTTP handlers together.
type Server struct {
	store  *store.Store
	bus    *engine.Bus
	engine *engine.Engine
	tunnel *tunnel.Manager
	hosts  *agenthost.Manager
	agents *catalog.Catalog
	voice  *piper.Engine

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
		bus:       engine.NewBus(),
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

	// Agent sessions run in their own processes, started by re-executing this
	// binary, so that a turn in flight survives a restart of the web server.
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("could not locate the Socrates binary: %w", err)
	}
	s.hosts, err = agenthost.NewManager(filepath.Join(dataDir, "agents"), self)
	if err != nil {
		return nil, err
	}

	s.agents = catalog.New(st, s.Settings)
	s.engine = engine.New(st, s.bus, s.Settings, s.hosts)
	s.tunnel = tunnel.New(s.Settings, s.LocalURL, filepath.Join(dataDir, "bin"))
	s.voice = piper.New(filepath.Join(dataDir, "voice"))
	s.installVoice()

	s.routes()
	return s, nil
}

// installVoice puts Piper on the machine while nobody is waiting for it. A
// fresh installation downloads about 150 MB, and the worst moment to discover
// that is the first answer somebody asks to have read out loud.
//
// The download itself belongs to the engine, on a context and a ceiling of its
// own, and is the same one a render starts when it finds the voice missing -
// one mechanism, so a render cannot end up racing the installer at startup.
// This goroutine only waits for the outcome to log it, and a failure is all it
// does: the server has to come up either way, and the install is tried again
// the next time an answer is read out loud.
func (s *Server) installVoice() {
	engine := s.voice
	go func() {
		if err := engine.Ensure(context.Background()); err != nil {
			log.Printf("voice: %v", err)
		}
	}()
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

// ResumeAgents reconnects to the agent sessions that kept running while
// Socrates was restarted and takes their turns back over. It returns the run
// ids it claimed, so the caller can keep RecoverRuns off them.
func (s *Server) ResumeAgents() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.hosts.Restore(ctx)
	s.hosts.Prune()
	return s.engine.Adopt(ctx)
}

// DetachAgents lets go of the running sessions without stopping them, so a
// restart does not interrupt work in progress. The engine is told first: a
// subscription that ends because we dropped the socket is not a turn that
// died, and mistaking one for the other orphans the turn.
func (s *Server) DetachAgents() { s.engine.Detach() }

// StopTunnel shuts the tunnel down, used on graceful shutdown.
func (s *Server) StopTunnel() { s.tunnel.Stop() }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /{$}", s.page("index.html"))
	mux.HandleFunc("GET /admin", s.page("admin.html"))
	mux.HandleFunc("GET /login", s.pageLogin)
	mux.HandleFunc("GET /setup", s.pageSetup)
	mux.Handle("GET /static/", http.StripPrefix("/static/", web.Static()))
	mux.HandleFunc("GET /favicon.png", web.Favicon)
	// The offline shell worker is deliberately outside the auth wall: it holds
	// no data, and it has to be reachable to be updated.
	mux.HandleFunc("GET /sw.js", web.ServiceWorker)

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
	mux.HandleFunc("POST /api/chats/{id}/archive", s.auth(s.handleArchiveChat))
	mux.HandleFunc("POST /api/chats/{id}/unarchive", s.auth(s.handleUnarchiveChat))
	mux.HandleFunc("POST /api/chats/{id}/messages", s.auth(s.handleSendMessage))
	mux.HandleFunc("POST /api/chats/{id}/stop", s.auth(s.handleStopRun))
	mux.HandleFunc("GET /api/chats/{id}/events", s.auth(s.handleEvents))

	// Agents: what is installed, and which models each one offers.
	mux.HandleFunc("GET /api/agents", s.auth(s.handleAgents))
	mux.HandleFunc("POST /api/agents/refresh", s.auth(s.handleAgentsRefresh))

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
	mux.HandleFunc("GET /api/voice/status", s.auth(s.handleVoiceStatus))

	// Liveness, and the one endpoint nothing in the browser calls: the
	// container's HEALTHCHECK is what asks for it, because a process that is
	// running is not the same thing as a server that answers.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
	})

	s.mux = mux
}

// ------------------------------------------------------------------- pages

// page serves one of the signed in documents. Both of them are behind the
// password: login and setup have their own handlers, because they are the two
// pages you reach without one.
func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasPassword() {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		if !s.authenticated(r) {
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
