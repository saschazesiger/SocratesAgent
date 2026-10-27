package server

import (
	"net/http"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// handleSessionMCP is the MCP servers a chat's folder gives its agent, as its
// .mcp.json files name them (agent.ReadFolderMCP), for the chat's Info:
//
//	GET /api/sessions/{id}/mcp  {"servers":[{"name":…,"transport":…}…],"problems":["…"]}
//
// Only names and transports: what the servers are started with stays on the
// machine. Variables are not looked at here; a server whose variable the
// chat lacks is said in the chat when the agent starts.
func (s *Server) handleSessionMCP(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	type server struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
	}
	servers, problems := agent.ReadFolderMCP(row.Workdir)
	out := make([]server, 0, len(servers))
	for _, sv := range servers {
		out = append(out, server{Name: sv.Name, Transport: sv.Transport})
	}
	if problems == nil {
		problems = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "problems": problems})
}
