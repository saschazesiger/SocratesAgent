package server

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/artifacts"
	artifactsmcp "github.com/saschazesiger/SocratesAgent/internal/mcpserver/artifacts"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Chat artifacts (docs/design/ARTIFACTS.md): a chat with Socrates Artifacts
// on makes pages that show what chat text cannot, kept in its folder
// (internal/artifacts). The agent makes them through the server's tools
// (internal/mcpserver/artifacts); the chat's page lists and deletes them
// here, every change is an artifacts frame on /api/events, and a page is
// served at its own address, sandboxed, with the files it uses beside it.

// artifactPageCSP makes a page a sandbox with an opaque origin wherever it
// is shown, a frame of the app or a tab of its own: its scripts run, and
// may load from the internet, but nothing they ask of Socrates carries the
// person's cookie, and the API refuses their origin.
const artifactPageCSP = "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals allow-downloads; frame-ancestors 'self'"

// newArtifacts is the store of every chat's artifacts, in the chat's
// folder: they go with the chat and are backed up with it.
func (s *Server) newArtifacts() *artifacts.Store {
	st := artifacts.New(func(chat string) string { return agenthost.ChatDir(s.dataDir, chat) })
	st.SetOnChange(func(chat, id, what string) {
		rev, _ := st.Rev(chat)
		s.events.publish(map[string]any{"t": "artifacts", "session": chat, "rev": rev, "artifact": id, "what": what})
	})
	return st
}

// artifactURL is the address of an artifact's page.
func artifactURL(chat string, a artifacts.Artifact) string {
	return "/artifacts/" + chat + "/" + a.ID + "/" + a.Key + "/"
}

// handleChatArtifacts lists a chat's artifacts:
//
//	GET /api/chats/{id}/artifacts  {"artifacts": [...], "rev", "on"}
func (s *Server) handleChatArtifacts(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	list, err := s.artifacts.List(row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]artifacts.ViewArtifact, len(list))
	for i, a := range list {
		views[i] = artifacts.View(a, artifactURL(row.ID, a))
	}
	rev, _ := s.artifacts.Rev(row.ID)
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": views, "rev": rev, "on": s.chatHasMCPOn(row, artifactsmcp.Name)})
}

// handleChatArtifactDelete deletes an artifact with its versions and files:
//
//	DELETE /api/chats/{id}/artifacts/{artifact}
func (s *Server) handleChatArtifactDelete(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	if _, err := s.artifacts.Delete(row.ID, r.PathValue("artifact")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleArtifact serves an artifact's page, or a file of it:
//
//	GET /artifacts/{id}/{artifact}/{key}/          the page (?v=3: version 3)
//	GET /artifacts/{id}/{artifact}/{key}/{file}    a file the page uses
//
// The page needs the person's login and the key; a file needs the key
// only, since the page's own requests come from an opaque origin and carry
// no cookie.
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	chat, id, key, file := r.PathValue("id"), r.PathValue("artifact"), r.PathValue("key"), r.PathValue("file")
	if file != "" {
		s.serveArtifactFile(w, r, chat, id, key, file)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.hasPassword() || !s.authenticated(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "Sign in to Socrates, then open this page again.\n")
		return
	}
	a, err := s.artifacts.ByKey(chat, id, key)
	if err != nil {
		http.Error(w, "There is no such artifact.", http.StatusNotFound)
		return
	}
	version := 0
	if v := r.URL.Query().Get("v"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "The version is a number from 1.", http.StatusBadRequest)
			return
		}
		version = n
	}
	a, v, content, err := s.artifacts.Content(chat, a.ID, version)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	page := artifacts.Page(a, v, content, web.Version())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", artifactPageCSP)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(page)
	}
}

// serveArtifactFile serves a file of an artifact as its type by name. A
// file opened by itself (an SVG, an HTML file) is a sandbox that runs
// nothing; any origin may read it, as the page's opaque one must.
func (s *Server) serveArtifactFile(w http.ResponseWriter, r *http.Request, chat, id, key, name string) {
	path, err := s.artifacts.FilePath(chat, id, key, name)
	if err != nil {
		http.Error(w, "There is no such file.", http.StatusNotFound)
		return
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > artifacts.MaxFileSize {
		http.Error(w, "The file is no longer there.", http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "The file is no longer there.", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		http.Error(w, "The file is no longer there.", http.StatusNotFound)
		return
	}
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if ctype == "" {
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "The file could not be read.", http.StatusInternalServerError)
			return
		}
		ctype = http.DetectContentType(head[:n])
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
