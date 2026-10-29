// Package server exposes the web interface and the JSON API.
package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/backup"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Version is stamped at build time.
var Version = "dev"

// SettingsKey is where the settings document lives in the key/value table.
const SettingsKey = "settings"

// Server wires storage, the terminal sessions and the HTTP handlers together.
type Server struct {
	store   *store.Store
	tunnel  *tunnel.Manager
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
	// updates is the updates card: it reads what socrates-update.service
	// left in the data directory and asks systemd to start it (updates.go).
	// Its zero value is the real machine.
	updates updatesAdmin

	mu       sync.RWMutex
	settings config.Settings
	// titles names a session once, the first time it has answered anything.
	titles *titleDriver
	// spoken keeps a chat's answers said for the ear (chatspoken.go).
	spoken *spokenCache
	// push is Web Push: the notification that reaches a phone whose page is
	// not running.
	push *pusher
	// events is the event stream every page without a terminal listens on,
	// and digests the verdicts on the chats' turns that Home shows (HOME.md).
	events  *eventHub
	digests *digester

	mux *http.ServeMux

	loginMu   sync.Mutex
	loginFail map[string]*attempt
	// wsRate is the per address ceiling on WebSocket handshakes, guarded by
	// loginMu because it is the same kind of defence.
	wsRate map[string]*attempt

	// ideMu makes finding or creating the editor of a folder one step, so
	// two taps on the same folder cannot start two editors on it.
	ideMu sync.Mutex

	// sched is the chats' clock: scheduled starts and the automatic
	// continue after a limit (schedule.go).
	sched *scheduler

	// backup is the engine that backs the data directory up (backup.go),
	// set once after New by main; restart starts Socrates again after a
	// restore, and restoreJob is that restore.
	backup     *backup.Engine
	restart    func()
	restoreJob restoreJob

	// usage holds the short-lived account limit reading shared by every tab.
	usageMu     sync.Mutex
	claudeUsage map[string]cachedLimits
	usageHTTP   *http.Client
	usageURL    string

	// titleSeq numbers the title frames (emitTitle). A page holds a terminal
	// socket and the event stream at once, and hears every one of them twice;
	// the number is how it knows the second for the same frame, and not a
	// second failure to be told about in a second dialog.
	titleSeq atomic.Int64

	// costs keeps the cost ledger current (costs.go).
	costs *costLedger

	// chats is every agent chat and the host it runs in (chats.go), and
	// accounts what they run on (accounts.go).
	chats *chatHub
	// tests runs the repositories' test catalogs (tests.go); nil until
	// StartSessions.
	tests    *testmode.Runner
	accounts *accountHub
	// mcp is the MCP servers built into Socrates (mcpbuiltin.go).
	mcp *mcpserver.Registry
	// The sign-ins of Admin's OAuth MCP servers (mcpoauth.go) and the last
	// test of each (mcpadmin.go), made when first used.
	oauthOnce sync.Once
	oauthKeep *oauthHub
	probeOnce sync.Once
	probeKeep *probeCache
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
		usageHTTP:    &http.Client{Timeout: 8 * time.Second},
		usageURL:     "https://api.anthropic.com/api/oauth/usage",
	}

	settings := config.Default()
	if err := st.GetJSON(SettingsKey, &settings); errors.Is(err, store.ErrNotFound) {
		// A fresh installation has nothing to bring up to date.
		settings.OpenRouter.ModelsRev = config.AssistModelsRev
	} else if err != nil {
		return nil, err
	}
	settings.Normalize()
	s.settings = settings
	if err := st.SetJSON(SettingsKey, settings); err != nil {
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
	cfg.OnActivity = s.onSessionActivity
	// The namer is built before the manager, because the manager is what
	// calls into it.
	s.titles = newTitleDriver(s)
	s.spoken = newSpokenCache()
	s.push = newPusher(st)
	// Every push says how many sessions are asking (the app icon's badge),
	// counted the way Home counts them.
	s.push.asking = s.askingSet
	s.events = newEventHub(s)
	s.digests = newDigester(s)
	// A turn's end waits for its outcome before it is pushed.
	s.push.checking = s.digests.outcomePending
	s.sched = newScheduler(s)
	s.costs = newCostLedger(s)
	s.chats = newChatHub(s)
	// A chat's push says what its journal says.
	s.push.chatAsk, s.push.chatTurn = s.chats.askWords, s.chats.turnWords
	s.accounts = newAccountHub(s)
	s.mcp = s.newMCPRegistry()
	manager, err := termux.New(st, cfg)
	if err != nil {
		return nil, err
	}
	s.manager = manager
	s.catalog = catalog.New(st, s.Settings)
	// An update that replaced a program's binary makes the catalogue's
	// versions stale; the updater's status file is how it finds out.
	s.catalog.WatchUpdates(filepath.Join(dataDir, updatesFile))

	s.tunnel = tunnel.New(s.Settings, s.LocalURL, filepath.Join(dataDir, "bin"))

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
	// The prepared chats of an older build are ordinary chats before anything
	// reads the chats.
	s.readyPreparedChats()
	// Chats need no tmux: their hosts are reattached whatever the terminal
	// engine says below, and so is their clock, once every chat is known.
	// The test runner knows its runs before the chats come back, so that what
	// a test session reports as it is reattached finds its run.
	s.tests = testmode.New(s.dataDir, testHost{s})
	s.tests.Load()
	s.chats.start()
	s.tests.Start()
	s.sched.start(ctx)
	// What every conversation cost is read from the CLIs' files, not from
	// tmux, so a machine whose terminals cannot start still keeps its ledger.
	s.costs.start(ctx)
	if err := s.manager.Start(ctx); err != nil {
		return err
	}
	if err := s.manager.Adopt(ctx); err != nil {
		log.Printf("terminal sessions: could not reconcile with tmux: %v", err)
	}
	s.manager.StartPoll(ctx)
	// The detector starts after Adopt, so that its first tick sees the
	// sessions that survived the last run rather than an empty server.
	s.manager.StartActivity(ctx)
	s.startSweep(ctx)
	return nil
}

