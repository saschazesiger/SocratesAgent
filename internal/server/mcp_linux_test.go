//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack/driver"
	testsmcp "github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
)

// mcpChat is a chat row of a signed in server, with the server's tools
// socket listening; no agent runs.
func mcpChat(t *testing.T, e *testEnv, workdir string) string {
	t.Helper()
	id := "c" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	if len(id) > 30 {
		id = id[:30]
	}
	row := &store.Session{ID: id, Title: "MCP chat", Harness: "fake", Kind: store.KindChat, State: store.StateRunning,
		Workdir: workdir, WorkdirMode: "custom"}
	if err := e.srv.store.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	tools := e.srv.startChatTools()
	t.Cleanup(tools.close)
	return id
}

// socketPost is a request of `socrates mcp-server` on the tools socket.
func socketPost(t *testing.T, e *testEnv, chat string, req mcpserver.Request, out any) int {
	t.Helper()
	ep, err := e.srv.chatToolsEndpoint(chat)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequest(http.MethodPost, "http://socrates"+mcpserver.EndpointPath, bytes.NewReader(body))
	hr.Header.Set("Authorization", "Bearer "+ep.Token)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", ep.Socket)
	}}}
	res, err := client.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_ = json.NewDecoder(res.Body).Decode(out)
	return res.StatusCode
}

// The built-in BrowserStack server is one of every chat's MCP servers, off
// until switched on; on, the chat's agent gets `socrates mcp-server
// browserstack`, whose calls reach the server with the chat's token.
func TestABuiltInServerIsAChatsMCPServer(t *testing.T) {
	e := signedIn(t)
	chat := mcpChat(t, e, t.TempDir())
	res, body := e.do(t, e.client, "GET", "/api/sessions/"+chat+"/mcp", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mcp: %d", res.StatusCode)
	}
	var found map[string]any
	for _, s := range body["servers"].([]any) {
		if m := s.(map[string]any); m["name"] == "browserstack" {
			found = m
		}
	}
	if found == nil || found["from"] != agent.MCPFromSocrates || found["on"] != false || found["title"] != "BrowserStack" {
		t.Fatalf("the built-in server: %v", body)
	}
	shared := e.srv.sharedMCP(e.srv.Settings(), chat)
	var bs *agent.SharedMCP
	for i := range shared {
		if shared[i].Name == "browserstack" {
			bs = &shared[i]
		}
	}
	if bs == nil || !bs.Builtin || bs.Default || strings.Join(bs.Args, " ") != "mcp-server browserstack" ||
		bs.Env[mcpserver.EnvSocket] == "" || bs.Env[mcpserver.EnvCaller] == "" || bs.ToolTimeout != builtinToolTimeout {
		t.Fatalf("what the agent starts: %+v", bs)
	}
	// Admin switches it on for every chat.
	next := e.srv.Settings()
	next.MCP.Builtin = []config.MCPBuiltinSetting{{Name: "browserstack", Default: true}}
	if err := e.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	list, _ := agent.ChatMCP(e.srv.sharedMCP(e.srv.Settings(), ""), nil, nil)
	for _, c := range list {
		if c.On != (c.Name == "browserstack") {
			t.Fatalf("on in every chat: %+v", list)
		}
	}

	var d mcpserver.Description
	if code := socketPost(t, e, chat, mcpserver.Request{Server: "browserstack", Method: "describe"}, &d); code != http.StatusOK ||
		d.Title != "BrowserStack" || !strings.Contains(d.Instructions, "Native taps") || len(d.Tools) < 20 {
		t.Fatalf("describe: %d %+v", code, d.Title)
	}
	var r mcpserver.Result
	socketPost(t, e, chat, mcpserver.Request{Server: "browserstack", Method: "call", Tool: "devices"}, &r)
	if r.IsError || !strings.Contains(r.Content[0].Text, "pixel: Pixel 9 · Chrome") {
		t.Fatalf("devices: %+v", r)
	}
	socketPost(t, e, chat, mcpserver.Request{Server: "browserstack", Method: "call", Tool: "tap", Arguments: json.RawMessage(`{"text":"Go"}`)}, &r)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "call start first") {
		t.Fatalf("tap without a device: %+v", r)
	}
	ep, _ := e.srv.chatToolsEndpoint(chat)
	ep.Token = "c-nobody.x"
	if code := socketPost(t, &testEnv{srv: e.srv}, chat, mcpserver.Request{}, &r); code != http.StatusBadRequest && code != http.StatusNotFound {
		// With the chat's own token an empty request is refused as such.
		t.Fatalf("an empty request: %d", code)
	}
	// An Admin server may not take the built-in server's name.
	bad := e.srv.Settings()
	bad.MCP.Servers = []config.MCPServerSetting{{Name: "browserstack", Type: "stdio", Command: "x"}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "built into Socrates") {
		t.Fatalf("a server named like the built-in one: %v", err)
	}
}

