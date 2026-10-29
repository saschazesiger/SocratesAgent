package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// authServer is a fake MCP server and authorization server in one: docs
// maps a path to the JSON document served there, {base} standing for the
// server's URL. It records the paths asked for.
type authServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newAuthServer(t *testing.T, docs map[string]string, handlers map[string]http.HandlerFunc) *authServer {
	s := &authServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		if h, ok := handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		doc, ok := docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, strings.ReplaceAll(doc, "{base}", s.URL))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *authServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

const tenantMetadata = `{
	"issuer": "{base}/tenant1",
	"authorization_endpoint": "{base}/tenant1/authorize",
	"token_endpoint": "{base}/tenant1/token",
	"registration_endpoint": "{base}/tenant1/register",
	"revocation_endpoint": "{base}/tenant1/revoke",
	"scopes_supported": ["openid", "mcp"],
	"code_challenge_methods_supported": ["plain", "S256"],
	"client_id_metadata_document_supported": true
}`

func TestDiscoverFromHeader(t *testing.T) {
	s := newAuthServer(t, map[string]string{
		"/custom/prm": `{"resource": "{base}/mcp", "authorization_servers": ["{base}/tenant1"], "scopes_supported": ["mcp:read"]}`,
		"/.well-known/oauth-authorization-server/tenant1": tenantMetadata,
	}, nil)
	md, err := Discover(context.Background(), s.URL+"/mcp", &AuthRequiredError{ResourceMetadata: s.URL + "/custom/prm"})
	if err != nil {
		t.Fatal(err)
	}
	want := Metadata{
		Resource:                          s.URL + "/mcp",
		ResourceMetadataURL:               s.URL + "/custom/prm",
		AuthorizationServer:               s.URL + "/tenant1",
		AuthorizationEndpoint:             s.URL + "/tenant1/authorize",
		TokenEndpoint:                     s.URL + "/tenant1/token",
		RegistrationEndpoint:              s.URL + "/tenant1/register",
		RevocationEndpoint:                s.URL + "/tenant1/revoke",
		ScopesSupported:                   []string{"mcp:read"},
		CodeChallengeMethods:              []string{"plain", "S256"},
		ClientIDMetadataDocumentSupported: true,
	}
	if fmt.Sprint(*md) != fmt.Sprint(want) {
		t.Errorf("metadata\n%+v\nwant\n%+v", *md, want)
	}
	if got := s.asked(); !slices.Equal(got, []string{"/custom/prm", "/.well-known/oauth-authorization-server/tenant1"}) {
		t.Errorf("asked for %v", got)
	}
}

