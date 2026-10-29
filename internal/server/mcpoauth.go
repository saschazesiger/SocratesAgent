package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/mcpclient"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// OAuth for the remote MCP servers of Admin (the MCP authorization spec):
// Socrates is the OAuth client. Admin's Connect discovers the server's
// authorization server, registers Socrates with it when it has not yet,
// and sends the browser there; the callback trades the code for tokens,
// which are kept sealed in the kv table, keyed by the server's URL, so a
// server renamed or edited keeps its sign-in. An agent never sees a token:
// it reaches a streamable HTTP server through Socrates (handleMCPProxy),
// which adds a fresh one to every request; an SSE server, which a proxy
// cannot carry, is given the token as a header when its agent starts.

// kvMCPOAuth is where the sign-ins are kept, sealed.
const kvMCPOAuth = "mcp.oauth"

// oauthState is the sign-in of one server.
type oauthState struct {
	URL         string              `json:"url"`
	Metadata    *mcpclient.Metadata `json:"metadata"`
	Client      *mcpclient.Client   `json:"client"`
	RedirectURI string              `json:"redirect_uri"`
	Token       *mcpclient.Token    `json:"token,omitempty"`
	Scope       string              `json:"scope,omitempty"`
	ConnectedAt int64               `json:"connected_at,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// oauthPending is a sign-in the browser is away for.
type oauthPending struct {
	url, name, verifier string
	created             time.Time
}

// oauthHub keeps the sign-ins.
type oauthHub struct {
	mu      sync.Mutex
	loaded  bool
	states  map[string]*oauthState
	pending map[string]oauthPending
	// refreshing serialises the refresh of one server's token.
	refreshing map[string]*sync.Mutex
}

func (s *Server) oauth() *oauthHub {
	s.oauthOnce.Do(func() {
		s.oauthKeep = &oauthHub{states: map[string]*oauthState{}, pending: map[string]oauthPending{}, refreshing: map[string]*sync.Mutex{}}
	})
	return s.oauthKeep
}

// oauthKey is a server's URL as the sign-ins are keyed by.
func oauthKey(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

func (s *Server) oauthLoadLocked(h *oauthHub) {
	if h.loaded {
		return
	}
	h.loaded = true
	raw, err := kvStore{s.store}.GetKV(kvMCPOAuth)
	if err != nil || raw == "" {
		return
	}
	sealed, err := hex.DecodeString(raw)
	if err != nil {
		return
	}
	plain, err := keySealer{s}.Open(kvMCPOAuth, sealed)
	if err != nil {
		log.Printf("mcp: the OAuth sign-ins cannot be opened: %v", err)
		return
	}
	_ = json.Unmarshal(plain, &h.states)
	if h.states == nil {
		h.states = map[string]*oauthState{}
	}
}

func (s *Server) oauthSaveLocked(h *oauthHub) error {
	plain, err := json.Marshal(h.states)
	if err != nil {
		return err
	}
	sealed, err := keySealer{s}.Seal(kvMCPOAuth, plain)
	if err != nil {
		return err
	}
	return s.store.SetKV(kvMCPOAuth, hex.EncodeToString(sealed))
}

// oauthOf is a copy of a server's sign-in, nil for none.
func (s *Server) oauthOf(serverURL string) *oauthState {
	h := s.oauth()
	h.mu.Lock()
	defer h.mu.Unlock()
	s.oauthLoadLocked(h)
	return h.states[oauthKey(serverURL)].clone()
}

// clone is a copy that shares nothing a change could reach.
func (st *oauthState) clone() *oauthState {
	if st == nil {
		return nil
	}
	c := *st
	if st.Token != nil {
		t := *st.Token
		c.Token = &t
	}
	if st.Metadata != nil {
		m := *st.Metadata
		c.Metadata = &m
	}
	if st.Client != nil {
		cl := *st.Client
		c.Client = &cl
	}
	return &c
}

// oauthUpdate changes a server's sign-in in place, reading and writing it
// under one lock, so that two changes made at once both count; change gets
// nil for a server without one, and may make it.
func (s *Server) oauthUpdate(serverURL string, change func(st *oauthState) *oauthState) error {
	h := s.oauth()
	h.mu.Lock()
	defer h.mu.Unlock()
	s.oauthLoadLocked(h)
	key := oauthKey(serverURL)
	next := change(h.states[key].clone())
	if next == nil {
		return nil
	}
	h.states[key] = next
	return s.oauthSaveLocked(h)
}

// oauthToken is a usable access token of a server, refreshed when it runs
// out within two minutes, or when refused is the token it holds (the server
// refused it before it ran out); "" with why when there is none. Refreshes
// of one server run one at a time, so a rotated refresh token is never used
// twice.
func (s *Server) oauthToken(ctx context.Context, serverURL, refused string) (string, error) {
	h := s.oauth()
	key := oauthKey(serverURL)
	h.mu.Lock()
	lock := h.refreshing[key]
	if lock == nil {
		lock = &sync.Mutex{}
		h.refreshing[key] = lock
	}
	h.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	st := s.oauthOf(serverURL)
	if st == nil || st.Token == nil {
		return "", errors.New("Socrates is not signed in to " + serverURL + ": connect it in Admin, MCP servers")
	}
	stale := refused != "" && st.Token.AccessToken == refused
	if !stale && !st.Token.Expired(2*time.Minute) {
		return st.Token.AccessToken, nil
	}
	if st.Token.RefreshToken == "" {
		return "", errors.New("the sign-in to " + serverURL + " ran out: connect it again in Admin")
	}
	next, err := mcpclient.Refresh(ctx, st.Metadata, st.Client, st.Token)
	if err != nil {
		_ = s.oauthUpdate(serverURL, func(cur *oauthState) *oauthState {
			if cur != nil {
				cur.Error = "the token could not be refreshed: " + err.Error()
			}
			return cur
		})
		return "", fmt.Errorf("the sign-in to %s could not be refreshed (%v): connect it again in Admin", serverURL, err)
	}
	if err := s.oauthUpdate(serverURL, func(cur *oauthState) *oauthState {
		if cur != nil {
			cur.Token, cur.Error = next, ""
		}
		return cur
	}); err != nil {
		log.Printf("mcp: %v", err)
	}
	return next.AccessToken, nil
}

// oauthView is a sign-in as the browser sees it: never a token.
func oauthView(st *oauthState) map[string]any {
	if st == nil {
		return map[string]any{"connected": false}
	}
	v := map[string]any{"connected": st.Token != nil, "connected_at": st.ConnectedAt, "scope": st.Scope}
	if st.Metadata != nil {
		v["authorization_server"] = st.Metadata.AuthorizationServer
	}
	if st.Token != nil {
		if !st.Token.ExpiresAt.IsZero() {
			v["expires_at"] = st.Token.ExpiresAt.UnixMilli()
		}
		v["refreshable"] = st.Token.RefreshToken != ""
		if st.Token.Scope != "" {
			v["scope"] = st.Token.Scope
		}
	}
	if st.Error != "" {
		v["error"] = st.Error
	}
	return v
}

// redirectURI is where an authorization server sends the browser back: the
// address the browser has this page at.
func redirectURI(r *http.Request) string {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	if origin == "" || origin == "null" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		origin = scheme + "://" + r.Host
	}
	return origin + "/api/mcp/oauth/callback"
}

// handleMCPOAuthStart begins a sign-in for a remote server and gives the
// address to send the browser to.
//
//	POST /api/mcp/oauth/start   {"name","url","scope"}   {"url": "<authorization request>"}
func (s *Server) handleMCPOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string            `json:"name"`
		URL     string            `json:"url"`
		Scope   string            `json:"scope"`
		Headers map[string]string `json:"headers"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	serverURL := strings.TrimSpace(body.URL)
	if u, err := url.Parse(serverURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "the server's URL is not an http(s) address")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	// What the server says when asked without a token points at its
	// authorization server.
	var challenge *mcpclient.AuthRequiredError
	if _, err := mcpclient.Probe(ctx, mcpclient.Server{Transport: "http", URL: serverURL, Headers: body.Headers}); err != nil {
		errors.As(err, &challenge)
	}
	md, err := mcpclient.Discover(ctx, serverURL, challenge)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the server's sign-in could not be found: "+err.Error())
		return
	}
	redirect := redirectURI(r)
	st := s.oauthOf(serverURL)
	var client *mcpclient.Client
	if st != nil && st.Client != nil && st.RedirectURI == redirect && st.Metadata != nil &&
		st.Metadata.AuthorizationServer == md.AuthorizationServer {
		client = st.Client
	} else {
		if md.RegistrationEndpoint == "" {
			writeError(w, http.StatusBadGateway, "the server's authorization server lets no new client register itself, so Socrates cannot sign in to it; give it a token in a header instead")
			return
		}
		client, err = mcpclient.Register(ctx, md, redirect, "Socrates")
		if err != nil {
			writeError(w, http.StatusBadGateway, "Socrates could not register with the server's authorization server: "+err.Error())
			return
		}
	}
	scope := strings.TrimSpace(body.Scope)
	if scope == "" && challenge != nil {
		scope = challenge.Scope
	}
	// Only what this start found out changes: a token refreshed meanwhile
	// stays.
	if err := s.oauthUpdate(serverURL, func(cur *oauthState) *oauthState {
		if cur == nil {
			cur = &oauthState{}
		}
		cur.URL, cur.Metadata, cur.Client, cur.RedirectURI, cur.Scope = serverURL, md, client, redirect, scope
		return cur
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	verifier, pkce := mcpclient.NewPKCE()
	state := randomState()
	h := s.oauth()
	h.mu.Lock()
	for k, p := range h.pending {
		if time.Since(p.created) > 30*time.Minute {
			delete(h.pending, k)
		}
	}
	h.pending[state] = oauthPending{url: serverURL, name: strings.TrimSpace(body.Name), verifier: verifier, created: time.Now()}
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"url": mcpclient.AuthURL(md, client, redirect, state, pkce, scope)})
}

