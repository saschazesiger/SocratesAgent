package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/builtin"
	testsmcp "github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The MCP servers built into Socrates (internal/mcpserver): the registry
// lives in the server, its servers run in this process, and a chat that has
// one on gets `socrates mcp-server <name>` among its MCP servers, which hands
// every call to the registry over the tools socket with the chat's token.
// Admin keeps their accounts (sealed in the kv table) and whether a chat
// has them unless it says otherwise (config.MCPSettings.Builtin, off unless
// switched on).

// builtinToolTimeout is the tool timeout an agent is given for a built-in
// server: a device BrowserStack queues keeps start waiting for minutes.
const builtinToolTimeout = 3600

// kvStore is the store as the registry keeps its accounts in it.
type kvStore struct{ st *store.Store }

func (k kvStore) GetKV(key string) (string, error) {
	v, err := k.st.GetKV(key)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func (k kvStore) SetKV(key, value string) error { return k.st.SetKV(key, value) }

// keySealer seals with the data directory's key, as the accounts' API keys
// are.
type keySealer struct{ s *Server }

func (k keySealer) Seal(object string, plain []byte) ([]byte, error) {
	keys, err := k.s.accounts.keys()
	if err != nil {
		return nil, err
	}
	return vault.Seal(keys.Key(), object, plain)
}

func (k keySealer) Open(object string, sealed []byte) ([]byte, error) {
	keys, err := k.s.accounts.keys()
	if err != nil {
		return nil, err
	}
	return vault.Open(keys.Key(), object, sealed)
}

// newMCPRegistry is the registry with every built-in server.
func (s *Server) newMCPRegistry() *mcpserver.Registry {
	r := mcpserver.NewRegistry(kvStore{s.store}, keySealer{s})
	builtin.Register(r, s.dataDir, builtin.Deps{Tests: testsmcp.Deps{Store: s.chatTests}})
	return r
}

// builtinDefault is whether a chat has a built-in server unless it says
// otherwise: off, until Admin switches it on.
func builtinDefault(settings config.Settings, name string) bool {
	for _, b := range settings.MCP.Builtin {
		if b.Name == name {
			return b.Default
		}
	}
	return false
}

// builtinMCP is the built-in servers as a chat's MCP servers. With a chat,
// each is `socrates mcp-server <name>` with the chat's endpoint; without
// one (a view, the new chat sheet) the command is left out.
func (s *Server) builtinMCP(settings config.Settings, chatID string) []agent.SharedMCP {
	var env map[string]string
	exe := ""
	if chatID != "" {
		e, err := s.chatToolsEndpoint(chatID)
		if err != nil {
			log.Printf("mcp: the built-in servers cannot reach Socrates from %s: %v", chatID, err)
			return nil
		}
		env = (&mcpserver.Endpoint{Socket: e.Socket, Token: e.Token}).Env()
		if exe, err = os.Executable(); err != nil {
			log.Printf("mcp: %v", err)
			return nil
		}
	}
	var out []agent.SharedMCP
	for _, srv := range s.mcp.Servers() {
		cmd := exe
		if cmd == "" {
			cmd = "socrates"
		}
		out = append(out, agent.SharedMCP{Builtin: true, Default: builtinDefault(settings, srv.Name()), MCPServer: agent.MCPServer{
			Name: srv.Name(), Transport: agent.MCPStdio, Command: cmd, Args: []string{"mcp-server", srv.Name()}, Env: env,
			ToolTimeout: builtinToolTimeout,
		}})
	}
	return out
}

// mcpSession is a chat as a built-in server sees the caller.
func (s *Server) mcpSession(row *store.Session) mcpserver.Session {
	return mcpserver.Session{ID: row.ID, Title: row.Title, Workdir: row.Workdir}
}

// handleMCPCall answers `socrates mcp-server` on the tools socket:
//
//	POST /mcp  {"server","method":"describe"|"call","tool","arguments"}
//	  describe: {"title","instructions","tools"}   call: {"content":[…],"isError"}
func (t *chatToolServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	s := t.srv
	caller := s.chatOfToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "this chat is not known to Socrates")
		return
	}
	var req mcpserver.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read")
		return
	}
	srv := s.mcp.Get(req.Server)
	if srv == nil {
		writeError(w, http.StatusNotFound, "Socrates has no MCP server "+req.Server)
		return
	}
	row, err := s.store.GetSession(caller)
	if err != nil || !row.IsChat() {
		writeError(w, http.StatusGone, "this chat is gone")
		return
	}
	switch req.Method {
	case "describe":
		writeJSON(w, http.StatusOK, mcpserver.Describe(srv))
	case "call":
		writeJSON(w, http.StatusOK, s.mcp.Call(r.Context(), req.Server, s.mcpSession(row), req.Tool, req.Arguments))
	default:
		writeError(w, http.StatusBadRequest, "unknown method "+req.Method)
	}
}