func TestDiscoverWellKnown(t *testing.T) {
	s := newAuthServer(t, map[string]string{
		"/.well-known/oauth-protected-resource/v1/mcp": `{"resource": "{base}", "authorization_servers": ["{base}"]}`,
		"/.well-known/openid-configuration": `{"issuer": "{base}/", "authorization_endpoint": "{base}/oauth/authorize",
			"token_endpoint": "{base}/oauth/token", "scopes_supported": ["mcp"]}`,
	}, nil)
	md, err := Discover(context.Background(), s.URL+"/v1/mcp/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if md.Resource != s.URL || md.ResourceMetadataURL != s.URL+"/.well-known/oauth-protected-resource/v1/mcp" ||
		md.AuthorizationServer != s.URL+"/" || md.TokenEndpoint != s.URL+"/oauth/token" || md.RegistrationEndpoint != "" ||
		!slices.Equal(md.ScopesSupported, []string{"mcp"}) {
		t.Errorf("metadata %+v", md)
	}
	want := []string{
		"/.well-known/oauth-protected-resource/v1/mcp",
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
	}
	if got := s.asked(); !slices.Equal(got, want) {
		t.Errorf("asked for %v, want %v", got, want)
	}
}

func TestDiscoverIssuerPathOpenIDLast(t *testing.T) {
	s := newAuthServer(t, map[string]string{
		"/.well-known/oauth-protected-resource": `{"authorization_servers": ["{base}/realms/x"]}`,
		"/realms/x/.well-known/openid-configuration": `{"issuer": "{base}/realms/x", "authorization_endpoint": "{base}/realms/x/auth",
			"token_endpoint": "{base}/realms/x/token", "code_challenge_methods_supported": ["S256"]}`,
	}, nil)
	md, err := Discover(context.Background(), s.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if md.Resource != s.URL+"/mcp" || md.AuthorizationEndpoint != s.URL+"/realms/x/auth" {
		t.Errorf("metadata %+v", md)
	}
	want := []string{
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-authorization-server/realms/x",
		"/.well-known/openid-configuration/realms/x",
		"/realms/x/.well-known/openid-configuration",
	}
	if got := s.asked(); !slices.Equal(got, want) {
		t.Errorf("asked for %v, want %v", got, want)
	}
}

func TestDiscoverFallback20250326(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		s := newAuthServer(t, nil, nil)
		md, err := Discover(context.Background(), s.URL+"/mcp", &AuthRequiredError{})
		if err != nil {
			t.Fatal(err)
		}
		want := Metadata{
			Resource: s.URL + "/mcp", AuthorizationServer: s.URL,
			AuthorizationEndpoint: s.URL + "/authorize", TokenEndpoint: s.URL + "/token", RegistrationEndpoint: s.URL + "/register",
		}
		if fmt.Sprint(*md) != fmt.Sprint(want) {
			t.Errorf("metadata\n%+v\nwant\n%+v", *md, want)
		}
	})
	t.Run("origin metadata", func(t *testing.T) {
		s := newAuthServer(t, map[string]string{
			"/.well-known/oauth-authorization-server": `{"issuer": "{base}", "authorization_endpoint": "{base}/a", "token_endpoint": "{base}/t"}`,
		}, nil)
		md, err := Discover(context.Background(), s.URL+"/deep/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		if md.AuthorizationEndpoint != s.URL+"/a" || md.TokenEndpoint != s.URL+"/t" || md.RegistrationEndpoint != "" || md.ResourceMetadataURL != "" {
			t.Errorf("metadata %+v", md)
		}
	})
}

func TestDiscoverRefuses(t *testing.T) {
	cases := map[string]struct {
		docs map[string]string
		want string
	}{
		"another resource": {map[string]string{
			"/.well-known/oauth-protected-resource": `{"resource": "{base}/other", "authorization_servers": ["{base}"]}`,
		}, "is for"},
		"insecure endpoint": {map[string]string{
			"/.well-known/oauth-authorization-server": `{"issuer": "{base}", "authorization_endpoint": "{base}/a", "token_endpoint": "http://auth.example.com/token"}`,
		}, "the token endpoint http://auth.example.com/token is not an https URL"},
		"insecure issuer": {map[string]string{
			"/.well-known/oauth-protected-resource": `{"authorization_servers": ["http://auth.example.com"]}`,
		}, "is not an https URL"},
		"issuer mix-up": {map[string]string{
			"/.well-known/oauth-protected-resource":   `{"authorization_servers": ["{base}"]}`,
			"/.well-known/oauth-authorization-server": `{"issuer": "https://evil.example.com", "authorization_endpoint": "{base}/a", "token_endpoint": "{base}/t"}`,
		}, "names another issuer"},
		"no S256": {map[string]string{
			"/.well-known/oauth-authorization-server": `{"issuer": "{base}", "authorization_endpoint": "{base}/a", "token_endpoint": "{base}/t", "code_challenge_methods_supported": ["plain"]}`,
		}, "does not support PKCE"},
	}
	for name, c := range cases {
		s := newAuthServer(t, c.docs, nil)
		_, err := Discover(context.Background(), s.URL+"/mcp", nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", name, err, c.want)
		}
	}
	if _, err := Discover(context.Background(), "http://mcp.example.com/mcp", nil); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("plain http: error %v", err)
	}
}

func TestSecureURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://example.com": true, "http://localhost:8080": true, "http://127.0.0.1": true, "http://[::1]:3000": true,
		"http://example.com": false, "http://127.0.0.1.example.com": false, "ftp://localhost": false,
	} {
		u, _ := url.Parse(raw)
		if secureURL(u) != want {
			t.Errorf("secureURL(%s) = %v", raw, !want)
		}
	}
}