// StopSessions lets go of everything Socrates owns and nothing tmux owns: the
// sessions themselves keep running, which is the whole point of them.
func (s *Server) StopSessions() {
	// The test runs go on at the next start; their sessions keep running.
	if s.tests != nil {
		s.tests.Close()
	}
	// The hosts keep running, like the tmux server; only the connections go.
	s.chats.close()
	s.push.stop()
	if err := s.manager.Close(); err != nil {
		log.Printf("terminal sessions: %v", err)
	}
}

// Sessions is the terminal manager, for the transport that attaches to it.
func (s *Server) Sessions() *termux.Manager { return s.manager }

// Settings returns a copy of the live configuration.
func (s *Server) Settings() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Server) saveSettings(next config.Settings) error {
	next.Normalize()
	before := s.Settings()
	previous := before.Tunnel
	if err := s.store.SetJSON(SettingsKey, next); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	s.reconcileTunnel(previous, next.Tunnel)
	// The outcomes switched off: the verdicts there are go with them, and a
	// notification waiting for one goes out without it.
	if s.digests != nil && before.OpenRouter.Outcomes && !next.OpenRouter.Outcomes {
		s.digests.clearOutcomes()
	}
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
	mux.HandleFunc("GET /costs", s.page("costs.html"))
	s.testRoutes(mux)
	mux.HandleFunc("GET /login", s.pageLogin)
	mux.HandleFunc("GET /setup", s.pageSetup)
	mux.Handle("GET /static/", http.StripPrefix("/static/", web.Static()))
	mux.HandleFunc("GET /favicon.png", web.Favicon)
	// iOS asks the root for the home screen icon in some flows whatever the
	// page's head says, and asks before anybody has signed in.
	mux.HandleFunc("GET /apple-touch-icon.png", web.AppleTouchIcon)
	mux.HandleFunc("GET /apple-touch-icon-precomposed.png", web.AppleTouchIcon)
	// The offline shell worker is deliberately outside the auth wall: it holds
	// no data, and it has to be reachable to be updated.
	mux.HandleFunc("GET /sw.js", web.ServiceWorker)

	// Session
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("/ide/{id}/", s.auth(s.handleVSCode))
	mux.HandleFunc("POST /api/ide", s.auth(s.handleFolderIDE))
	mux.HandleFunc("POST /api/ide/blank", s.auth(s.handleBlankIDE))

	// Terminal sessions
	mux.HandleFunc("GET /api/sessions", s.auth(s.handleListSessions))
	mux.HandleFunc("POST /api/sessions", s.auth(s.handleCreateSession))
	mux.HandleFunc("GET /api/sessions/{id}", s.auth(s.handleGetSession))
	mux.HandleFunc("PATCH /api/sessions/{id}", s.auth(s.handleRenameSession))
	mux.HandleFunc("PUT /api/sessions/{id}/workdir", s.auth(s.handleSetWorkdir))
	mux.HandleFunc("GET /api/sessions/{id}/mcp", s.auth(s.handleSessionMCP))
	// A new name, from a model, because somebody asked for one.
	mux.HandleFunc("POST /api/sessions/{id}/title", s.auth(s.handleRetitleSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.auth(s.endingViewers(s.handleDeleteSession)))
	mux.HandleFunc("POST /api/sessions/{id}/file", s.auth(s.handleSetStatus))
	mux.HandleFunc("POST /api/sessions/{id}/resume", s.auth(s.terminal(s.handleResumeSession)))
	mux.HandleFunc("POST /api/sessions/{id}/restart", s.auth(s.terminal(s.handleRestartSession)))
	// A chat's draft: what its composer holds (drafts.go).
	mux.HandleFunc("GET /api/sessions/{id}/draft", s.auth(s.handleGetDraft))
	mux.HandleFunc("PUT /api/sessions/{id}/draft", s.auth(s.handlePutDraft))
	mux.HandleFunc("POST /api/sessions/{id}/ack-resume", s.auth(s.handleAckResume))
	mux.HandleFunc("POST /api/sessions/{id}/read", s.auth(s.handleMarkRead))
	// What every conversation on this machine cost, for the costs page.
	mux.HandleFunc("GET /api/costs", s.auth(s.handleCosts))
	// What a terminal is doing, said out loud.
	mux.HandleFunc("POST /api/sessions/{id}/status", s.auth(s.handleSessionStatus))
	// Home: the event stream, and its hello as a poll.
	mux.HandleFunc("GET /api/events", s.auth(s.handleEvents))
	mux.HandleFunc("GET /api/home", s.auth(s.handleHome))
	mux.HandleFunc("GET /api/sessions/{id}/journal", s.auth(s.terminal(s.handleJournal)))
	mux.HandleFunc("POST /api/sessions/{id}/upload", s.auth(s.handleUpload))
	mux.HandleFunc("GET /api/sessions/{id}/ws", s.auth(s.terminal(s.handleSessionWS)))

	// Chats: agents driven through their protocols, by a host per chat.
	mux.HandleFunc("POST /api/chats", s.auth(s.handleCreateChat))
	mux.HandleFunc("GET /api/chats/{id}", s.auth(s.handleGetChat))
	mux.HandleFunc("POST /api/chats/{id}/messages", s.auth(s.handleChatMessage))
	mux.HandleFunc("POST /api/chats/{id}/interrupt", s.auth(s.handleChatInterrupt))
	mux.HandleFunc("POST /api/chats/{id}/tasks/{task}/stop", s.auth(s.handleChatStopTask))
	mux.HandleFunc("POST /api/chats/{id}/asks/{ask}/answer", s.auth(s.handleChatAnswer))
	mux.HandleFunc("GET /api/chats/{id}/stream", s.auth(s.handleChatStream))
	mux.HandleFunc("GET /api/chats/{id}/parts/{part}", s.auth(s.handleChatPart))
	mux.HandleFunc("GET /api/chats/{id}/parts/{part}/image", s.auth(s.handleChatPartImage))
	mux.HandleFunc("POST /api/chats/{id}/model", s.auth(s.handleChatModel))
	mux.HandleFunc("POST /api/chats/{id}/command", s.auth(s.handleChatCommand))
	mux.HandleFunc("POST /api/chats/{id}/attachments", s.auth(s.handleChatAttachments))
	mux.HandleFunc("POST /api/chats/{id}/options", s.auth(s.handleChatOptions))
	mux.HandleFunc("POST /api/chats/{id}/mcp", s.auth(s.handleChatMCP))
	// The MCP servers a chat about to be made would have, for the sheet.
	mux.HandleFunc("GET /api/mcp", s.auth(s.handleNewChatMCP))
	// The MCP servers built into Socrates, their accounts (mcpbuiltin.go).
	mux.HandleFunc("GET /api/mcp/builtin", s.auth(s.handleBuiltinMCP))
	// Testing a server, its sign-in, and the proxy that adds the sign-in's
	// token for an agent (mcpadmin.go, mcpoauth.go).
	mux.HandleFunc("GET /api/mcp/overview", s.auth(s.handleMCPOverview))
	mux.HandleFunc("POST /api/mcp/test", s.auth(s.handleMCPTest))
	mux.HandleFunc("POST /api/sessions/{id}/mcp/test", s.auth(s.handleChatMCPTest))
	mux.HandleFunc("POST /api/mcp/oauth/start", s.auth(s.handleMCPOAuthStart))
	mux.HandleFunc("GET /api/mcp/oauth/callback", s.auth(s.handleMCPOAuthCallback))
	mux.HandleFunc("GET /api/mcp/oauth", s.auth(s.handleMCPOAuthStatus))
	mux.HandleFunc("DELETE /api/mcp/oauth", s.auth(s.handleMCPOAuthDisconnect))
	mux.HandleFunc("/mcp-proxy/{name}", s.handleMCPProxy)
	mux.HandleFunc("POST /api/mcp/builtin/{name}/accounts", s.auth(s.handleBuiltinMCPAccountAdd))
	mux.HandleFunc("POST /api/mcp/builtin/{name}/accounts/{id}/check", s.auth(s.handleBuiltinMCPAccountCheck))
	mux.HandleFunc("DELETE /api/mcp/builtin/{name}/accounts/{id}", s.auth(s.handleBuiltinMCPAccountDelete))
	mux.HandleFunc("POST /api/chats/{id}/account", s.auth(s.handleChatAccount))
	mux.HandleFunc("POST /api/chats/{id}/spoken", s.auth(s.handleChatSpoken))
	mux.HandleFunc("PUT /api/chats/{id}/auto-switch", s.auth(s.handleChatAutoSwitch))
	// The chats' clock (schedule.go).
	mux.HandleFunc("PUT /api/chats/{id}/start_at", s.auth(s.handleChatStartAt))
	mux.HandleFunc("PUT /api/chats/{id}/auto-continue", s.auth(s.handleChatAutoContinue))

	// The harness catalogue: what can be started, and where.
	mux.HandleFunc("GET /api/harnesses", s.auth(s.handleHarnesses))
	mux.HandleFunc("POST /api/harnesses/refresh", s.auth(s.handleRefreshHarnesses))
	mux.HandleFunc("GET /api/accounts", s.auth(s.handleListAccounts))
	mux.HandleFunc("POST /api/accounts", s.auth(s.handleCreateAccount))
	mux.HandleFunc("PUT /api/accounts/order", s.auth(s.handleReorderAccounts))
	mux.HandleFunc("PATCH /api/accounts/{id}", s.auth(s.handlePatchAccount))
	mux.HandleFunc("DELETE /api/accounts/{id}", s.auth(s.handleDeleteAccount))
	mux.HandleFunc("POST /api/accounts/{id}/signin", s.auth(s.handleAccountSignIn))
	mux.HandleFunc("GET /api/accounts/{id}/signin", s.auth(s.handleGetAccountSignIn))
	mux.HandleFunc("POST /api/accounts/{id}/signin/code", s.auth(s.handleAccountSignInCode))
	mux.HandleFunc("DELETE /api/accounts/{id}/signin", s.auth(s.handleCancelAccountSignIn))

	// Admin
	mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	mux.HandleFunc("POST /api/settings/password", s.auth(s.handleChangePassword))
	mux.HandleFunc("GET /api/preferences", s.auth(s.handlePreferences))
	mux.HandleFunc("POST /api/diagnostics", s.auth(s.handleDiagnostics))

	// The terminal engine: is tmux here and can it run a session.
	mux.HandleFunc("GET /api/tmux", s.auth(s.handleTmux))

	// Updates: what the updater did last, and a run started now.
	mux.HandleFunc("GET /api/updates", s.auth(s.handleUpdates))
	mux.HandleFunc("POST /api/updates/run", s.auth(s.handleUpdatesRun))

	// Cloudflare tunnel
	mux.HandleFunc("GET /api/tunnel", s.auth(s.handleTunnelStatus))
	mux.HandleFunc("POST /api/tunnel/start", s.auth(s.handleTunnelStart))
	mux.HandleFunc("POST /api/tunnel/stop", s.auth(s.handleTunnelStop))
	mux.HandleFunc("POST /api/tunnel/install", s.auth(s.handleTunnelInstall))

	// Voice
	mux.HandleFunc("POST /api/voice/transcribe", s.auth(s.handleTranscribe))
	mux.HandleFunc("POST /api/voice/speak", s.auth(s.handleSpeak))
	mux.HandleFunc("POST /api/voice/check", s.auth(s.handleVoiceCheck))

	// Web Push
	mux.HandleFunc("GET /api/push", s.auth(s.handlePushKey))
	mux.HandleFunc("POST /api/push/subscribe", s.auth(s.handlePushSubscribe))
	mux.HandleFunc("POST /api/push/unsubscribe", s.auth(s.handlePushUnsubscribe))
	mux.HandleFunc("POST /api/push/test", s.auth(s.handlePushTest))

	// Backup: the Backup card (backup.go).
	mux.HandleFunc("GET /api/backup", s.auth(s.handleBackupStatus))
	mux.HandleFunc("POST /api/backup/run", s.auth(s.handleBackupRun))
	mux.HandleFunc("GET /api/backup/snapshots", s.auth(s.handleBackupSnapshots))
	mux.HandleFunc("GET /api/backup/legacy", s.auth(s.handleBackupLegacy))
	mux.HandleFunc("DELETE /api/backup/legacy", s.auth(s.handleBackupLegacyDelete))
	mux.HandleFunc("POST /api/backup/test", s.auth(s.handleBackupTest))
	mux.HandleFunc("PUT /api/backup/config", s.auth(s.handleBackupConfig))
	mux.HandleFunc("DELETE /api/backup/config", s.auth(s.handleBackupOff))
	// Restore: a new machine, from the setup page, before there is a
	// password (backup.go says when it is allowed).
	mux.HandleFunc("POST /api/restore/snapshots", s.handleRestoreSnapshots)
	mux.HandleFunc("POST /api/restore", s.handleRestoreStart)
	mux.HandleFunc("GET /api/restore", s.handleRestoreStatus)

	// Liveness, and the one endpoint nothing in the browser calls:
	// scripts/update.sh asks it after a restart, because a process that is
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

// compressFrom is the smallest answer worth compressing.
const compressFrom = 1 << 10

// writeJSONCompressed is writeJSON gzipped for a client that takes it, for
// the answers that carry every session: Home's state is polled every few
// seconds by a page whose stream is down, which is a page on a bad link, and
// sixty sessions with their digests are a hundred kilobytes as text and a
// tenth of that compressed.
func writeJSONCompressed(w http.ResponseWriter, r *http.Request, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the answer could not be encoded")
		return
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Add("Vary", "Accept-Encoding")
	if len(body) < compressFrom || !acceptsGzip(r) {
		w.WriteHeader(code)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(code)
	zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if _, err := zw.Write(body); err != nil {
		log.Printf("server: write json: %v", err)
	}
	_ = zw.Close()
}

// acceptsGzip is whether a request's Accept-Encoding takes gzip, and does not
// refuse it with q=0.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); ok {
			weight, err := strconv.ParseFloat(q, 64)
			return err == nil && weight > 0
		}
		return true
	}
	return false
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// sleepFor waits d, or less when ctx ends first, which it then answers with.
func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
