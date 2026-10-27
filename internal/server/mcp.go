package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// A chat's MCP servers are the ones set up in the dashboard for every chat
// (config.MCPSettings) and its folder's .mcp.json files (agent.ReadFolderMCP),
// each on as the chat chose or else by its default (agent.ChatMCP). The
// chat's choices are its options' "mcp": {name: on}.

// sharedMCP is the dashboard's MCP servers as the agent package has them.
func sharedMCP(settings config.Settings) []agent.SharedMCP {
	var out []agent.SharedMCP
	for _, s := range settings.MCP.Servers {
		out = append(out, agent.SharedMCP{Default: s.Default, MCPServer: agent.MCPServer{
			Name: s.Name, Transport: s.Type, Command: s.Command, Args: s.Args, Env: s.Env,
			URL: s.URL, Headers: s.Headers,
		}})
	}
	return out
}

// readMCPChoices is the MCP servers a row's options switch on or off by
// name.
func readMCPChoices(raw json.RawMessage) map[string]bool {
	var o struct {
		MCP map[string]bool `json:"mcp"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	if len(o.MCP) == 0 {
		return nil
	}
	return o.MCP
}

// mcpSpec puts the chat's MCP servers into its launch spec: the dashboard's,
// sealed for this chat since their environment and headers may hold a
// token, and the chat's choices. The folder's the host reads itself.
func (p *chatPeer) mcpSpec(row *store.Session, spec *agenthost.Spec) error {
	spec.MCPChoices = readMCPChoices(row.Options)
	shared := sharedMCP(p.hub.srv.Settings())
	if len(shared) == 0 {
		return nil
	}
	keys, err := p.hub.srv.accounts.keys()
	if err != nil {
		return err
	}
	spec.SharedMCP, err = agenthost.SealMCP(keys.Key(), p.id, shared)
	return err
}

// mcpServerView is one MCP server of a chat as the browser sees it: only
// what it is called, where it is from and whether it is on. What a server
// is started with stays on the machine.
type mcpServerView struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	From      string `json:"from"`
	Default   bool   `json:"default"`
	On        bool   `json:"on"`
}

// chatMCPView is every MCP server a chat may have and what had to be left
// out, for GET and POST …/mcp.
func (s *Server) chatMCPView(row *store.Session) map[string]any {
	folder, problems := agent.ReadFolderMCP(row.Workdir)
	list, more := agent.ChatMCP(sharedMCP(s.Settings()), folder, readMCPChoices(row.Options))
	problems = append(problems, more...)
	out := make([]mcpServerView, 0, len(list))
	for _, c := range list {
		out = append(out, mcpServerView{Name: c.Name, Transport: c.Transport, From: c.From, Default: c.Default, On: c.On})
	}
	if problems == nil {
		problems = []string{}
	}
	return map[string]any{"servers": out, "problems": problems}
}

// chatHasMCP is whether name is one of the MCP servers a chat may have.
func (s *Server) chatHasMCP(row *store.Session, name string) bool {
	folder, _ := agent.ReadFolderMCP(row.Workdir)
	list, _ := agent.ChatMCP(sharedMCP(s.Settings()), folder, nil)
	for _, c := range list {
		if c.Name == name {
			return true
		}
	}
	return false
}

// handleSessionMCP is the MCP servers a chat may have, for its Info and its
// MCP servers dialog:
//
//	GET /api/sessions/{id}/mcp
//	  {"servers":[{"name","transport","from":"admin"|"folder","default","on"}…],"problems":["…"]}
//
// Variables are not looked at here; a server whose variable the chat lacks
// is said in the chat when the agent starts.
func (s *Server) handleSessionMCP(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.chatMCPView(row))
}

// handleChatMCP switches one MCP server on or off in a chat, or gives it
// back to its default. Like Ultracode it is a launch setting of the CLI:
// the agent is parked as soon as it is idle and the next message resumes
// the conversation with it.
//
//	POST /api/chats/{id}/mcp   {"name":…,"on":bool|null}   the GET's answer
func (s *Server) handleChatMCP(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		On   *bool  `json:"on"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "which MCP server?")
		return
	}
	// A server is switched only where the chat has it; a choice is given
	// back to the default even for one that has gone since.
	if body.On != nil && !s.chatHasMCP(row, name) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("this chat has no MCP server %q", name))
		return
	}
	changed, err := s.store.UpdateChatOptions(row.ID, func(raw json.RawMessage) json.RawMessage {
		choices := readMCPChoices(raw)
		if choices == nil {
			choices = map[string]bool{}
		}
		if body.On == nil {
			delete(choices, name)
		} else {
			choices[name] = *body.On
		}
		if len(choices) == 0 {
			return withOption(raw, "mcp", nil)
		}
		return withOption(raw, "mcp", choices)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if changed {
		s.events.poke()
		s.chats.park(p.id)
	}
	next, err := s.store.GetSession(row.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.chatMCPView(next))
}
