// Package server exposes the web interface and the JSON API.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Version is stamped at build time.
var Version = "dev"

const settingsKey = "settings"

// Server wires storage, the terminal sessions and the HTTP handlers together.
type Server struct {
	store   *store.Store
	tunnel  *tunnel.Manager
	voice   *piper.Engine
	manager *termux.Manager
	catalog *catalog.Catalog

	dataDir  string
	localURL string

	// hub is every browser tab the transport currently remembers, including
	// the ones whose socket has dropped and whose terminal is in its grace.
	hub *termHub
	// pingEvery, pingTimeout and writeTimeout are the transport's watchdog and
	// its slow-reader guard, set once here so that the tests can run them in
	// milliseconds rather than in minutes.
	pingEvery    time.Duration
	pingTimeout  time.Duration
	writeTimeout time.Duration

	// tmuxAdmin is the terminal engine card of the dashboard: the detection
	// behind /api/tmux and the install it streams. Its zero value is a server
	// that has installed nothing, so it needs nothing from New.
	tmuxAdmin tmuxAdmin

	mu       sync.RWMutex
	settings config.Settings

	mux *http.ServeMux

	loginMu   sync.Mutex
	loginFail map[string]*attempt
	// wsRate is the per address ceiling on WebSocket handshakes, guarded by
	// loginMu because it is the same kind of defence.
	wsRate map[string]*attempt
}

type attempt struct {
	count int
	until time.Time
}

// New builds a server around an open store. dataDir is where Socrates keeps
// its own files, including a cloudflared it downloads itself.
func New(st *store.Store, dataDir string) (*Server, error) {
	s := &Server{
		store:        st,
		dataDir:      dataDir,
		hub:          newTermHub(),
		pingEvery:    defaultPingEvery,
		pingTimeout:  defaultPingTimeout,
		writeTimeout: defaultWriteTimeout,
		loginFail:    map[string]*attempt{},
		wsRate:       map[string]*attempt{},
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

	cfg := managerConfig(dataDir, settings)
	// The manager plans a resume of its own accord after a reboot, so it reads
	// the live settings rather than the copy it was built with.
	cfg.Settings = s.Settings
	// A pane that dies and a window that changes size are facts about the
	// substrate; the frames that carry them to a browser are the transport's,
	// which is why the Manager is handed two functions rather than a socket.
	cfg.OnExit = s.onSessionExit
	cfg.OnSize = s.onSessionSize
	manager, err := termux.New(st, cfg)
	if err != nil {
		return nil, err
	}
	s.manager = manager
	s.catalog = catalog.New(st, s.Settings)

	s.tunnel = tunnel.New(s.Settings, s.LocalURL, filepath.Join(dataDir, "bin"))
	s.voice = piper.New(filepath.Join(dataDir, "voice"))
	s.installVoice()

	s.routes()
	return s, nil
}

// managerConfig is the terminal substrate as the dashboard has configured it.
// The generated tmux configuration is written from these, so a change to them
// reaches the sessions created after it and never the ones already running: a
// terminal keeps the substrate it was started on.
func managerConfig(dataDir string, settings config.Settings) termux.Config {
	return termux.Config{
		DataDir: dataDir,
		Conf: termux.ConfOptions{
			HistoryLimit: settings.Terminal.HistoryLimit,
			Mouse:        settings.Terminal.Mouse,
			ExtendedKeys: settings.Terminal.ExtendedKeys,
		},
		WindowSize: settings.Terminal.WindowSize,
		Supervisor: termux.DetectSupervisor(),
	}
}

// StartSessions brings the terminal substrate up: the generated configuration,
// the socket the tmux hooks report to, the re-adoption of everything that
// survived the last run, and the poll that watches for panes that die.
//
// Adoption runs before the listener accepts, because a browser that asks for
// the session list in the first moment must not be told a running session is
// gone. A failure here is reported and not fatal: a Socrates that cannot start
// sessions still has to serve the dashboard that says why.
func (s *Server) StartSessions(ctx context.Context) error {
	if err := s.manager.Start(ctx); err != nil {
		return err
	}
	if err := s.manager.Adopt(ctx); err != nil {
		log.Printf("terminal sessions: could not reconcile with tmux: %v", err)
	}
	s.manager.StartPoll(ctx)
	return nil
}

// StopSessions lets go of everything Socrates owns and nothing tmux owns: the
// sessions themselves keep running, which is the whole point of them.
func (s *Server) StopSessions() {
	if err := s.manager.Close(); err != nil {
		log.Printf("terminal sessions: %v", err)
	}
}

// Sessions is the terminal manager, for the transport that attaches to it.
func (s *Server) Sessions() *termux.Manager { return s.manager }

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

	// Terminal sessions
	mux.HandleFunc("GET /api/sessions", s.auth(s.handleListSessions))
	mux.HandleFunc("POST /api/sessions", s.auth(s.handleCreateSession))
	mux.HandleFunc("GET /api/sessions/{id}", s.auth(s.handleGetSession))
	mux.HandleFunc("PATCH /api/sessions/{id}", s.auth(s.handleRenameSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.auth(s.endingViewers(s.handleDeleteSession)))
	mux.HandleFunc("POST /api/sessions/{id}/archive", s.auth(s.handleArchiveSession))
	mux.HandleFunc("POST /api/sessions/{id}/resume", s.auth(s.handleResumeSession))
	mux.HandleFunc("POST /api/sessions/{id}/restart", s.auth(s.handleRestartSession))
	mux.HandleFunc("POST /api/sessions/{id}/ack-resume", s.auth(s.handleAckResume))
	mux.HandleFunc("GET /api/sessions/{id}/journal", s.auth(s.handleJournal))
	mux.HandleFunc("GET /api/sessions/{id}/ws", s.auth(s.handleSessionWS))

	// The harness catalogue: what can be started, and where.
	mux.HandleFunc("GET /api/harnesses", s.auth(s.handleHarnesses))
	mux.HandleFunc("POST /api/harnesses/refresh", s.auth(s.handleRefreshHarnesses))

	// Admin
	mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	mux.HandleFunc("POST /api/settings/password", s.auth(s.handleChangePassword))
	mux.HandleFunc("GET /api/preferences", s.auth(s.handlePreferences))
	mux.HandleFunc("POST /api/diagnostics", s.auth(s.handleDiagnostics))

	// The terminal engine: is tmux here, and the install that puts it there.
	mux.HandleFunc("GET /api/tmux", s.auth(s.handleTmux))
	mux.HandleFunc("POST /api/tmux/install", s.auth(s.handleTmuxInstall))
	mux.HandleFunc("GET /api/tmux/events", s.auth(s.handleTmuxEvents))

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
// session cookie is SameSite=Lax (auth.go, and DEVIATIONS says why: Strict
// would sign a person out of every link into the app), so this is not a second
// line of defence but a real one; the WebSocket handshake has the same check
// inside websocket.Accept, which is where it has to be because a GET is not
// covered here.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non browser client or a same origin navigation
	}
	host := r.Host
	trimmed := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return trimmed == host
}
