package server

import (
	"context"
	"log"
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
		if filepath.Clean(row.Workdir) != folder {
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
	if err := s.manager.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	settings := s.Settings()
	if !settings.Harnesses.VSCode.Enabled {
		writeError(w, http.StatusBadRequest, "VS Code is switched off in the dashboard")
		return
	}
	h, _ := harnesses.Get(config.HarnessVSCode)
	id := harnesses.VSCodeSessionID(folder)
	ctx, cancel := context.WithTimeout(r.Context(), createTimeout)
	defer cancel()
	// A row under this id is this folder's editor, and it was not returned
	// above, so it is one somebody filed as done. It makes way: the id is the
	// folder's, and what the editor remembers is not in the row.
	if _, err := s.store.GetSession(id); err == nil {
		if err := s.deleteSession(ctx, id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	plan, err := h.Plan(ctx, harnesses.PlanRequest{
		SessionID: id,
		Cwd:       folder,
		Settings:  settings,
		DataDir:   s.dataDir,
		Logf:      log.Printf,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := s.manager.Create(ctx, termux.Spec{
		ID:          id,
		Title:       h.Label() + " · " + filepath.Base(folder),
		TitleSource: store.TitleUser,
		Harness:     config.HarnessVSCode,
		Workdir:     folder,
		WorkdirMode: source.WorkdirMode,
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
