package mcpclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The OAuth client follows the MCP authorization specification (2025-06-18
// and 2025-11-25): protected resource metadata (RFC 9728), authorization
// server metadata (RFC 8414 and OpenID Connect discovery), dynamic client
// registration (RFC 7591), the authorization code grant with PKCE (S256) and
// resource indicators (RFC 8707), refresh, and revocation (RFC 7009).

const (
	// oauthTimeout bounds one OAuth request.
	oauthTimeout = 30 * time.Second
	// maxOAuthBody bounds an OAuth answer.
	maxOAuthBody = 1 << 20
)

// oauthClient makes every OAuth request. It follows a redirect only to a
// secure URL.
var oauthClient = &http.Client{
	Timeout: oauthTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !secureURL(req.URL) {
			return fmt.Errorf("refused a redirect to %s: it is not https", req.URL.Redacted())
		}
		return nil
	},
}

// Metadata is what discovery found.
type Metadata struct {
	// Resource is the canonical resource URI of the MCP server (RFC 8707,
	// RFC 9728), sent with every authorization and token request.
	Resource string
	// ResourceMetadataURL is where the protected resource metadata was
	// found; empty when the server has none.
	ResourceMetadataURL string
	// AuthorizationServer is the authorization server's issuer.
	AuthorizationServer   string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	RevocationEndpoint    string
	// ScopesSupported are the scopes the resource (or else the
	// authorization server) names.
	ScopesSupported      []string
	CodeChallengeMethods []string
	// ClientIDMetadataDocumentSupported is whether the authorization server
	// accepts a URL as client_id (2025-11-25). It is for information only.
	ClientIDMetadataDocumentSupported bool
}

// resourceMetadata is a protected resource metadata document (RFC 9728).
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// serverMetadata is an authorization server metadata document (RFC 8414).
type serverMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
}

