package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// imageMax is the largest image a part's file may be to be shown.
const imageMax = 32 << 20

// handleChatPartImage is the file an image part shows, GET
// /api/chats/{id}/parts/{part}/image: an image the agent made, viewed or got
// back from a tool, kept on this machine at the path the part names (Codex's
// generated images, Claude's tool images beside the journal). Only an image
// part's own file, and only when it is an image by its bytes: the path is
// the adapter's, never the browser's, and nothing else is served this way.
func (s *Server) handleChatPartImage(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var (
		part  agent.Event
		found bool
	)
	if err := p.view(func(proj *agent.Projection) { part, _, found = proj.Detail(r.PathValue("part")) }); err != nil {
		writeError(w, http.StatusInternalServerError, "the chat could not be read")
		return
	}
	var c struct {
		Path string `json:"path"`
	}
	if found {
		_ = json.Unmarshal(part.Content, &c)
	}
	if !found || part.Kind != agent.PartImage || c.Path == "" || !filepath.IsAbs(c.Path) {
		writeError(w, http.StatusNotFound, "the chat has no such image")
		return
	}
	serveImage(w, r, c.Path)
}

// handleChatAttachmentImage is an image a message of the chat carries, GET
// /api/chats/{id}/attachments/{name}: the file the upload route kept in the
// chat's attachments directory under that name (chatcompose.go), and only
// when it is an image by its bytes. The composer's message names it before
// the journal has it, so it is found by its name rather than by its message.
func (s *Server) handleChatAttachmentImage(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		writeError(w, http.StatusNotFound, "the chat has no such file")
		return
	}
	serveImage(w, r, filepath.Join(s.chatAttachments(row.ID), name))
}

// handleChatShownImage is an image the agent showed in what it wrote, GET
// /api/chats/{id}/image?path=...: a Markdown image (![what](path)) of a file
// on this machine, which the chat draws in place (md.js). The path is the
// one the Markdown names, absolute or relative to the chat's folder, and it
// is served only when a text of the agent's in this chat names it as it was
// asked for and it is an image by its bytes: the route shows what the agent
// showed, never any file a browser asks for.
func (s *Server) handleChatShownImage(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	raw := r.URL.Query().Get("path")
	path := strings.TrimPrefix(raw, "file://")
	if raw == "" || strings.ContainsRune(raw, 0) || len(raw) > 4096 {
		writeError(w, http.StatusNotFound, "the chat has no such image")
		return
	}
	if !filepath.IsAbs(path) {
		if row.Workdir == "" {
			writeError(w, http.StatusNotFound, "the chat has no such image")
			return
		}
		path = filepath.Join(row.Workdir, path)
	}
	named := false
	if err := p.view(func(proj *agent.Projection) { named = proj.AgentNames(raw) }); err != nil {
		writeError(w, http.StatusInternalServerError, "the chat could not be read")
		return
	}
	if !named {
		writeError(w, http.StatusNotFound, "the chat has no such image")
		return
	}
	serveImage(w, r, filepath.Clean(path))
}

// serveImage answers the image file at path. A regular file only, looked
// at before it is opened - opening a FIFO would wait for a writer - and
// again once open (O_NONBLOCK, should it have been swapped for one in
// between), the same file both times; and an image by its bytes, never by
// its name, so that nothing a page could run (an SVG, an HTML file named
// .png) is ever served this way.
func serveImage(w http.ResponseWriter, r *http.Request, path string) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > imageMax {
		writeError(w, http.StatusNotFound, "the image is no longer there")
		return
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "the image is no longer there")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > imageMax {
		writeError(w, http.StatusNotFound, "the image is no longer there")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	mime := http.DetectContentType(head[:n])
	if !strings.HasPrefix(mime, "image/") {
		writeError(w, http.StatusUnsupportedMediaType, "the file is not an image")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