// endMCP ends what the built-in servers keep for a session.
func (s *Server) endMCP(session string) {
	if s.mcp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, what := range s.mcp.End(ctx, session) {
		log.Printf("mcp: %s: %s", session, what)
	}
}

// ------------------------------------------------------------------ Admin

// builtinView is a built-in server as Admin shows it.
type builtinView struct {
	Name          string            `json:"name"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Default       bool              `json:"default"`
	Tools         []mcpserver.Tool  `json:"tools"`
	Instructions  string            `json:"instructions"`
	AccountFields []mcpserver.Field `json:"account_fields,omitempty"`
	Accounts      []accountOfMCP    `json:"accounts"`
	Uses          []mcpserver.Use   `json:"uses"`
	Pooled        bool              `json:"pooled"`
}

// accountOfMCP is an account of a built-in server as the browser sees it:
// its secret values never leave the machine, only their last characters.
type accountOfMCP struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Values map[string]string `json:"values"`
	Check  *mcpserver.Check  `json:"check,omitempty"`
	// InUse says who uses it now, "" for nobody.
	InUse string `json:"in_use,omitempty"`
}

func (s *Server) builtinViews() []builtinView {
	settings := s.Settings()
	var out []builtinView
	for _, srv := range s.mcp.Servers() {
		v := builtinView{Name: srv.Name(), Title: srv.Title(), Description: srv.Description(),
			Default: builtinDefault(settings, srv.Name()), Tools: srv.Tools(), Instructions: srv.Instructions(),
			Accounts: []accountOfMCP{}, Uses: []mcpserver.Use{}}
		if rep, ok := srv.(mcpserver.Reporter); ok {
			v.Uses = append(v.Uses, rep.Uses()...)
		}
		_, v.Pooled = srv.(mcpserver.Pool)
		if k, ok := srv.(mcpserver.Accounts); ok {
			v.AccountFields = k.AccountFields()
			for _, a := range s.mcp.Accounts(srv.Name()) {
				view := accountOfMCP{ID: a.ID, Label: k.AccountLabel(a.Values), Values: map[string]string{}, Check: a.Check}
				for _, f := range v.AccountFields {
					val := a.Values[f.Name]
					if f.Secret {
						val = maskSecret(val)
					}
					view.Values[f.Name] = val
				}
				for _, u := range v.Uses {
					if u.Account == a.ID {
						view.InUse = s.useWords(u)
					}
				}
				v.Accounts = append(v.Accounts, view)
			}
		}
		for i := range v.Uses {
			v.Uses[i].What = s.useWords(v.Uses[i])
		}
		out = append(out, v)
	}
	return out
}

// useWords is a use with the chat's title.
func (s *Server) useWords(u mcpserver.Use) string {
	if u.Session == "" {
		return u.What
	}
	if row, err := s.store.GetSession(u.Session); err == nil {
		return u.What + ", chat " + strings.TrimSpace(row.Title)
	}
	return u.What
}

// maskSecret shows the last four characters of a secret, the rest as dots.
func maskSecret(v string) string {
	r := []rune(v)
	if len(r) <= 4 {
		return strings.Repeat("•", len(r))
	}
	return "••••" + string(r[len(r)-4:])
}

// handleBuiltinMCP lists the built-in servers for Admin:
//
//	GET /api/mcp/builtin   [{"name","title","description","default","tools","accounts",…}]
func (s *Server) handleBuiltinMCP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.builtinViews())
}

// builtinViewOf is one server's view, for an answer about it.
func (s *Server) builtinViewOf(name string) any {
	for _, v := range s.builtinViews() {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// handleBuiltinMCPAccountAdd checks and keeps an account of a built-in
// server.
//
//	POST /api/mcp/builtin/{name}/accounts   {"values":{…}}   the server's view
func (s *Server) handleBuiltinMCPAccountAdd(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Values map[string]string `json:"values"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, err := s.mcp.AddAccount(r.Context(), name, body.Values); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.events.poke()
	writeJSON(w, http.StatusOK, s.builtinViewOf(name))
}

// handleBuiltinMCPAccountCheck checks a kept account again.
//
//	POST /api/mcp/builtin/{name}/accounts/{id}/check   the server's view
func (s *Server) handleBuiltinMCPAccountCheck(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	if id == "all" {
		for _, a := range s.mcp.Accounts(name) {
			if _, err := s.mcp.CheckAccount(r.Context(), name, a.ID); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	} else if _, err := s.mcp.CheckAccount(r.Context(), name, id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.builtinViewOf(name))
}

// handleBuiltinMCPAccountDelete forgets an account of a built-in server.
//
//	DELETE /api/mcp/builtin/{name}/accounts/{id}   the server's view
func (s *Server) handleBuiltinMCPAccountDelete(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	if err := s.mcp.RemoveAccount(name, id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.builtinViewOf(name))
}

// mcpTitle is a built-in server's title for words about it.
func (s *Server) mcpTitle(name string) string {
	if srv := s.mcp.Get(name); srv != nil {
		return srv.Title()
	}
	return fmt.Sprintf("%q", name)
}
