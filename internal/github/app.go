package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ParsePrivateKey reads the App's private key as GitHub hands it out (PKCS#1,
// "RSA PRIVATE KEY"), or as PKCS#8.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("the App's private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the App's private key cannot be read: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the App's private key is not an RSA key")
	}
	return rk, nil
}

// AppJWT is the App's JSON Web Token at now: RS256 over iat a minute in the
// past (GitHub's clock may be behind), exp nine minutes ahead (GitHub takes
// at most ten) and iss the App's id.
func AppJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]int64{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(540 * time.Second).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Manifest is the GitHub App manifest Socrates creates its App from (§3.1).
type Manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	RedirectURL        string            `json:"redirect_url"`
	SetupURL           string            `json:"setup_url"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
	Public             bool              `json:"public"`
	HookAttributes     HookAttributes    `json:"hook_attributes"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// HookAttributes is the manifest's webhook, which Socrates keeps switched
// off: it asks GitHub when it needs to know.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// maxAppName is the longest name GitHub takes for an App.
const maxAppName = 34

// NewManifest is the manifest of an App for the Socrates the browser has at
// origin ("https://host[:port]"). It may write contents and pull requests,
// and workflows too, since a push that changes .github/workflows is refused
// without it.
func NewManifest(origin string) Manifest {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	host := origin
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	name := "Socrates " + host
	if len(name) > maxAppName {
		name = strings.TrimRight(name[:maxAppName], ".-")
	}
	return Manifest{
		Name:           name,
		URL:            origin,
		RedirectURL:    origin + "/api/github/app/created",
		SetupURL:       origin + "/api/github/app/installed",
		SetupOnUpdate:  true,
		Public:         false,
		HookAttributes: HookAttributes{URL: origin + "/api/github/hook", Active: false},
		DefaultPermissions: map[string]string{
			"contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read",
		},
	}
}

// ManifestAction is where the browser posts the manifest form: the new App
// page of the person's account, or of the organisation org.
func (s *Service) ManifestAction(org, state string) string {
	q := "?state=" + url.QueryEscape(state)
	if org = strings.TrimSpace(org); org != "" {
		return s.cfg.Web + "/organizations/" + url.PathEscape(org) + "/settings/apps/new" + q
	}
	return s.cfg.Web + "/settings/apps/new" + q
}

// Conversion is what GitHub hands out once for a manifest's code: the App's
// facts and its secrets.
type Conversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

// App is the conversion as the App the service acts as.
func (c Conversion) App() *App {
	return &App{ID: c.ID, Slug: c.Slug, Name: c.Name, Owner: c.Owner.Login, HTMLURL: c.HTMLURL, PEM: []byte(c.PEM)}
}

// ConvertManifest trades the code GitHub's redirect carries for the App it
// created (POST /app-manifests/{code}/conversions, without authentication).
func (s *Service) ConvertManifest(ctx context.Context, code string) (Conversion, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Conversion{}, errors.New("GitHub sent no code")
	}
	var out Conversion
	if err := s.call(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &out); err != nil {
		return Conversion{}, err
	}
	if out.ID == 0 || out.Slug == "" || out.PEM == "" {
		return Conversion{}, errors.New("GitHub's answer has no App in it")
	}
	if _, err := ParsePrivateKey([]byte(out.PEM)); err != nil {
		return Conversion{}, err
	}
	return out, nil
}
