package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
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
// that folder's editor: the one it already has, or a new one. There is only
// ever one editor per folder - it is a window onto the files, and two of them
// would only disagree about which files are open.
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
		// A prepared session has not worked in its folder yet, and a
		// dynamic one does not even have it.
		if row.State == store.StatePrestart || filepath.Clean(row.Workdir) != folder {
			continue
		}
		// Newest first, so a folder that somehow has two editors opens the
		// one used last.
		if row.Harness == config.HarnessVSCode && row.Status != store.StatusDone {
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
	s.startIDE(w, r, harnesses.VSCodeSessionID(folder), func(_ config.Settings, label string) (string, string, string, error) {
		return folder, source.WorkdirMode, label + " · " + filepath.Base(folder), nil
	})
}

// handleBlankIDE answers the VS Code mark at the top of the sidebar with an
// editor that has no folder open: the one there is, or a new one. Like a
// folder's editor there is only ever one, and every tab it is opened in is
// another window onto it.
//
//	POST /api/ide/blank   200 {"session":<session>} for the editor it has
//	                      201 {"session":<session>} for a new one
//	                      201 {"session":<session>,"error":"…"} when it
//	                          could not start
//
// It works in a directory of its own in the workspace, the way a dynamic
// session does, so its terminals start somewhere that is nobody's project and
// closing it takes nothing with it but that directory.
func (s *Server) handleBlankIDE(w http.ResponseWriter, r *http.Request) {
	s.ideMu.Lock()
	defer s.ideMu.Unlock()
	id := harnesses.VSCodeBlankID
	if row, err := s.store.GetSession(id); err == nil && row.Status != store.StatusDone {
		writeJSON(w, http.StatusOK, map[string]any{"session": s.view(row)})
		return
	}
	s.startIDE(w, r, id, func(settings config.Settings, label string) (string, string, string, error) {
		dir, err := harnesses.ResolveWorkdir(settings, harnesses.WorkdirDynamic, "", config.HarnessVSCode, id)
		return dir, store.WorkdirDynamic, label, err
	})
}

// startIDE starts the editor under id, in the directory where answers, and
// answers the request with it. Both callers return a live editor before they
// get here, so a row already under the id is one somebody filed as done. It
// makes way: the id is the editor's address, and what the editor remembers is
// not in the row. The caller holds ideMu.
func (s *Server) startIDE(w http.ResponseWriter, r *http.Request, id string, where func(settings config.Settings, label string) (dir, mode, title string, err error)) {
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
	if _, err := s.store.GetSession(id); err == nil {
		if err := s.deleteSession(ctx, id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	folder, mode, title, err := where(settings, h.Label())
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
		writeJSON(w, http.StatusCreated, map[string]any{"session": s.view(row), "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": s.view(row)})
}
