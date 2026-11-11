package server

import (
	"log"
	"os"
	"path/filepath"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// A chat's pages (paths.ChatPages): a folder of the chat's own,
// <data>/chats/<id>/pages, where its agent writes Markdown with its own file
// tools and which it links in the chat (agent.PagesGuidance). The Docs view
// (docs.go) lists and shows it like a project folder; a session's view names
// it (pages_dir). It is made when the chat is and whenever its agent starts
// (agenthost.MakePages), and goes with the chat's folder: backed up with it,
// deleted with it.

// chatPagesDir is a chat's pages folder as a session's view names it
// (pages_dir) and the Docs view is asked for it.
func (s *Server) chatPagesDir(id string) string {
	return agent.PagesDir(s.dataDir, id)
}

// chatOfPages is the chat whose pages folder folder is, spelt exactly as
// chatPagesDir spells it, and false when it is no existing chat's.
func (s *Server) chatOfPages(folder string) (string, bool) {
	if filepath.Base(folder) != paths.ChatPages {
		return "", false
	}
	id := filepath.Base(filepath.Dir(folder))
	if !agenthost.ValidChatID(id) || s.chatPagesDir(id) != folder {
		return "", false
	}
	row, err := s.store.GetSession(id)
	if err != nil || !row.IsChat() {
		return "", false
	}
	// The folder itself, never a link put in its place: that would serve
	// whatever it points at.
	if fi, err := os.Lstat(folder); err == nil && !fi.IsDir() {
		return "", false
	}
	return id, true
}

// forgetArtifacts deletes the chat artifacts of the built-in MCP server
// Socrates Artifacts, which the pages folder replaced: every chat's
// artifacts folder (paths.ChatArtifactsGone). It runs at every start and
// finds nothing after the first.
func (s *Server) forgetArtifacts() {
	dirs, err := filepath.Glob(filepath.Join(paths.New(s.dataDir).Path(paths.Chats), "*", paths.ChatArtifactsGone))
	if err != nil {
		log.Printf("pages: could not look for the chats' artifacts: %v", err)
		return
	}
	removed := 0
	for _, dir := range dirs {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("pages: could not delete the artifacts of %s: %v", filepath.Base(filepath.Dir(dir)), err)
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Printf("pages: deleted the artifacts of %d chats, which pages replaced", removed)
	}
}