// Discover finds out how to get an access token for the MCP server at
// mcpURL, following the MCP authorization specification. auth is the
// server's refusal, when there was one: the protected resource metadata URL
// it names is tried first.
//
// The protected resource metadata (RFC 9728) comes from that URL, else from
// /.well-known/oauth-protected-resource with the server's path appended
// (then without it). Its first authorization server's metadata comes from
// /.well-known/oauth-authorization-server{path},
// /.well-known/openid-configuration{path} or {path}/.well-known/openid-configuration.
// A server without protected resource metadata is treated as the 2025-03-26
// specification says: the authorization server is the URL's origin, and
// without metadata its endpoints are /authorize, /token and /register.
//
// Endpoints must be https, but for loopback hosts (localhost, 127.0.0.1, ::1).
func Discover(ctx context.Context, mcpURL string, auth *AuthRequiredError) (*Metadata, error) {
	u, err := checkURL(mcpURL)
	if err != nil {
		return nil, err
	}
	if !secureURL(u) {
		return nil, fmt.Errorf("OAuth needs an https URL, and %s is not one", u.Redacted())
	}
	md := &Metadata{Resource: canonicalResource(u)}

	var candidates []string
	if auth != nil && auth.ResourceMetadata != "" {
		if ref, err := url.Parse(auth.ResourceMetadata); err == nil {
			candidates = append(candidates, u.ResolveReference(ref).String())
		}
	}
	candidates = append(candidates, wellKnown(u, "oauth-protected-resource")...)
	prm, prmURL, err := fetchFirst(ctx, candidates, func(d *resourceMetadata) bool {
		return len(d.AuthorizationServers) > 0 || d.Resource != ""
	})
	if err != nil {
		return nil, err
	}
	hasEndpoints := func(d *serverMetadata) bool { return d.AuthorizationEndpoint != "" || d.TokenEndpoint != "" }

	var asm *serverMetadata
	if prm != nil {
		md.ResourceMetadataURL = prmURL
		if prm.Resource != "" {
			if !resourceCovers(prm.Resource, u) {
				return nil, fmt.Errorf("the protected resource metadata at %s is for %s, not for %s", prmURL, prm.Resource, u.Redacted())
			}
			md.Resource = prm.Resource
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, fmt.Errorf("the protected resource metadata at %s names no authorization server", prmURL)
		}
		issuer, err := url.Parse(prm.AuthorizationServers[0])
		if err != nil || !issuer.IsAbs() || !secureURL(issuer) {
			return nil, fmt.Errorf("the authorization server %q is not an https URL", prm.AuthorizationServers[0])
		}
		md.AuthorizationServer = prm.AuthorizationServers[0]
		md.ScopesSupported = prm.ScopesSupported
		asm, _, err = fetchFirst(ctx, serverMetadataURLs(issuer), hasEndpoints)
		if err != nil {
			return nil, err
		}
		if asm == nil {
			return nil, fmt.Errorf("found no metadata for the authorization server %s", md.AuthorizationServer)
		}
		if !sameIssuer(asm.Issuer, md.AuthorizationServer) {
			return nil, fmt.Errorf("the metadata of the authorization server %s names another issuer (%s)", md.AuthorizationServer, asm.Issuer)
		}
	} else {
		// The 2025-03-26 rule: the authorization server is the origin.
		origin := &url.URL{Scheme: u.Scheme, Host: u.Host}
		md.AuthorizationServer = origin.String()
		asm, _, err = fetchFirst(ctx, serverMetadataURLs(origin), hasEndpoints)
		if err != nil {
			return nil, err
		}
		if asm == nil {
			asm = &serverMetadata{
				AuthorizationEndpoint: origin.String() + "/authorize",
				TokenEndpoint:         origin.String() + "/token",
				RegistrationEndpoint:  origin.String() + "/register",
			}
		} else if asm.Issuer != "" && !sameIssuer(asm.Issuer, md.AuthorizationServer) {
			return nil, fmt.Errorf("the metadata of the authorization server %s names another issuer (%s)", md.AuthorizationServer, asm.Issuer)
		}
	}

	if asm.Issuer != "" {
		md.AuthorizationServer = asm.Issuer
	}
	md.AuthorizationEndpoint = asm.AuthorizationEndpoint
	md.TokenEndpoint = asm.TokenEndpoint
	md.RegistrationEndpoint = asm.RegistrationEndpoint
	md.RevocationEndpoint = asm.RevocationEndpoint
	md.CodeChallengeMethods = asm.CodeChallengeMethodsSupported
	md.ClientIDMetadataDocumentSupported = asm.ClientIDMetadataDocumentSupported
	if len(md.ScopesSupported) == 0 {
		md.ScopesSupported = asm.ScopesSupported
	}

	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" {
		return nil, fmt.Errorf("the authorization server %s names no authorization or no token endpoint", md.AuthorizationServer)
	}
	for _, ep := range []struct{ name, url string }{
		{"authorization endpoint", md.AuthorizationEndpoint},
		{"token endpoint", md.TokenEndpoint},
		{"registration endpoint", md.RegistrationEndpoint},
		{"revocation endpoint", md.RevocationEndpoint},
	} {
		if ep.url == "" {
			continue
		}
		pu, err := url.Parse(ep.url)
		if err != nil || !pu.IsAbs() || !secureURL(pu) {
			return nil, fmt.Errorf("the %s %s is not an https URL", ep.name, ep.url)
		}
	}
	if len(md.CodeChallengeMethods) > 0 && !slices.Contains(md.CodeChallengeMethods, "S256") {
		return nil, fmt.Errorf("the authorization server %s does not support PKCE with S256", md.AuthorizationServer)
	}
	return md, nil
}