func TestRegister(t *testing.T) {
	var got map[string]any
	s := newAuthServer(t, nil, map[string]http.HandlerFunc{
		"/register": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got["client_name"] == "refuse" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error": "invalid_redirect_uri", "error_description": "The redirect URI is not allowed."}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"client_id": "cid-1", "registration_access_token": "rat", "client_id_issued_at": 1}`)
		},
	})
	md := &Metadata{RegistrationEndpoint: s.URL + "/register"}
	c, err := Register(context.Background(), md, "https://socrates.example.com/oauth/callback", "Socrates")
	if err != nil {
		t.Fatal(err)
	}
	if *c != (Client{ClientID: "cid-1", RegistrationAccessToken: "rat"}) {
		t.Errorf("client %+v", c)
	}
	want := `{"client_name":"Socrates","grant_types":["authorization_code","refresh_token"],"redirect_uris":["https://socrates.example.com/oauth/callback"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Errorf("registration %s\nwant %s", b, want)
	}
	_, err = Register(context.Background(), md, "https://x/cb", "refuse")
	if err == nil || err.Error() != "the registration endpoint answered invalid_redirect_uri: The redirect URI is not allowed." {
		t.Errorf("error %v", err)
	}
	if _, err := Register(context.Background(), &Metadata{}, "https://x/cb", "Socrates"); err == nil {
		t.Error("Register without an endpoint succeeded")
	}
}

func TestPKCEAndAuthURL(t *testing.T) {
	verifier, challenge := NewPKCE()
	if len(verifier) < 43 || len(verifier) > 128 || !regexp.MustCompile(`^[A-Za-z0-9._~-]+$`).MatchString(verifier) {
		t.Errorf("verifier %q", verifier)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("the challenge is not the S256 of the verifier")
	}
	if v2, _ := NewPKCE(); v2 == verifier {
		t.Error("two verifiers are the same")
	}

	md := &Metadata{AuthorizationEndpoint: "https://auth.example.com/authorize?tenant=t1", Resource: "https://mcp.example.com/mcp"}
	raw := AuthURL(md, &Client{ClientID: "cid"}, "https://socrates.example.com/cb", "st4te", challenge, "mcp:read mcp:write")
	u, err := url.Parse(raw)
	if err != nil || u.Host != "auth.example.com" || u.Path != "/authorize" {
		t.Fatalf("URL %s", raw)
	}
	q := u.Query()
	want := url.Values{
		"tenant": {"t1"}, "response_type": {"code"}, "client_id": {"cid"}, "redirect_uri": {"https://socrates.example.com/cb"},
		"state": {"st4te"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"resource": {"https://mcp.example.com/mcp"}, "scope": {"mcp:read mcp:write"},
	}
	if q.Encode() != want.Encode() {
		t.Errorf("query %v\nwant  %v", q, want)
	}
	if q := mustQuery(t, AuthURL(md, &Client{ClientID: "cid"}, "https://x/cb", "s", challenge, "")); q.Has("scope") {
		t.Error("an empty scope was sent")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// tokenServer is a token and revocation endpoint. answer gives the JSON
// for a token request's form; revoked records the revocation requests.
type tokenServer struct {
	*httptest.Server
	mu      sync.Mutex
	forms   []url.Values
	basic   []string
	revoked []string
}

func newTokenServer(t *testing.T, answer func(url.Values) (int, string)) *tokenServer {
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = r.ParseForm()
		user, pass, ok := r.BasicAuth()
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		if ok {
			ts.basic = append(ts.basic, user+":"+pass)
		} else {
			ts.basic = append(ts.basic, "")
		}
		if r.URL.Path == "/revoke" {
			ts.revoked = append(ts.revoked, r.PostForm.Get("token_type_hint")+"="+r.PostForm.Get("token"))
			ts.mu.Unlock()
			return
		}
		ts.mu.Unlock()
		status, body := answer(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) metadata() *Metadata {
	return &Metadata{Resource: "https://mcp.example.com/mcp", TokenEndpoint: ts.URL + "/token", RevocationEndpoint: ts.URL + "/revoke"}
}

func TestExchange(t *testing.T) {
	ts := newTokenServer(t, func(f url.Values) (int, string) {
		if f.Get("code") == "stale" {
			return http.StatusBadRequest, `{"error": "invalid_grant", "error_description": "The code has expired."}`
		}
		return http.StatusOK, `{"access_token": "at-1", "token_type": "bearer", "refresh_token": "rt-1", "expires_in": 3600, "scope": "mcp"}`
	})
	md := ts.metadata()

	tok, err := Exchange(context.Background(), md, &Client{ClientID: "c i/d", ClientSecret: "s&cret"}, "code-1", "verifier-1", "https://x/cb")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-1" || tok.TokenType != "bearer" || tok.RefreshToken != "rt-1" || tok.Scope != "mcp" {
		t.Errorf("token %+v", tok)
	}
	if left := time.Until(tok.ExpiresAt); left < 59*time.Minute || left > time.Hour {
		t.Errorf("expires in %v", left)
	}
	if tok.Expired(time.Minute) || !tok.Expired(2*time.Hour) {
		t.Error("Expired is wrong")
	}
	ts.mu.Lock()
	form, basic := ts.forms[0], ts.basic[0]
	ts.mu.Unlock()
	want := url.Values{
		"grant_type": {"authorization_code"}, "code": {"code-1"}, "code_verifier": {"verifier-1"},
		"redirect_uri": {"https://x/cb"}, "resource": {"https://mcp.example.com/mcp"}, "client_id": {"c i/d"},
	}
	if form.Encode() != want.Encode() {
		t.Errorf("form %v\nwant %v", form, want)
	}
	if basic != url.QueryEscape("c i/d")+":"+url.QueryEscape("s&cret") {
		t.Errorf("basic auth %q", basic)
	}

	if _, err := Exchange(context.Background(), md, &Client{ClientID: "pub"}, "code-2", "v", "https://x/cb"); err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	if ts.basic[1] != "" || ts.forms[1].Get("client_id") != "pub" {
		t.Errorf("a public client sent %q, form %v", ts.basic[1], ts.forms[1])
	}
	ts.mu.Unlock()

	_, err = Exchange(context.Background(), md, &Client{ClientID: "pub"}, "stale", "v", "https://x/cb")
	if err == nil || err.Error() != "the token endpoint answered invalid_grant: The code has expired." {
		t.Errorf("error %v", err)
	}
}

func TestRefreshAndRevoke(t *testing.T) {
	ts := newTokenServer(t, func(f url.Values) (int, string) {
		if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rt-1" || f.Get("resource") == "" {
			return http.StatusBadRequest, `{"error": "invalid_request"}`
		}
		return http.StatusOK, `{"access_token": "at-2", "token_type": "Bearer", "expires_in": "60"}`
	})
	md := ts.metadata()
	c := &Client{ClientID: "cid"}
	old := &Token{AccessToken: "at-1", TokenType: "Bearer", RefreshToken: "rt-1", Scope: "mcp"}
	tok, err := Refresh(context.Background(), md, c, old)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-2" || tok.RefreshToken != "rt-1" || tok.Scope != "mcp" {
		t.Errorf("token %+v", tok)
	}
	if left := time.Until(tok.ExpiresAt); left < 50*time.Second || left > time.Minute {
		t.Errorf("expires in %v", left)
	}
	if _, err := Refresh(context.Background(), md, c, &Token{AccessToken: "x"}); err == nil {
		t.Error("Refresh without a refresh token succeeded")
	}

	if err := Revoke(context.Background(), md, c, tok); err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	if want := []string{"refresh_token=rt-1", "access_token=at-2"}; !slices.Equal(ts.revoked, want) {
		t.Errorf("revoked %v, want %v", ts.revoked, want)
	}
	ts.mu.Unlock()
	if err := Revoke(context.Background(), &Metadata{}, c, tok); err != nil {
		t.Errorf("Revoke without an endpoint: %v", err)
	}
}

func TestTokenJSON(t *testing.T) {
	b, _ := json.Marshal(Token{AccessToken: "a", TokenType: "Bearer"})
	if string(b) != `{"access_token":"a","token_type":"Bearer"}` {
		t.Errorf("JSON %s", b)
	}
	if (&Token{AccessToken: "a"}).Expired(time.Hour) {
		t.Error("a token of unknown expiry is expired")
	}
}
