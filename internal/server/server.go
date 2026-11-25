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
	"github.com/saschazesiger/SocratesAgent/internal/chattests"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/github"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/repos"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/web"
	zdash "github.com/saschazesiger/Z-Dash/server"
)

// Version is stamped at build time.
var Version = "dev"

// SettingsKey is where the settings document lives in the key/value table.
const SettingsKey = "settings"

// Server wires storage, the terminal sessions and the HTTP handlers together.
type Server struct {
	store *store.Store
	// app is the Z-Dash server: the listeners, the password and the
	// sessions, the guards and the Cloudflare tunnel (app.go). pages is
	// what it serves at /, sandboxed the chats' pages outside the login.
	app       *zdash.App
	pages     *http.ServeMux
	sandboxed *http.ServeMux
	// serving is closed by the first health check this process answers
	// (Serving).
	serving     chan struct{}
	servingOnce sync.Once
	manager     *termux.Manager
	catalog     *catalog.Catalog

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

	// wsRate is the per address ceiling on WebSocket handshakes, guarded by
	// loginMu.
	loginMu sync.Mutex
	wsRate  map[string]*attempt

	// ideMu makes finding or creating the editor of a folder one step, so
	// two taps on the same folder cannot start two editors on it.
	ideMu sync.Mutex
	// ides is what is known of the editors' open tabs, which is what decides
	// when an editor is closed (vscode.go, reapIDEs).
	ides *ideUse

	// sched is the chats' clock: scheduled starts, the automatic continue
	// after a limit and the automatic retry after a failed turn (schedule.go,
	// retry.go).
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
	chats    *chatHub
	accounts *accountHub
	// mcp is the MCP servers built into Socrates (mcpbuiltin.go).
	mcp *mcpserver.Registry
	// chatTests is every chat's tests (chattests.go).
	chatTests *chattests.Store
	// docsKeep is the Docs view's token key and probe answers (docs.go).
	docsKeep docsState
	// linked keeps each chat's linked files a little while (chatfile.go).
	linked linkedState
	// The sign-ins of Admin's OAuth MCP servers (mcpoauth.go) and the last
	// test of each (mcpadmin.go), made when first used.
	oauthOnce sync.Once
	oauthKeep *oauthHub
	probeOnce sync.Once
	probeKeep *probeCache
	// github is the GitHub App's connection, and githubFlow the manifest
	// flows under way (github.go).
	ghApp      *github.Service
	githubFlow githubFlow

	// ghState is GitHub and the repositories' setup scripts from Admin
	// (UseGitHub, github), unset while GitHub is not connected; repos is the
	// caches and worktrees of the chats in repositories (repos.go).
	ghState atomic.Pointer[githubState]
	repos   *repos.Manager
}

type attempt struct {
	count int
	until time.Time
}