func randomState() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// handleMCPOAuthCallback is where the authorization server sends the
// browser back: the code is traded for tokens, and the browser goes back to
// Admin's MCP servers with the outcome.
//
//	GET /api/mcp/oauth/callback?code=…&state=…   303 to /admin
func (s *Server) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back := func(name, problem string) {
		v := url.Values{}
		if problem != "" {
			v.Set("mcp_error", problem)
		} else {
			v.Set("mcp_connected", name)
		}
		http.Redirect(w, r, "/admin?"+v.Encode()+"#mcpCard", http.StatusSeeOther)
	}
	h := s.oauth()
	h.mu.Lock()
	p, ok := h.pending[q.Get("state")]
	delete(h.pending, q.Get("state"))
	h.mu.Unlock()
	if !ok {
		back("", "The sign-in was not started here, or it took longer than 30 minutes. Connect again.")
		return
	}
	if e := q.Get("error"); e != "" {
		text := e
		if d := q.Get("error_description"); d != "" {
			text += ": " + d
		}
		back(p.name, "The sign-in was refused: "+text)
		return
	}
	st := s.oauthOf(p.url)
	if st == nil || st.Client == nil || st.Metadata == nil {
		back(p.name, "The sign-in was forgotten meanwhile. Connect again.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	tok, err := mcpclient.Exchange(ctx, st.Metadata, st.Client, q.Get("code"), p.verifier, st.RedirectURI)
	if err != nil {
		back(p.name, "The code could not be traded for a token: "+err.Error())
		return
	}
	if err := s.oauthUpdate(p.url, func(cur *oauthState) *oauthState {
		if cur == nil {
			cur = st
		}
		cur.Token, cur.ConnectedAt, cur.Error = tok, time.Now().UnixMilli(), ""
		return cur
	}); err != nil {
		back(p.name, err.Error())
		return
	}
	name := p.name
	if name == "" {
		name = p.url
	}
	back(name, "")
}

// handleMCPOAuthStatus is a server's sign-in, never its token.
//
//	GET /api/mcp/oauth?url=…   {"connected", "expires_at", "scope", "authorization_server", "error"}
func (s *Server) handleMCPOAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, oauthView(s.oauthOf(r.URL.Query().Get("url"))))
}