// secureURL is whether u is https, or http on a loopback host.
func secureURL(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

// canonicalResource is the canonical URI of an MCP server: scheme and host
// lowercased, no fragment, and no trailing slash on an empty path.
func canonicalResource(u *url.URL) string {
	c := *u
	c.Scheme = strings.ToLower(c.Scheme)
	c.Host = strings.ToLower(c.Host)
	c.Fragment, c.RawFragment = "", ""
	c.User = nil
	if c.Path == "/" {
		c.Path, c.RawPath = "", ""
	}
	return c.String()
}

// resourceCovers is whether the resource a metadata document names covers
// the MCP server's URL: the same origin, and a path that is the server's or
// one of its parents.
func resourceCovers(resource string, u *url.URL) bool {
	r, err := url.Parse(resource)
	if err != nil {
		return false
	}
	if !strings.EqualFold(r.Scheme, u.Scheme) || !strings.EqualFold(r.Host, u.Host) {
		return false
	}
	rp := strings.TrimSuffix(r.Path, "/")
	up := strings.TrimSuffix(u.Path, "/")
	return rp == "" || up == rp || strings.HasPrefix(up, rp+"/")
}

// wellKnown are the well-known URLs of a document for u: with u's path
// appended, then at the root.
func wellKnown(u *url.URL, name string) []string {
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	if path == "" {
		return []string{origin + "/.well-known/" + name}
	}
	return []string{origin + "/.well-known/" + name + path, origin + "/.well-known/" + name}
}

// serverMetadataURLs are the URLs an authorization server's metadata is
// looked for at, in the order of the MCP specification.
func serverMetadataURLs(issuer *url.URL) []string {
	origin := issuer.Scheme + "://" + issuer.Host
	path := strings.TrimSuffix(issuer.EscapedPath(), "/")
	if path == "" {
		return []string{
			origin + "/.well-known/oauth-authorization-server",
			origin + "/.well-known/openid-configuration",
		}
	}
	return []string{
		origin + "/.well-known/oauth-authorization-server" + path,
		origin + "/.well-known/openid-configuration" + path,
		origin + path + "/.well-known/openid-configuration",
	}
}

// sameIssuer compares two issuers, ignoring a trailing slash.
func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// fetchFirst gets the first of the URLs that answers with a JSON document
// that ok accepts, and returns it with its URL. A URL that answers otherwise
// is skipped; none at all gives nil. Only a host that cannot be reached is
// an error.
func fetchFirst[T any](ctx context.Context, urls []string, ok func(*T) bool) (*T, string, error) {
	tried := map[string]bool{}
	for _, target := range urls {
		if tried[target] {
			continue
		}
		tried[target] = true
		u, err := url.Parse(target)
		if err != nil || !u.IsAbs() || !secureURL(u) {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
		resp, err := oauthClient.Do(req)
		if err != nil {
			return nil, "", requestError(ctx, target, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		var doc T
		if json.Unmarshal(body, &doc) == nil && ok(&doc) {
			return &doc, target, nil
		}
	}
	return nil, "", nil
}

// Client is an OAuth client registered with an authorization server.
type Client struct {
	ClientID                string
	ClientSecret            string
	RegistrationAccessToken string `json:",omitempty"`
}

// Register registers Socrates as a public client with the authorization
// server (RFC 7591 dynamic client registration).
func Register(ctx context.Context, md *Metadata, redirectURI, clientName string) (*Client, error) {
	if md.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("the authorization server %s does not offer dynamic client registration", md.AuthorizationServer)
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	status, data, err := do(ctx, req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return nil, oauthError("the registration endpoint", status, data)
	}
	var reg struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		RegistrationAccessToken string `json:"registration_access_token"`
	}
	if err := json.Unmarshal(data, &reg); err != nil || reg.ClientID == "" {
		return nil, errors.New("the registration endpoint gave no client_id")
	}
	return &Client{ClientID: reg.ClientID, ClientSecret: reg.ClientSecret, RegistrationAccessToken: reg.RegistrationAccessToken}, nil
}

// NewPKCE returns a new PKCE code verifier (64 characters from crypto/rand)
// and its S256 code challenge.
func NewPKCE() (verifier, challenge string) {
	b := make([]byte, 48)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, pkceChallenge(verifier)
}

// pkceChallenge is the S256 code challenge of a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthURL is the authorization request to send the person's browser to:
// the authorization code flow with PKCE (S256), the resource indicator, and
// the scope when there is one.
func AuthURL(md *Metadata, c *Client, redirectURI, state, challenge, scope string) string {
	u, err := url.Parse(md.AuthorizationEndpoint)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if md.Resource != "" {
		q.Set("resource", md.Resource)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Token is an OAuth access token, with its refresh token.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// ExpiresAt is when the access token expires; zero when unknown.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Expired is whether the access token expires within margin. A token whose
// expiry is unknown is not expired; no token is.
func (t *Token) Expired(margin time.Duration) bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	if t.ExpiresAt.IsZero() {
		return false
	}
	return !time.Now().Add(margin).Before(t.ExpiresAt)
}

// Exchange trades an authorization code for a token.
func Exchange(ctx context.Context, md *Metadata, c *Client, code, verifier, redirectURI string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	return tokenRequest(ctx, md, c, form, nil)
}

// Refresh gets a new access token with the refresh token. The old refresh
// token is kept when the answer has no new one.
func Refresh(ctx context.Context, md *Metadata, c *Client, t *Token) (*Token, error) {
	if t == nil || t.RefreshToken == "" {
		return nil, errors.New("there is no refresh token; sign in again")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.RefreshToken)
	return tokenRequest(ctx, md, c, form, t)
}

// tokenRequest posts a grant to the token endpoint. old, when set, gives
// the refresh token and scope the answer does not repeat.
func tokenRequest(ctx context.Context, md *Metadata, c *Client, form url.Values, old *Token) (*Token, error) {
	if md.TokenEndpoint == "" {
		return nil, errors.New("the authorization server names no token endpoint")
	}
	if md.Resource != "" {
		form.Set("resource", md.Resource)
	}
	req, err := clientRequest(ctx, md.TokenEndpoint, c, form)
	if err != nil {
		return nil, err
	}
	status, data, err := do(ctx, req)
	if err != nil {
		return nil, err
	}
	var answer struct {
		AccessToken  string          `json:"access_token"`
		TokenType    string          `json:"token_type"`
		RefreshToken string          `json:"refresh_token"`
		Scope        string          `json:"scope"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		Error        string          `json:"error"`
	}
	// Some servers answer an error with 200 (GitHub, for one).
	if status != http.StatusOK || json.Unmarshal(data, &answer) != nil || answer.Error != "" {
		return nil, oauthError("the token endpoint", status, data)
	}
	if answer.AccessToken == "" {
		return nil, errors.New("the token endpoint gave no access_token")
	}
	t := &Token{AccessToken: answer.AccessToken, TokenType: answer.TokenType, RefreshToken: answer.RefreshToken, Scope: answer.Scope}
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	if secs := expiresIn(answer.ExpiresIn); secs > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(secs) * time.Second)
	}
	if old != nil {
		if t.RefreshToken == "" {
			t.RefreshToken = old.RefreshToken
		}
		if t.Scope == "" {
			t.Scope = old.Scope
		}
	}
	return t, nil
}

// expiresIn reads expires_in, a number or (from some servers) a string.
func expiresIn(raw json.RawMessage) int64 {
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0
		}
		n = json.Number(s)
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return int64(f)
	}
	return 0
}

// Revoke revokes the token at the authorization server (RFC 7009): the
// refresh token, then the access token. It is best effort; without a
// revocation endpoint it does nothing.
func Revoke(ctx context.Context, md *Metadata, c *Client, t *Token) error {
	if md.RevocationEndpoint == "" || t == nil {
		return nil
	}
	var first error
	for _, tok := range []struct{ value, hint string }{
		{t.RefreshToken, "refresh_token"},
		{t.AccessToken, "access_token"},
	} {
		if tok.value == "" {
			continue
		}
		form := url.Values{}
		form.Set("token", tok.value)
		form.Set("token_type_hint", tok.hint)
		req, err := clientRequest(ctx, md.RevocationEndpoint, c, form)
		if err == nil {
			var status int
			var data []byte
			status, data, err = do(ctx, req)
			if err == nil && status != http.StatusOK {
				err = oauthError("the revocation endpoint", status, data)
			}
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// clientRequest is a form post to an endpoint, with the client's id in the
// form and its secret, when it has one, as HTTP Basic authentication.
func clientRequest(ctx context.Context, endpoint string, c *Client, form url.Values) (*http.Request, error) {
	if c != nil && c.ClientID != "" {
		form.Set("client_id", c.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c != nil && c.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.ClientSecret))
	}
	return req, nil
}

// do sends an OAuth request and reads its answer.
func do(ctx context.Context, req *http.Request) (int, []byte, error) {
	resp, err := oauthClient.Do(req)
	if err != nil {
		return 0, nil, requestError(ctx, req.URL.String(), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if err != nil {
		return 0, nil, requestError(ctx, req.URL.String(), err)
	}
	return resp.StatusCode, data, nil
}

// oauthError explains an OAuth error answer: its error and
// error_description, else its HTTP status.
func oauthError(who string, status int, data []byte) error {
	var e struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		msg := who + " answered " + e.Error
		if e.ErrorDescription != "" {
			msg += ": " + oneLine(e.ErrorDescription, 300)
		}
		return errors.New(msg)
	}
	text := strconv.Itoa(status)
	if t := http.StatusText(status); t != "" {
		text += " " + t
	}
	if status == http.StatusOK {
		return errors.New(who + " gave an answer that is not valid JSON")
	}
	return errors.New(who + " answered " + text)
}
