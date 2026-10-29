package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// A chat's MCP servers are the ones set up in the dashboard for every chat
// (config.MCPSettings) and its folder's .mcp.json files (agent.ReadFolderMCP),
// each on as the chat chose or else by its default (agent.ChatMCP). The
// chat's choices are its options' "mcp": {name: on}.

// sharedMCP is the dashboard's MCP servers as the agent package has them,
// and the servers built into Socrates. For a chat (chatID) a built-in server
// is the command that reaches it and a server signed in to with OAuth is
// Socrates' proxy to it (mcpoauth.go); without one they are only what a
// view lists.
func (s *Server) sharedMCP(settings config.Settings, chatID string) []agent.SharedMCP {
	var out []agent.SharedMCP
	for _, m := range settings.MCP.Servers {
		srv := agent.MCPServer{
			Name: m.Name, Transport: m.Type, Command: m.Command, Args: m.Args, Env: m.Env,
			URL: m.URL, Headers: m.Headers, ToolTimeout: m.ToolTimeout, DisabledTools: m.DisabledTools,
		}
		if m.Auth == config.MCPAuthOAuth && chatID != "" {
			srv = s.oauthProxied(srv, chatID)
		}
		out = append(out, agent.SharedMCP{Default: m.Default, MCPServer: srv})
	}
	return append(out, s.builtinMCP(settings, chatID)...)
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
	shared := p.hub.srv.sharedMCP(p.hub.srv.Settings(), p.id)
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
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Transport   string `json:"transport"`
	From        string `json:"from"`
	Default     bool   `json:"default"`
	On          bool   `json:"on"`
}

// chatMCPView is every MCP server a chat may have and what had to be left
// out, for GET and POST …/mcp.
func (s *Server) chatMCPView(row *store.Session) map[string]any {
	folder, problems := agent.ReadFolderMCP(row.Workdir)
	list, more := agent.ChatMCP(s.sharedMCP(s.Settings(), ""), folder, readMCPChoices(row.Options))
	problems = append(problems, more...)
	if problems == nil {
		problems = []string{}
	}
	return map[string]any{"servers": s.mcpViews(list), "problems": problems}
}

// chatHasMCP is whether name is one of the MCP servers a chat may have.
func (s *Server) chatHasMCP(row *store.Session, name string) bool {
	folder, _ := agent.ReadFolderMCP(row.Workdir)
	list, _ := agent.ChatMCP(s.sharedMCP(s.Settings(), ""), folder, nil)
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

// mcpViews is a list of ChatMCP as the browser sees it, with what Admin
// says each server is for.
func (s *Server) mcpViews(list []agent.MCPChoice) []mcpServerView {
	notes := map[string]string{}
	for _, m := range s.Settings().MCP.Servers {
		notes[m.Name] = m.Description
	}
	out := make([]mcpServerView, 0, len(list))
	for _, c := range list {
		v := mcpServerView{Name: c.Name, Transport: c.Transport, From: c.From, Default: c.Default, On: c.On}
		switch c.From {
		case agent.MCPFromAdmin:
			v.Description = notes[c.Name]
		case agent.MCPFromSocrates:
			if b := s.mcp.Get(c.Name); b != nil {
				v.Title, v.Description = b.Title(), b.Description()
			}
		}
		out = append(out, v)
	}
	return out
}

// newChatMCP is the MCP servers a chat about to be made in workdir may
// have: the dashboard's and those of the folder's .mcp.json files. A dynamic
// folder does not exist yet; it is looked at as the new folder it will be
// under the workspace root, which has no .mcp.json of its own but may sit in
// a repository that has one.
func (s *Server) newChatMCP(settings config.Settings, mode, workdir string) ([]agent.MCPChoice, []string) {
	dir, err := harnesses.CheckWorkdir(settings, mode, workdir)
	if err == nil && dir == "" {
		dir = filepath.Join(settings.Workspace.Root, "new")
	}
	var folder []agent.MCPServer
	var problems []string
	if err == nil {
		folder, problems = agent.ReadFolderMCP(dir)
	}
	list, more := agent.ChatMCP(s.sharedMCP(settings, ""), folder, nil)
	return list, append(problems, more...)
}

// newChatMCPChoices is what a new chat keeps of the MCP choices the sheet
// made: only a server it may have, and only where the choice is not that
// server's default anyway, so that a default changed in Admin later still
// counts for it - as a switch put back in the chat's MCP servers dialog
// does. A server the sheet named that has gone since is left out.
func newChatMCPChoices(list []agent.MCPChoice, chosen map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, c := range list {
		if on, ok := chosen[c.Name]; ok && on != c.Default {
			out[c.Name] = on
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleNewChatMCP is the MCP servers a chat would have in a folder, for
// the new session sheet, before the chat exists:
//
//	GET /api/mcp?workdir_mode=…&workdir=…
//	  {"servers":[{"name","transport","from","default","on"}…],"problems":["…"]}
//
// A folder the sheet cannot have yet (a path half typed) is no error here:
// the answer is the dashboard's servers, and the start says what is wrong
// with the folder.
func (s *Server) handleNewChatMCP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, problems := s.newChatMCP(s.Settings(), strings.TrimSpace(q.Get("workdir_mode")), strings.TrimSpace(q.Get("workdir")))
	if problems == nil {
		problems = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": s.mcpViews(list), "problems": problems})
}