// handleMCPOAuthDisconnect signs out of a server: the token is revoked where
// the server can, and forgotten.
//
//	DELETE /api/mcp/oauth?url=…
func (s *Server) handleMCPOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	key := oauthKey(r.URL.Query().Get("url"))
	st := s.oauthOf(key)
	if st != nil && st.Token != nil && st.Metadata != nil && st.Client != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		if err := mcpclient.Revoke(ctx, st.Metadata, st.Client, st.Token); err != nil {
			log.Printf("mcp: revoking the token of %s: %v", key, err)
		}
		cancel()
	}
	h := s.oauth()
	h.mu.Lock()
	s.oauthLoadLocked(h)
	delete(h.states, key)
	err := s.oauthSaveLocked(h)
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, oauthView(nil))
}

// ------------------------------------------------------------- the proxy

// mcpProxyHeader carries a chat's token on the requests of its agent to the
// proxy.
const mcpProxyHeader = "X-Socrates-MCP"

// oauthProxied is a server signed in to with OAuth as a chat's agent
// reaches it: a streamable HTTP server through Socrates' proxy, which adds a
// fresh token to every request; an SSE server directly, with the token as
// it is now (refreshed when the agent starts again).
func (s *Server) oauthProxied(srv agent.MCPServer, chatID string) agent.MCPServer {
	if srv.Transport == agent.MCPHTTP && s.LocalURL() != "" {
		e, err := s.chatToolsEndpoint(chatID)
		if err == nil {
			srv.URL = strings.TrimRight(s.LocalURL(), "/") + "/mcp-proxy/" + url.PathEscape(srv.Name)
			srv.Headers = map[string]string{mcpProxyHeader: e.Token}
			return srv
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, err := s.oauthToken(ctx, srv.URL, "")
	if err != nil {
		log.Printf("mcp: %s: %v", srv.Name, err)
		return srv
	}
	headers := map[string]string{}
	for k, v := range srv.Headers {
		headers[k] = v
	}
	headers["Authorization"] = "Bearer " + token
	srv.Headers = headers
	return srv
}

// hopHeaders are not passed through the proxy.
var hopHeaders = map[string]bool{"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Host": true, "Authorization": true, "Cookie": true, "Content-Length": true}

// mcpProxyClient carries the proxied requests; a stream lasts as long as the
// agent keeps it.
var mcpProxyClient = &http.Client{Timeout: 0, CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// maxProxyBody is the largest request the proxy passes on.
const maxProxyBody = 16 << 20

// loopback is whether a request came from this machine and not through the
// tunnel, which also connects from here but says whom it carries.
func loopback(r *http.Request) bool {
	if r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("Cf-Ray") != "" || r.Header.Get("X-Forwarded-For") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// chatHasMCPOn is whether a chat's agent has an MCP server on: as the chat
// chose, or as its test run's area and settings say for a test session.
func (s *Server) chatHasMCPOn(row *store.Session, name string) bool {
	if row.IsTest() {
		if s.tests == nil {
			return false
		}
		l, err := s.tests.LaunchFor(row.TestRef())
		return err == nil && slices.Contains(l.MCP, name)
	}
	folder, _ := agent.ReadFolderMCP(row.Workdir)
	list, _ := agent.ChatMCP(s.sharedMCP(s.Settings(), ""), folder, readMCPChoices(row.Options))
	for _, c := range list {
		if c.Name == name {
			return c.On
		}
	}
	return false
}

// handleMCPProxy passes a chat agent's request on to an OAuth server of
// Admin with a fresh token, and its answer back as it streams.
//
//	ANY /mcp-proxy/{name}   (header X-Socrates-MCP: the chat's token)
func (s *Server) handleMCPProxy(w http.ResponseWriter, r *http.Request) {
	// Only the agents on this machine, and only for a chat that has the
	// server on: the proxy holds Admin's sign-in.
	if !loopback(r) {
		writeError(w, http.StatusForbidden, "the MCP proxy serves the agents on this machine only")
		return
	}
	chat := s.chatOfToken(r.Header.Get(mcpProxyHeader))
	if chat == "" {
		writeError(w, http.StatusUnauthorized, "this chat is not known to Socrates")
		return
	}
	name := r.PathValue("name")
	if row, err := s.store.GetSession(chat); err != nil || !row.IsChat() || !s.chatHasMCPOn(row, name) {
		writeError(w, http.StatusForbidden, "this chat does not have the MCP server "+name+" on")
		return
	}
	var m *config.MCPServerSetting
	for _, one := range s.Settings().MCP.Servers {
		if one.Name == name && one.Auth == config.MCPAuthOAuth {
			m = &one
			break
		}
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "Admin has no OAuth MCP server "+name)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) > maxProxyBody {
		writeError(w, http.StatusRequestEntityTooLarge, "the request is larger than the MCP proxy takes")
		return
	}
	send := func(token string) (*http.Response, error) {
		target := m.URL
		if r.URL.RawQuery != "" {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + r.URL.RawQuery
		}
		out, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, vs := range r.Header {
			if !hopHeaders[http.CanonicalHeaderKey(k)] && !strings.EqualFold(k, mcpProxyHeader) {
				out.Header[k] = vs
			}
		}
		for k, v := range m.Headers {
			out.Header.Set(k, v)
		}
		out.Header.Set("Authorization", "Bearer "+token)
		return mcpProxyClient.Do(out)
	}
	token, err := s.oauthToken(r.Context(), m.URL, "")
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	res, err := send(token)
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		// The token was refused before it ran out: refresh it once.
		res.Body.Close()
		if token, err = s.oauthToken(r.Context(), m.URL, token); err == nil {
			res, err = send(token)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "the MCP server "+name+" could not be reached: "+err.Error())
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		if !hopHeaders[http.CanonicalHeaderKey(k)] {
			w.Header()[k] = vs
		}
	}
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}
