package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The authenticated proxy also carries the IDE's WebSockets. code-server has
// no TCP listener: the only entrypoint is this route and its private socket.
func (s *Server) handleVSCode(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin IDE request refused")
		return
	}
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	if row.Harness != config.HarnessVSCode {
		http.NotFound(w, r)
		return
	}
	// Every request is a sign of life, and a WebSocket - the workbench holds
	// one for as long as its tab is open - is one until it closes.
	defer s.ides.use(row.ID, isWebSocket(r))()
	prefix := "/ide/" + row.ID
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", harnesses.VSCodeSocket(s.dataDir, row.ID))
	}}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "localhost"
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			pr.Out.URL.RawPath = ""
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "VS Code is not ready. Reload the editor in a moment, or restart the session.", http.StatusServiceUnavailable)
		},
	}
	proxy.ServeHTTP(w, r)
}

// handleFolderIDE answers the VS Code mark of a folder in the sidebar with
// that folder's editor: the one open on it now, or a new one. There is only
// ever one editor per folder at a time - it is a window onto the files, and
// two of them would only disagree about which files are open. Once it is
// closed it is gone (ideReaper), and the next one starts afresh.
//
//	POST /api/ide {"workdir":"…"}   200 {"session":<session>} for the editor it has
//	                                201 {"session":<session>} for a new one
//	                                201 {"session":<session>,"error":"…"} when it
//	                                    could not start
//
// Only a folder some session already works in can be opened. The sidebar
// draws nothing else, and it keeps this route from being a way to open an
// editor anywhere on the disk.
func (s *Server) handleFolderIDE(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workdir string `json:"workdir"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	folder := strings.TrimSpace(body.Workdir)
	if folder == "" || !filepath.IsAbs(folder) {
		writeError(w, http.StatusBadRequest, "a folder is an absolute path")
		return
	}
	folder = filepath.Clean(folder)

	s.ideMu.Lock()
	defer s.ideMu.Unlock()
	rows, err := s.store.ListSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var source *store.Session
	for i := range rows {
		row := &rows[i]
		if filepath.Clean(row.Workdir) != folder {
			continue
		}
		// Newest first, so a folder that somehow has two editors opens the
		// one used last.
		if row.Harness == config.HarnessVSCode && row.Status != store.StatusDone {
			// It is about to be opened in a tab, so it is in use.
			s.ides.use(row.ID, false)()
			writeJSON(w, http.StatusOK, map[string]any{"session": s.view(row)})
			return
		}
		if source == nil {
			source = row
		}
	}
	if source == nil {
		writeError(w, http.StatusNotFound, "no session works in that folder")
		return
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "the folder "+folder+" is gone")
		return
	}
	s.startIDE(w, r, func(_ config.Settings, _, label string) (string, string, string, error) {
		return folder, source.WorkdirMode, label + " · " + filepath.Base(folder), nil
	})
}

// handleBlankIDE answers the VS Code mark at the top of the sidebar with a
// new editor that has no folder open. Every tap is another one: it is a
// scratch editor, and it is gone once its tab is closed.
//
//	POST /api/ide/blank   201 {"session":<session>}
//	                      201 {"session":<session>,"error":"…"} when it
//	                          could not start
//
// It works in a directory of its own in the workspace, the way a dynamic
// session does, so its terminals start somewhere that is nobody's project and
// closing it takes nothing with it but that directory.
func (s *Server) handleBlankIDE(w http.ResponseWriter, r *http.Request) {
	s.ideMu.Lock()
	defer s.ideMu.Unlock()
	s.startIDE(w, r, func(settings config.Settings, id, label string) (string, string, string, error) {
		dir, err := harnesses.ResolveWorkdir(settings, harnesses.WorkdirDynamic, "", config.HarnessVSCode, id)
		return dir, store.WorkdirDynamic, label, err
	})
}

// startIDE starts a new editor, under an id of its own, in the directory
// where answers, and answers the request with it. The caller holds ideMu.
func (s *Server) startIDE(w http.ResponseWriter, r *http.Request, where func(settings config.Settings, id, label string) (dir, mode, title string, err error)) {
	if err := s.manager.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	settings := s.Settings()
	if !settings.Harnesses.VSCode.Enabled {
		writeError(w, http.StatusBadRequest, "VS Code is switched off in the dashboard")
		return
	}
	h, _ := harnesses.TerminalOf(config.HarnessVSCode)
	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	id := harnesses.NewVSCodeID()
	folder, mode, title, err := where(settings, id, h.Label())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := h.Plan(ctx, harnesses.PlanRequest{
		SessionID: id,
		Cwd:       folder,
		Settings:  settings,
		DataDir:   s.dataDir,
	})
	if err != nil {
		if mode == store.WorkdirDynamic {
			// It was made for this editor a moment ago and nothing has run in
			// it; os.Remove only removes it while that is true.
			_ = os.Remove(folder)
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// From now on the editor has until ideGrace to be opened in a tab.
	s.ides.use(id, false)()
	row, err := s.manager.Create(ctx, termux.Spec{
		ID:          id,
		Title:       title,
		TitleSource: store.TitleUser,
		Harness:     config.HarnessVSCode,
		Workdir:     folder,
		WorkdirMode: mode,
		Plan:        plan,
	})
	if err != nil {
		if row == nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"session": s.view(row), "error": sentence(err.Error())})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": s.view(row)})
}

// How long an editor is kept with no tab open on it: long enough for a
// reload, a restart of Socrates or a network that drops for a moment - the
// workbench connects again by itself - and short enough that a closed tab is
// a closed editor.
const ideGrace = time.Minute

// How often the reaper looks, and takes the settings of the editors that are
// open back into the profile.
const ideReapEvery = 15 * time.Second

// ideUse is what Socrates knows of the editors' tabs: how many WebSockets
// each has open through handleVSCode, and when it was last asked anything.
// It is memory only: after a restart every editor has ideGrace from the
// restart for its tabs to connect again.
type ideUse struct {
	mu    sync.Mutex
	start time.Time
	by    map[string]*ideSeen
	now   func() time.Time
}

type ideSeen struct {
	open int
	last time.Time
}

func newIDEUse() *ideUse {
	return &ideUse{start: time.Now(), by: map[string]*ideSeen{}, now: time.Now}
}

// use notes a request to editor id, and returns what ends it. A WebSocket
// keeps the editor in use until it ends.
func (u *ideUse) use(id string, socket bool) func() {
	u.mu.Lock()
	defer u.mu.Unlock()
	seen := u.by[id]
	if seen == nil {
		seen = &ideSeen{}
		u.by[id] = seen
	}
	seen.last = u.now()
	if socket {
		seen.open++
	}
	return func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		seen.last = u.now()
		if socket {
			seen.open--
		}
	}
}

// idle says whether editor id has had no tab on it for longer than ideGrace.
// An editor nothing is known of counts from the later of its creation and
// the start of this process.
func (u *ideUse) idle(id string, created time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	last := u.start
	if created.After(last) {
		last = created
	}
	if seen := u.by[id]; seen != nil {
		if seen.open > 0 {
			return false
		}
		if seen.last.After(last) {
			last = seen.last
		}
	}
	return u.now().Sub(last) > ideGrace
}

func (u *ideUse) forget(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.by, id)
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// startIDEReaper closes the editors nobody has open any more, every
// ideReapEvery until ctx ends.
func (s *Server) startIDEReaper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(ideReapEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			s.reapIDEs(ctx)
		}
	}()
}

// reapIDEs deletes every editor whose last tab closed more than ideGrace
// ago - deleteSession takes its settings into the profile first - and takes
// the settings of the others there now, so that an editor that is still open
// when the machine goes down has lost at most the last few seconds of them.
func (s *Server) reapIDEs(ctx context.Context) {
	rows, err := s.store.ListSessions()
	if err != nil {
		log.Printf("vscode: could not read the sessions: %v", err)
		return
	}
	for _, row := range rows {
		if row.Harness != config.HarnessVSCode {
			continue
		}
		// Under ideMu, so that an editor handleFolderIDE has just handed
		// out is in use by the time it is looked at here.
		s.ideMu.Lock()
		if !s.ides.idle(row.ID, time.UnixMilli(row.CreatedAt)) {
			s.ideMu.Unlock()
			if err := harnesses.SyncVSCodeProfile(s.dataDir, row.ID); err != nil {
				log.Printf("vscode: could not keep the settings of editor %s: %v", row.ID, err)
			}
			continue
		}
		one, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := s.deleteSession(one, row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("vscode: could not close editor %s: %v", row.ID, err)
		}
		cancel()
		s.ideMu.Unlock()
	}
}