// New builds a server around an open store. dataDir is where Socrates keeps
// its own files, including a cloudflared it downloads itself; addr is the
// local address Run listens on.
func New(st *store.Store, dataDir, addr string) (*Server, error) {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	s := &Server{
		store:        st,
		dataDir:      dataDir,
		hub:          newTermHub(),
		serving:      make(chan struct{}),
		pingEvery:    defaultPingEvery,
		pingTimeout:  defaultPingTimeout,
		writeTimeout: defaultWriteTimeout,
		ides:         newIDEUse(),
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
	s.repos = s.newRepoManager()
	s.chats = newChatHub(s)
	// A chat's push says what its journal says.
	// A failed turn the clock will try again is no news yet (retry.go).
	s.push.chatAsk = s.chats.askWords
	s.push.chatTurn = func(id string) (string, bool) {
		if s.willRetry(id) {
			return "", false
		}
		return s.chats.turnWords(id)
	}
	s.accounts = newAccountHub(s)
	s.chatTests = s.newChatTests()
	s.mcp = s.newMCPRegistry()
	s.ghApp = s.newGitHub()
	s.useGitHubApp()
	manager, err := termux.New(st, cfg)
	if err != nil {
		return nil, err
	}
	s.manager = manager
	s.catalog = catalog.New(st, s.Settings)
	// An update that replaced a program's binary makes the catalogue's
	// versions stale; the updater's status file is how it finds out.
	s.catalog.WatchUpdates(filepath.Join(dataDir, updatesFile))
	// OpenCode's models are its accounts' providers'.
	s.catalog.UseAccounts(catalogAccounts{s})

	app, err := s.newApp(addr)
	if err != nil {
		return nil, err
	}
	s.app = app
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
	// Test mode's sessions and runs go before the chats come back, so that
	// none of its sessions is taken for a chat (chattests.go).
	s.forgetTestMode(ctx)
	// The chats' artifacts went with Socrates Artifacts, which their pages
	// folder replaced (pages.go).
	s.forgetArtifacts()
	// Chats need no tmux: their hosts are reattached whatever the terminal
	// engine says below, and so is their clock, once every chat is known.
	s.chats.start()
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
	s.startIDEReaper(ctx)
	return nil
}

// StopSessions lets go of everything Socrates owns and nothing tmux owns: the
// sessions themselves keep running, which is the whole point of them.
func (s *Server) StopSessions() {
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
	if err := s.store.SetJSON(SettingsKey, next); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	// The outcomes switched off: the verdicts there are go with them, and a
	// notification waiting for one goes out without it.
	if s.digests != nil && before.OpenRouter.Outcomes && !next.OpenRouter.Outcomes {
		s.digests.clearOutcomes()
	}
	return nil
}

// SetLocalURL records the loopback address Socrates listens on: what its own
// agents and tools call (MCP proxy, hooks); the Cloudflare tunnel reaches
// Socrates through a socket of its own instead (TunnelSocket).
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

// routes registers everything Socrates serves. The Z-Dash server brings its
// own beside them: /api/login, /api/logout, /api/password, /api/session, the
// tunnel's API under /api/cloudflare and Z-Dash itself under /z-dash/.
//
//   - private is a route of a signed-in person. The server checks the session
//     and its CSRF guard (the header X-Z-Dash: 1 on every request that is not
//     a GET, which api.js sends, and an Origin that is this host).
//   - public is one anybody on the local address may call, behind the same
//     CSRF guard: the setup page before there is a password.
//   - pages are served at /, behind the Host check only: each gates itself.
//   - sandboxed are the chats' pages, which Cloudflare Access lets through
//     without a login and which guard themselves with a token.
func (s *Server) routes() {
	pages := http.NewServeMux()
	sandboxed := http.NewServeMux()
	private := func(pattern string, h http.HandlerFunc) { s.app.HandleFunc(pattern, s.auth(h)) }
	public := func(pattern string, h http.HandlerFunc) { s.app.Public(pattern, open(h)) }

	// Pages
	pages.HandleFunc("GET /{$}", s.page("index.html"))
	pages.HandleFunc("GET /admin", s.page("admin.html"))
	pages.HandleFunc("GET /costs", s.page("costs.html"))
	pages.HandleFunc("GET /login", s.pageLogin)
	pages.HandleFunc("GET /setup", s.pageSetup)
	pages.Handle("GET /static/", http.StripPrefix("/static/", web.Static()))
	pages.HandleFunc("GET /favicon.png", web.Favicon)
	// iOS asks the root for the home screen icon in some flows whatever the
	// page's head says, and asks before anybody has signed in.
	pages.HandleFunc("GET /apple-touch-icon.png", web.AppleTouchIcon)
	pages.HandleFunc("GET /apple-touch-icon-precomposed.png", web.AppleTouchIcon)
	// The offline shell worker is deliberately outside the auth wall: it holds
	// no data, and it has to be reachable to be updated.
	pages.HandleFunc("GET /sw.js", web.ServiceWorker)

	// Session
	public("GET /api/state", s.handleState)
	public("POST /api/setup", s.handleSetup)

	// The editor is a page with requests of its own, which carry no CSRF
	// header: it is served like a page and checks the session itself.
	pages.HandleFunc("/ide/{id}/", s.signedIn(s.handleVSCode))
	private("POST /api/ide", s.handleFolderIDE)
	private("POST /api/ide/blank", s.handleBlankIDE)

	// Terminal sessions
	private("GET /api/sessions", s.handleListSessions)
	private("POST /api/sessions", s.handleCreateSession)
	private("GET /api/sessions/{id}", s.handleGetSession)
	private("PATCH /api/sessions/{id}", s.handleRenameSession)
	private("PUT /api/sessions/{id}/workdir", s.handleSetWorkdir)
	private("GET /api/sessions/{id}/mcp", s.handleSessionMCP)
	// A new name, from a model, because somebody asked for one.
	private("POST /api/sessions/{id}/title", s.handleRetitleSession)
	private("DELETE /api/sessions/{id}", s.endingViewers(s.handleDeleteSession))
	private("POST /api/sessions/{id}/file", s.handleSetStatus)
	private("POST /api/sessions/{id}/resume", s.terminal(s.handleResumeSession))
	private("POST /api/sessions/{id}/restart", s.terminal(s.handleRestartSession))
	// A chat's draft: what its composer holds (drafts.go).
	private("GET /api/sessions/{id}/draft", s.handleGetDraft)
	private("PUT /api/sessions/{id}/draft", s.handlePutDraft)
	private("POST /api/sessions/{id}/ack-resume", s.handleAckResume)
	private("POST /api/sessions/{id}/read", s.handleMarkRead)
	// What every conversation on this machine cost, for the costs page.
	private("GET /api/costs", s.handleCosts)
	// A project folder's board of notes (boards.go).
	private("GET /api/github/repos/{owner}/{name}/branches", s.handleRepoBranches)

	private("GET /api/boards", s.handleGetBoard)
	private("PUT /api/boards", s.handlePutBoard)
	// What a terminal is doing, said out loud.
	private("POST /api/sessions/{id}/status", s.handleSessionStatus)
	// A question about a passage of a chat, answered beside it (chatask.go).
	private("POST /api/sessions/{id}/ask", s.handleSessionAsk)
	// Home: the event stream, and its hello as a poll. (/api/events is the
	// Z-Dash server's own stream, which Socrates' pages do not use.)
	private("GET /api/stream", s.handleEvents)
	private("GET /api/home", s.handleHome)
	private("GET /api/sessions/{id}/journal", s.terminal(s.handleJournal))
	private("POST /api/sessions/{id}/upload", s.handleUpload)
	private("GET /api/sessions/{id}/ws", s.terminal(s.handleSessionWS))

	// Chats: agents driven through their protocols, by a host per chat.
	private("POST /api/chats", s.handleCreateChat)
	private("GET /api/chats/{id}", s.handleGetChat)
	private("POST /api/chats/{id}/messages", s.handleChatMessage)
	private("POST /api/chats/{id}/interrupt", s.handleChatInterrupt)
	private("POST /api/chats/{id}/tasks/{task}/stop", s.handleChatStopTask)
	private("POST /api/chats/{id}/messages/{message}/withdraw", s.handleChatWithdraw)
	private("POST /api/chats/{id}/asks/{ask}/answer", s.handleChatAnswer)
	private("GET /api/chats/{id}/stream", s.handleChatStream)
	private("GET /api/chats/{id}/events", s.handleChatEvents)
	private("GET /api/chats/{id}/parts/{part}", s.handleChatPart)
	private("GET /api/chats/{id}/parts/{part}/image", s.handleChatPartImage)
	private("GET /api/chats/{id}/image", s.handleChatShownImage)
	private("GET /api/chats/{id}/file", s.handleChatFile)
	private("GET /api/chats/{id}/file/info", s.handleChatFileInfo)
	private("GET /api/chats/{id}/files", s.handleChatLinkedFiles)
	private("GET /api/chats/{id}/attachments/{name}", s.handleChatAttachmentImage)
	private("POST /api/chats/{id}/model", s.handleChatModel)
	private("POST /api/chats/{id}/command", s.handleChatCommand)
	private("POST /api/chats/{id}/attachments", s.handleChatAttachments)
	private("POST /api/chats/{id}/options", s.handleChatOptions)
	private("POST /api/chats/{id}/mcp", s.handleChatMCP)
	private("GET /api/chats/{id}/tests", s.handleChatTests)
	private("GET /api/chats/{id}/tests/{test}", s.handleChatTest)
	private("PATCH /api/chats/{id}/tests/{test}", s.handleChatTestPatch)
	private("DELETE /api/chats/{id}/tests/{test}", s.handleChatTestDelete)
	private("POST /api/chats/{id}/tests/{test}/override", s.handleChatTestOverride)
	private("POST /api/chats/{id}/tests/{test}/rerun", s.handleChatTestRerun)
	private("GET /api/chats/{id}/tests/{test}/files/{file...}", s.handleChatTestFile)
	// A project folder's docs, and a chat's pages folder (docs.go): which
	// folders have any, a folder's Markdown, one file's time, and the pages,
	// outside the login with a token of their own (their requests carry no
	// cookie), with the kit that renders them.
	sandboxed.Handle("GET /artifact-kit/{name}", web.ArtifactKit())
	private("POST /api/docs/probe", s.handleDocsProbe)
	private("GET /api/docs", s.handleDocs)
	private("GET /api/docs/stat", s.handleDocsStat)
	private("GET /api/docs/locate", s.handleDocsLocate)
	sandboxed.HandleFunc("GET /docs/{token}/{path...}", s.handleDocsPage)
	// The MCP servers a chat about to be made would have, for the sheet.
	private("GET /api/mcp", s.handleNewChatMCP)
	// The MCP servers built into Socrates, their accounts (mcpbuiltin.go).
	private("GET /api/mcp/builtin", s.handleBuiltinMCP)
	// Testing a server, its sign-in, and the proxy that adds the sign-in's
	// token for an agent (mcpadmin.go, mcpoauth.go).
	private("GET /api/mcp/overview", s.handleMCPOverview)
	private("POST /api/mcp/test", s.handleMCPTest)
	private("POST /api/sessions/{id}/mcp/test", s.handleChatMCPTest)
	private("POST /api/mcp/oauth/start", s.handleMCPOAuthStart)
	private("GET /api/mcp/oauth/callback", s.handleMCPOAuthCallback)
	private("GET /api/mcp/oauth", s.handleMCPOAuthStatus)
	private("DELETE /api/mcp/oauth", s.handleMCPOAuthDisconnect)
	pages.HandleFunc("/mcp-proxy/{name}", s.handleMCPProxy)
	private("POST /api/mcp/builtin/{name}/accounts", s.handleBuiltinMCPAccountAdd)
	private("POST /api/mcp/builtin/{name}/accounts/{id}/check", s.handleBuiltinMCPAccountCheck)
	private("DELETE /api/mcp/builtin/{name}/accounts/{id}", s.handleBuiltinMCPAccountDelete)
	// GitHub: the App, its installations and repositories (github.go).
	private("GET /api/github", s.handleGitHub)
	private("POST /api/github/app/start", s.handleGitHubAppStart)
	private("GET /api/github/app/created", s.handleGitHubAppCreated)
	private("GET /api/github/app/install", s.handleGitHubAppInstall)
	public("GET /api/github/app/installed", s.handleGitHubAppInstalled)
	private("DELETE /api/github/app", s.handleGitHubAppDelete)
	private("POST /api/github/installations/{id}/accept", s.handleGitHubInstallationAccept)
	private("DELETE /api/github/installations/{id}", s.handleGitHubInstallationDelete)
	private("PUT /api/github/repos/{owner}/{name}/settings", s.handleGitHubRepoSettings)
	private("POST /api/chats/{id}/account", s.handleChatAccount)
	private("POST /api/chats/{id}/spoken", s.handleChatSpoken)
	private("PUT /api/chats/{id}/auto-switch", s.handleChatAutoSwitch)
	// The chats' clock (schedule.go).
	private("PUT /api/chats/{id}/start_at", s.handleChatStartAt)
	private("PUT /api/chats/{id}/auto-continue", s.handleChatAutoContinue)

	// The harness catalogue: what can be started, and where.
	private("GET /api/harnesses", s.handleHarnesses)
	private("GET /api/harnesses/folder", s.handleFolderHarnesses)
	private("POST /api/harnesses/refresh", s.handleRefreshHarnesses)
	private("GET /api/accounts", s.handleListAccounts)
	private("POST /api/accounts", s.handleCreateAccount)
	private("PUT /api/accounts/order", s.handleReorderAccounts)
	private("PATCH /api/accounts/{id}", s.handlePatchAccount)
	private("DELETE /api/accounts/{id}", s.handleDeleteAccount)
	private("POST /api/accounts/{id}/signin", s.handleAccountSignIn)
	private("GET /api/accounts/{id}/signin", s.handleGetAccountSignIn)
	private("POST /api/accounts/{id}/signin/code", s.handleAccountSignInCode)
	private("DELETE /api/accounts/{id}/signin", s.handleCancelAccountSignIn)

	// Admin
	private("GET /api/settings", s.handleGetSettings)
	private("PUT /api/settings", s.handlePutSettings)
	private("GET /api/preferences", s.handlePreferences)
	private("POST /api/diagnostics", s.handleDiagnostics)

	// The terminal engine: is tmux here and can it run a session.
	private("GET /api/tmux", s.handleTmux)

	// Updates: what the updater did last, and a run started now.
	private("GET /api/updates", s.handleUpdates)
	private("POST /api/updates/run", s.handleUpdatesRun)

	// Voice
	private("POST /api/voice/transcribe", s.handleTranscribe)
	private("POST /api/voice/speak", s.handleSpeak)
	private("POST /api/voice/check", s.handleVoiceCheck)

	// Web Push
	private("GET /api/push", s.handlePushKey)
	private("POST /api/push/subscribe", s.handlePushSubscribe)
	private("POST /api/push/unsubscribe", s.handlePushUnsubscribe)
	private("POST /api/push/test", s.handlePushTest)

	// Backup: the Backup card (backup.go).
	private("GET /api/backup", s.handleBackupStatus)
	private("POST /api/backup/run", s.handleBackupRun)
	private("GET /api/backup/snapshots", s.handleBackupSnapshots)
	private("GET /api/backup/legacy", s.handleBackupLegacy)
	private("DELETE /api/backup/legacy", s.handleBackupLegacyDelete)
	private("POST /api/backup/test", s.handleBackupTest)
	private("PUT /api/backup/config", s.handleBackupConfig)
	private("DELETE /api/backup/config", s.handleBackupOff)
	// Restore: a new machine, from the setup page, before there is a
	// password (backup.go says when it is allowed).
	public("POST /api/restore/snapshots", s.handleRestoreSnapshots)
	public("POST /api/restore", s.handleRestoreStart)
	public("GET /api/restore", s.handleRestoreStatus)

	// Liveness, and the one endpoint nothing in the browser calls:
	// scripts/update.sh asks it after a restart, because a process that is
	// running is not the same thing as a server that answers.
	// It answers whatever name it is asked by, like the scripts that ask it.
	s.app.Unguarded("GET /api/health", open(func(w http.ResponseWriter, r *http.Request) {
		s.servingOnce.Do(func() { close(s.serving) })
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
	}))

	s.pages, s.sandboxed = pages, sandboxed
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

// keptStatus is the status an answer goes out with. Socrates is reached
// through a Cloudflare tunnel, and Cloudflare puts its own error page in place
// of an origin's 502 or 504 - "error code: 502", whatever the server said - so
// "the push service refused it" reached the page as "HTTP 502". Those two are
// sent as a 500, which Cloudflare passes through with its body, and which the
// page retries the same way (net.js RETRYABLE_STATUS).
func keptStatus(code int) int {
	if code == http.StatusBadGateway || code == http.StatusGatewayTimeout {
		return http.StatusInternalServerError
	}
	return code
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(keptStatus(code))
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
	code = keptStatus(code)
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

// writeError answers with {"error": msg}, msg said as a sentence (sentence.go).
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": sentence(msg)})
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