// Socrates Tests is built in, needs no account and is off in a chat until
// switched on; on, it answers about the chat's repository's catalog and
// the chat tools leave their own test tools out.
func TestSocratesTestsIsAChatsMCPServer(t *testing.T) {
	e := signedIn(t)
	repo := catalogRepo(t)
	chat := mcpChat(t, e, filepath.Join(repo, "sub"))
	_, body := e.do(t, e.client, "GET", "/api/sessions/"+chat+"/mcp", "")
	var found map[string]any
	for _, s := range body["servers"].([]any) {
		if m := s.(map[string]any); m["name"] == testsmcp.Name {
			found = m
		}
	}
	if found == nil || found["from"] != agent.MCPFromSocrates || found["on"] != false || found["title"] != "Socrates Tests" ||
		found["chat_only"] != true {
		t.Fatalf("the built-in server: %v", body)
	}
	row, err := e.srv.store.GetSession(chat)
	if err != nil {
		t.Fatal(err)
	}
	if e.srv.testsMCPOn(row) || !offersTestTools(row) {
		t.Fatal("off, the chat tools have the test tools")
	}
	row.Options = withOption(row.Options, "mcp", map[string]bool{testsmcp.Name: true})
	if !e.srv.testsMCPOn(row) {
		t.Fatal("switched on in the chat")
	}

	var d mcpserver.Description
	if code := socketPost(t, e, chat, mcpserver.Request{Server: testsmcp.Name, Method: "describe"}, &d); code != http.StatusOK ||
		!strings.Contains(d.Instructions, "end your turn") || len(d.Tools) < 20 {
		t.Fatalf("describe: %d %+v", code, d)
	}
	var r mcpserver.Result
	socketPost(t, e, chat, mcpserver.Request{Server: testsmcp.Name, Method: "call", Tool: chattools.TestsCheck}, &r)
	if r.IsError || !strings.Contains(r.Content[0].Text, "The test catalog of Shop") {
		t.Fatalf("tests_check: %+v", r)
	}
	socketPost(t, e, chat, mcpserver.Request{Server: testsmcp.Name, Method: "call", Tool: testsmcp.Runs}, &r)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "not running") {
		t.Fatalf("test_runs without a runner: %+v", r)
	}
	e.srv.tests = testmode.New(t.TempDir(), testHost{e.srv})
	r = mcpserver.Result{}
	socketPost(t, e, chat, mcpserver.Request{Server: testsmcp.Name, Method: "call", Tool: testsmcp.Runs}, &r)
	if r.IsError || !strings.Contains(r.Content[0].Text, "no test run yet") {
		t.Fatalf("test_runs: %+v", r)
	}
}

// Admin adds a BrowserStack account after BrowserStack accepted it, never
// shows its key again, and removes it.
func TestAdminKeepsTheBuiltInServersAccounts(t *testing.T) {
	plans := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, key, _ := r.BasicAuth(); user != "ann" || key != "secret-key-1234" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"parallel_sessions_running":0,"parallel_sessions_max_allowed":1}`))
	}))
	defer plans.Close()
	old := driver.PlanAPI
	driver.PlanAPI = plans.URL
	defer func() { driver.PlanAPI = old }()
	e := signedIn(t)
	if err := os.MkdirAll(e.srv.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	res, body := e.do(t, e.client, "POST", "/api/mcp/builtin/browserstack/accounts", `{"values":{"user":"ann","key":"wrong"}}`)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body["error"].(string), "refused") {
		t.Fatalf("a refused account: %d %v", res.StatusCode, body)
	}
	res, body = e.do(t, e.client, "POST", "/api/mcp/builtin/browserstack/accounts", `{"values":{"user":"ann","key":"secret-key-1234"}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("add: %d %v", res.StatusCode, body)
	}
	accounts := body["accounts"].([]any)
	a := accounts[0].(map[string]any)
	if len(accounts) != 1 || a["label"] != "ann" || a["values"].(map[string]any)["key"] != "••••1234" ||
		a["check"].(map[string]any)["state"] != mcpserver.AccountValid {
		t.Fatalf("the account: %v", a)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "secret-key") {
		t.Fatal("the key went to the browser")
	}
	if kv, _ := e.srv.store.GetKV("mcp.browserstack.accounts"); kv == "" || strings.Contains(kv, "secret") {
		t.Fatalf("stored: %q", kv)
	}
	_, over := e.do(t, e.client, "GET", "/api/mcp/overview", "")
	if b := over["builtin"].([]any); len(b) != 2 || b[0].(map[string]any)["name"] != "browserstack" ||
		len(b[0].(map[string]any)["accounts"].([]any)) != 1 {
		t.Fatalf("overview: %v", over)
	}
	res, body = e.do(t, e.client, "DELETE", "/api/mcp/builtin/browserstack/accounts/"+a["id"].(string), "")
	if res.StatusCode != http.StatusOK || len(body["accounts"].([]any)) != 0 {
		t.Fatalf("remove: %d %v", res.StatusCode, body)
	}
}

// fakeOAuthMCP is a remote MCP server behind OAuth: protected resource
// metadata, an authorization server with registration, and a streamable
// HTTP endpoint that wants the token it issued.
type fakeOAuthMCP struct {
	srv   *httptest.Server
	mu    sync.Mutex
	seen  []string // the Authorization of each MCP request
	token string
}

