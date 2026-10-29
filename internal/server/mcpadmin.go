package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpclient"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// Admin's MCP servers beyond their settings: a test that connects to a
// server as an agent would and says what it offers (its tools, prompts and
// resources, its name and version, how long it took), the outcome of the
// last test of each server, and their sign-ins (mcpoauth.go).

// probeTimeout bounds one test: a server npx installs first takes a while.
const probeTimeout = 90 * time.Second

// probeResult is a test of a server as the browser sees it.
type probeResult struct {
	OK bool `json:"ok"`
	// State is ok, auth (the server wants a sign-in) or error.
	State           string               `json:"state"`
	Error           string               `json:"error,omitempty"`
	At              int64                `json:"at"`
	LatencyMS       int64                `json:"latency_ms,omitempty"`
	ProtocolVersion string               `json:"protocol_version,omitempty"`
	ServerName      string               `json:"server_name,omitempty"`
	ServerTitle     string               `json:"server_title,omitempty"`
	ServerVersion   string               `json:"server_version,omitempty"`
	Instructions    string               `json:"instructions,omitempty"`
	Capabilities    []string             `json:"capabilities,omitempty"`
	Tools           []mcpclient.Tool     `json:"tools"`
	Prompts         []mcpclient.Prompt   `json:"prompts,omitempty"`
	Resources       []mcpclient.Resource `json:"resources,omitempty"`
	Stderr          string               `json:"stderr,omitempty"`
	OAuth           map[string]any       `json:"oauth,omitempty"`
}

// probeCache is the last test of each server of Admin, by name.
type probeCache struct {
	mu   sync.Mutex
	last map[string]probeResult
}

func (s *Server) probes() *probeCache {
	s.probeOnce.Do(func() { s.probeKeep = &probeCache{last: map[string]probeResult{}} })
	return s.probeKeep
}

