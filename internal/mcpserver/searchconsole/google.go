package searchconsole

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google's addresses; Deps may point them elsewhere (tests).
const (
	googleToken   = "https://oauth2.googleapis.com/token"
	googleAPI     = "https://www.googleapis.com/webmasters/v3"
	googleInspect = "https://searchconsole.googleapis.com/v1"
)

// scope is read-only: the server lists, queries and inspects, and never
// changes a property, a user or a sitemap.
const scope = "https://www.googleapis.com/auth/webmasters.readonly"

// serviceKey is a service account's JSON key file, as Google Cloud gives it.
type serviceKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`

	rsa *rsa.PrivateKey
}

// errKey is a key that is not a usable service account key: Admin's check
// calls it invalid.
type errKey struct{ why string }

func (e errKey) Error() string { return e.why }

// parseKey reads a service account's JSON key file.
func parseKey(raw string) (*serviceKey, error) {
	var k serviceKey
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &k); err != nil {
		return nil, errKey{"the key is not a JSON key file of a service account"}
	}
	if k.Type != "service_account" {
		what := k.Type
		if what == "" {
			what = "no"
		}
		return nil, errKey{"the key is of type " + what + ", not a service account's (a JSON key file from Google Cloud → IAM → Service accounts → Keys)"}
	}
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, errKey{"the key file has no client_email or no private_key"}
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return nil, errKey{"the key file's private_key is not a PEM key"}
	}
	var parsed any
	var err error
	if parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		if parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, errKey{"the key file's private_key cannot be read"}
		}
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errKey{"the key file's private_key is not an RSA key"}
	}
	k.rsa = key
	return &k, nil
}

// emailOf is the service account's address of a key file, "" when it has
// none; it does not read the private key.
func emailOf(raw string) string {
	var k struct {
		ClientEmail string `json:"client_email"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &k)
	return strings.ToLower(strings.TrimSpace(k.ClientEmail))
}

// assertion is the signed JWT a service account trades for an access token
// (RFC 7523, as Google has it).
func (k *serviceKey) assertion(aud string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{
		"iss": k.ClientEmail, "scope": scope, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, k.rsa, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// errRefused is Google refusing the service account itself (a deleted or
// disabled key, a wrong project): Admin's check calls it invalid.
type errRefused struct{ why string }

func (e errRefused) Error() string { return e.why }

// token is an access token and when it runs out.
type token struct {
	value string
	until time.Time
}

// fetchToken trades a key for an access token.
func (s *Server) fetchToken(ctx context.Context, k *serviceKey) (token, error) {
	now := s.now()
	jwt, err := k.assertion(s.tokenURL(), now)
	if err != nil {
		return token{}, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client().Do(req)
	if err != nil {
		return token{}, fmt.Errorf("Google's sign-in could not be reached: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		why := strings.TrimSpace(out.Error + ": " + out.Description)
		if out.Error == "" {
			why = resp.Status
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return token{}, errRefused{"Google refused the service account's key (" + strings.TrimSuffix(why, ": ") + ")"}
		}
		return token{}, fmt.Errorf("Google's sign-in answered %s", strings.TrimSuffix(why, ": "))
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	return token{value: out.AccessToken, until: now.Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
}

func tokenID(k *serviceKey) string { return k.ClientEmail + "\x00" + k.PrivateKeyID }

// forget drops the token kept for a key.
func (s *Server) forget(k *serviceKey) {
	s.mu.Lock()
	delete(s.tokens, tokenID(k))
	s.mu.Unlock()
}

// accessToken is a token for a key: the kept one while it lasts another
// minute, else a new one.
func (s *Server) accessToken(ctx context.Context, k *serviceKey, fresh bool) (string, error) {
	id := tokenID(k)
	s.mu.Lock()
	t, ok := s.tokens[id]
	s.mu.Unlock()
	if ok && !fresh && s.now().Add(time.Minute).Before(t.until) {
		return t.value, nil
	}
	t, err := s.fetchToken(ctx, k)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[id] = t
	s.mu.Unlock()
	return t.value, nil
}

// apiError is what the API answered when it refused a call.
type apiError struct {
	code int
	msg  string
}

func (e apiError) Error() string {
	switch e.code {
	case http.StatusForbidden:
		return "Search Console refused it (403): " + e.msg
	case http.StatusNotFound:
		return "Search Console does not know it (404): " + e.msg
	case http.StatusTooManyRequests:
		return "Search Console's quota is used up for now (429): " + e.msg
	}
	return fmt.Sprintf("Search Console answered %d: %s", e.code, e.msg)
}

// api is one account's way to the API.
type api struct {
	s   *Server
	key *serviceKey
}

// do calls the API with the account's token, again with a fresh one if the
// kept one was refused, and decodes the answer into out.
func (a api) do(ctx context.Context, method, addr string, in, out any) error {
	var payload []byte
	if in != nil {
		payload, _ = json.Marshal(in)
	}
	for attempt := 0; ; attempt++ {
		tok, err := a.s.accessToken(ctx, a.key, attempt > 0)
		if err != nil {
			return err
		}
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, addr, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.s.client().Do(req)
		if err != nil {
			return fmt.Errorf("Search Console could not be reached: %w", err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("Search Console's answer could not be read: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode/100 != 2 {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &e)
			msg := strings.TrimSpace(e.Error.Message)
			if msg == "" {
				msg = strings.TrimSpace(string(raw))
				if len(msg) > 300 {
					msg = msg[:300] + "…"
				}
			}
			if msg == "" {
				msg = resp.Status
			}
			return apiError{code: resp.StatusCode, msg: msg}
		}
		if out == nil || len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Search Console's answer could not be read: %w", err)
		}
		return nil
	}
}

// site is a property as sites.list gives it.
type site struct {
	URL        string `json:"siteUrl"`
	Permission string `json:"permissionLevel"`
}

func (a api) sites(ctx context.Context) ([]site, error) {
	var out struct {
		SiteEntry []site `json:"siteEntry"`
	}
	if err := a.do(ctx, http.MethodGet, a.s.apiBase()+"/sites", nil, &out); err != nil {
		return nil, err
	}
	return out.SiteEntry, nil
}

// sitePath is a property in a path of the API: escaped as one segment, the
// slashes and colon of its URL too.
func sitePath(siteURL string) string { return url.QueryEscape(siteURL) }

func isRefused(err error) bool {
	var r errRefused
	var k errKey
	return errors.As(err, &r) || errors.As(err, &k)
}