func newFakeOAuthMCP(t *testing.T) *fakeOAuthMCP {
	f := &fakeOAuthMCP{token: "tok-1"}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	base := f.srv.URL
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize",
			"token_endpoint": base + "/token", "registration_endpoint": base + "/register",
			"code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "socrates-client"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.token, "token_type": "Bearer", "expires_in": 3600, "refresh_token": "r-1"})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{}
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "fake-remote", "version": "1.2.3"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "search", "description": "Searches.", "inputSchema": map[string]any{"type": "object"}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	})
	return f
}

// Admin connects a remote server with OAuth: the test says it wants a
// sign-in, Connect sends the browser to its authorization server, the
// callback keeps the token, the test then lists its tools, and a chat's
// agent reaches it through Socrates' proxy, which adds the token.
func TestAnOAuthServerIsConnectedTestedAndProxied(t *testing.T) {
	f := newFakeOAuthMCP(t)
	e := signedIn(t)
	if err := os.MkdirAll(e.srv.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.srv.SetLocalURL(e.server.URL)
	serverURL := f.srv.URL + "/mcp"
	setting := config.MCPServerSetting{Name: "remote", Type: "http", URL: serverURL, Auth: config.MCPAuthOAuth, Default: true}
	raw, _ := json.Marshal(map[string]any{"server": setting})
	_, body := e.do(t, e.client, "POST", "/api/mcp/test", string(raw))
	if body["state"] != "auth" {
		t.Fatalf("before the sign-in: %v", body)
	}
	res, body := e.do(t, e.client, "POST", "/api/mcp/oauth/start", `{"name":"remote","url":"`+serverURL+`"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start: %d %v", res.StatusCode, body)
	}
	auth, err := url.Parse(body["url"].(string))
	if err != nil || auth.Path != "/authorize" || auth.Query().Get("client_id") != "socrates-client" ||
		auth.Query().Get("code_challenge_method") != "S256" || auth.Query().Get("resource") != serverURL {
		t.Fatalf("the authorization request: %v", body["url"])
	}
	if !strings.HasSuffix(auth.Query().Get("redirect_uri"), "/api/mcp/oauth/callback") {
		t.Fatalf("redirect: %s", auth.Query().Get("redirect_uri"))
	}
	res, _ = e.do(t, e.client, "GET", "/api/mcp/oauth/callback?code=the-code&state="+url.QueryEscape(auth.Query().Get("state")), "")
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "mcp_connected=remote") {
		t.Fatalf("callback: %d %s", res.StatusCode, loc)
	}
	_, status := e.do(t, e.client, "GET", "/api/mcp/oauth?url="+url.QueryEscape(serverURL), "")
	if status["connected"] != true || status["refreshable"] != true {
		t.Fatalf("status: %v", status)
	}
	raw, _ = json.Marshal(status)
	if strings.Contains(string(raw), "tok-1") {
		t.Fatal("the token went to the browser")
	}
	_, body = e.do(t, e.client, "POST", "/api/mcp/test", string(mustJSON(map[string]any{"server": setting})))
	if body["state"] != "ok" || body["server_version"] != "1.2.3" || len(body["tools"].([]any)) != 1 {
		t.Fatalf("after the sign-in: %v", body)
	}

	// A chat's agent reaches it through the proxy, with no token of its own.
	next := e.srv.Settings()
	next.MCP.Servers = []config.MCPServerSetting{setting}
	if err := e.srv.saveSettings(next); err != nil {
		t.Fatal(err)
	}
	chat := mcpChat(t, e, t.TempDir())
	var proxied agent.MCPServer
	for _, s := range e.srv.sharedMCP(e.srv.Settings(), chat) {
		if s.Name == "remote" {
			proxied = s.MCPServer
		}
	}
	if !strings.HasPrefix(proxied.URL, e.server.URL+"/mcp-proxy/remote") || proxied.Headers[mcpProxyHeader] == "" ||
		proxied.Headers["Authorization"] != "" {
		t.Fatalf("what the agent gets: %+v", proxied)
	}
	req, _ := http.NewRequest(http.MethodPost, proxied.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set(mcpProxyHeader, proxied.Headers[mcpProxyHeader])
	req.Header.Set("Content-Type", "application/json")
	pres, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(pres.Body)
	pres.Body.Close()
	if pres.StatusCode != http.StatusOK || !strings.Contains(string(out), `"search"`) {
		t.Fatalf("through the proxy: %d %s", pres.StatusCode, out)
	}
	f.mu.Lock()
	last := f.seen[len(f.seen)-1]
	f.mu.Unlock()
	if last != "Bearer tok-1" {
		t.Fatalf("the upstream saw %q", last)
	}
	// Without a chat's token the proxy is closed.
	bad, _ := http.Post(e.server.URL+"/mcp-proxy/remote", "application/json", strings.NewReader("{}"))
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous request: %d", bad.StatusCode)
	}
	// Disconnect forgets it.
	_, status = e.do(t, e.client, "DELETE", "/api/mcp/oauth?url="+url.QueryEscape(serverURL), "")
	if status["connected"] != false {
		t.Fatalf("disconnect: %v", status)
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