// probe tests a server as a chat in dir would have it: ${VAR} expanded on
// Socrates' own environment, a sign-in's token added.
func (s *Server) probe(ctx context.Context, srv agent.MCPServer, auth, dir string) probeResult {
	out := probeResult{At: time.Now().UnixMilli(), Tools: []mcpclient.Tool{}}
	expanded, problems := agent.ExpandMCP([]agent.MCPServer{srv}, os.Environ())
	if len(expanded) == 0 {
		out.State, out.Error = "error", strings.Join(problems, " ")
		return out
	}
	e := expanded[0]
	target := mcpclient.Server{Transport: e.Transport, Command: e.Command, Args: e.Args, Env: e.Env, Dir: dir,
		URL: e.URL, Headers: map[string]string{}}
	for k, v := range e.Headers {
		target.Headers[k] = v
	}
	if auth == config.MCPAuthOAuth {
		out.OAuth = oauthView(s.oauthOf(e.URL))
		if token, err := s.oauthToken(ctx, e.URL, ""); err == nil {
			target.Headers["Authorization"] = "Bearer " + token
		}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	info, err := mcpclient.Probe(ctx, target)
	if info != nil {
		out.Stderr = info.Stderr
	}
	var needAuth *mcpclient.AuthRequiredError
	switch {
	case errors.As(err, &needAuth):
		out.State = "auth"
		out.Error = "The server wants a sign-in. Choose OAuth as its sign-in and connect it, or give it a token in a header."
		if auth == config.MCPAuthOAuth {
			out.Error = "The server wants a sign-in: connect it."
		}
		return out
	case err != nil:
		out.State, out.Error = "error", err.Error()
		return out
	}
	out.OK, out.State = true, "ok"
	out.LatencyMS = info.Latency.Milliseconds()
	out.ProtocolVersion, out.ServerName, out.ServerTitle, out.ServerVersion = info.ProtocolVersion, info.ServerName, info.ServerTitle, info.ServerVersion
	out.Instructions, out.Capabilities = info.Instructions, info.Capabilities
	if info.Tools != nil {
		out.Tools = info.Tools
	}
	out.Prompts, out.Resources = info.Prompts, info.Resources
	return out
}

// builtinProbe is a built-in server as a test of it says: its tools, no
// connection needed.
func builtinProbe(srv mcpserver.Server) probeResult {
	out := probeResult{OK: true, State: "ok", At: time.Now().UnixMilli(), ServerName: srv.Name(), ServerTitle: srv.Title(),
		Instructions: srv.Instructions(), Capabilities: []string{"tools"}, Tools: []mcpclient.Tool{}}
	for _, t := range srv.Tools() {
		out.Tools = append(out.Tools, mcpclient.Tool{Name: t.Name, Title: t.Title, Description: t.Description})
	}
	return out
}

// handleMCPTest tests a server as it is in Admin's form, saved or not, and
// keeps the outcome for the server's row.
//
//	POST /api/mcp/test   {"server": {name, type, command, args, env, url, headers, auth, …}}   probeResult
func (s *Server) handleMCPTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Server config.MCPServerSetting `json:"server"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	settings := config.MCPSettings{Servers: []config.MCPServerSetting{body.Server}}
	settings.Normalize()
	if len(settings.Servers) == 0 {
		writeError(w, http.StatusBadRequest, "there is no server to test")
		return
	}
	m := settings.Servers[0]
	if b := s.mcp.Get(m.Name); b != nil && m.Command == "" && m.URL == "" {
		writeJSON(w, http.StatusOK, builtinProbe(b))
		return
	}
	srv := agent.MCPServer{Name: m.Name, Transport: m.Type, Command: m.Command, Args: m.Args, Env: m.Env, URL: m.URL, Headers: m.Headers}
	res := s.probe(r.Context(), srv, m.Auth, s.Settings().Workspace.Root)
	if m.Name != "" {
		c := s.probes()
		c.mu.Lock()
		c.last[m.Name] = res
		c.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, res)
}

// handleChatMCPTest tests one of a chat's MCP servers in the chat's folder.
//
//	POST /api/sessions/{id}/mcp/test   {"name"}   probeResult
func (s *Server) handleChatMCPTest(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	folder, _ := agent.ReadFolderMCP(row.Workdir)
	list, _ := agent.ChatMCP(s.sharedMCP(s.Settings(), ""), folder, nil)
	for _, c := range list {
		if c.Name != body.Name {
			continue
		}
		if c.From == agent.MCPFromSocrates {
			if b := s.mcp.Get(c.Name); b != nil {
				writeJSON(w, http.StatusOK, builtinProbe(b))
				return
			}
		}
		auth := ""
		if c.From == agent.MCPFromAdmin {
			for _, m := range s.Settings().MCP.Servers {
				if m.Name == c.Name {
					auth = m.Auth
				}
			}
		}
		writeJSON(w, http.StatusOK, s.probe(r.Context(), c.MCPServer, auth, row.Workdir))
		return
	}
	writeError(w, http.StatusNotFound, "this chat has no MCP server "+body.Name)
}

// handleMCPOverview is what Admin shows about the MCP servers beside their
// settings: the built-in servers, the last test of each of Admin's, and the
// sign-ins of those that use OAuth.
//
//	GET /api/mcp/overview   {"builtin": […], "tests": {name: probeResult}, "oauth": {name: {...}}, "redirect_uri": "…"}
func (s *Server) handleMCPOverview(w http.ResponseWriter, r *http.Request) {
	c := s.probes()
	c.mu.Lock()
	tests := make(map[string]probeResult, len(c.last))
	for k, v := range c.last {
		tests[k] = v
	}
	c.mu.Unlock()
	signins := map[string]any{}
	for _, m := range s.Settings().MCP.Servers {
		if m.Auth == config.MCPAuthOAuth {
			signins[m.Name] = oauthView(s.oauthOf(m.URL))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"builtin": s.builtinViews(), "tests": tests, "oauth": signins,
		"redirect_uri": redirectURI(r)})
}
