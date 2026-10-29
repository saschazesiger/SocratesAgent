package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	f, err := os.Open(c.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "the image is no longer there")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > imageMax {
		writeError(w, http.StatusNotFound, "the image is no longer there")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	mime := http.DetectContentType(head[:n])
	if !strings.HasPrefix(mime, "image/") {
		writeError(w, http.StatusUnsupportedMediaType, "the part's file is not an image")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
