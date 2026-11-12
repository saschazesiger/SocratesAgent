package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/saschazesiger/SocratesAgent/internal/cloudflare"
)

func TestCloudflareEndpoints(t *testing.T) {
	env := signedIn(t)
	if res, _ := env.do(t, env.anon, "GET", "/api/cloudflare", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the tunnel API must require a session, got %d", res.StatusCode)
	}
	_, data := env.do(t, env.client, "GET", "/api/cloudflare", "")
	status, _ := data["status"].(map[string]any)
	if status == nil || status["configured"] != false || status["partial"] != false {
		t.Fatalf("a fresh install should have no tunnel: %#v", data)
	}
	if !strings.HasPrefix(data["token_url"].(string), "https://dash.cloudflare.com/profile/api-tokens?") ||
		len(data["permissions"].([]any)) != 6 || data["via"] != "local" {
		t.Fatalf("payload = %#v", data)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/cloudflare/check", `{"api_token":""}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("checking without a stored token = %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/cloudflare/setup", `{"api_token":"x","zone_id":"z","hostname":"a.example.com","emails":[]}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a setup without addresses = %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "PUT", "/api/cloudflare/emails", `{"emails":["a@example.com"]}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("addresses without a tunnel = %d", res.StatusCode)
	}
	// The settings document no longer knows a tunnel.
	_, settings := env.do(t, env.client, "GET", "/api/settings", "")
	if _, ok := settings["settings"].(map[string]any)["tunnel"]; ok {
		t.Error("the settings still carry a tunnel")
	}
}

// accessFixture is a Cloudflare Access team: its signing key, served as a
// key set, and a tunnel set up for it in the server's store.
type accessFixture struct {
	key    *rsa.PrivateKey
	tunnel *httptest.Server
}

func newAccessFixture(t *testing.T, env *testEnv) *accessFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(certs.Close)
	state, _ := json.Marshal(map[string]any{
		"ready": true, "hostname": "socrates.example.com", "team_domain": "team.cloudflareaccess.com",
		"app_aud": "aud-main", "emails": []string{"owner@example.com"}, "origin": "unix:" + env.srv.TunnelSocket(),
	})
	if err := env.store.SetKV("cloudflare:state", string(state)); err != nil {
		t.Fatal(err)
	}
	env.srv.cf = env.srv.newCloudflare()
	env.srv.cf.CertsURL = func(string) string { return certs.URL }
	f := &accessFixture{key: key, tunnel: httptest.NewServer(env.srv.TunnelHandler())}
	t.Cleanup(f.tunnel.Close)
	return f
}

func (f *accessFixture) token(t *testing.T, email string) string {
	t.Helper()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	in := enc(map[string]any{"alg": "RS256", "kid": "k1"}) + "." + enc(map[string]any{
		"iss": "https://team.cloudflareaccess.com", "aud": []string{"aud-main"}, "email": email,
		"exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
	})
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// call sends a request through the tunnel's handler, as cloudflared would
// with the Access token Cloudflare adds.
func (f *accessFixture) call(t *testing.T, client *http.Client, method, path, jwt, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.tunnel.URL+path, strings.NewReader(body))
	if jwt != "" {
		req.Header.Set("Cf-Access-Jwt-Assertion", jwt)
	}
	req.Header.Set("Cf-Connecting-Ip", "198.51.100.7")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// Through the tunnel, Cloudflare Access is the login: a valid token signs
// the person in without the password, anything else is refused, and the
// sandboxed pages pass with their own tokens.
func TestTunnelSignsInWithCloudflareAccess(t *testing.T) {
	env := signedIn(t)
	f := newAccessFixture(t, env)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, jwt := range []string{"", f.token(t, "mallory@example.com"), "a.b.c"} {
		res, body := f.call(t, browser, "GET", "/api/home", jwt, "")
		if res.StatusCode != http.StatusForbidden || res.Header.Get(cloudflare.MarkerHeader) == "" || !strings.Contains(body, "Cloudflare Access") {
			t.Fatalf("token %q: %d %q", jwt, res.StatusCode, body)
		}
	}
	// The sandboxed pages are let through and keep their own guard.
	if res, _ := f.call(t, browser, "GET", "/artifact-kit/nothing-here", "", ""); res.StatusCode == http.StatusForbidden {
		t.Fatalf("the artifact kit was refused through the tunnel: %d", res.StatusCode)
	}
	if res, _ := f.call(t, browser, "GET", "/docs/not-a-token/a.md", "", ""); res.StatusCode == http.StatusOK {
		t.Fatal("a page without its token was served")
	}

	jwt := f.token(t, "Owner@Example.com")
	res, body := f.call(t, browser, "GET", "/api/state", jwt, "")
	var state map[string]any
	_ = json.Unmarshal([]byte(body), &state)
	if res.StatusCode != http.StatusOK || state["authenticated"] != true || state["via"] != "tunnel" || state["email"] != "owner@example.com" {
		t.Fatalf("state through the tunnel = %d %s", res.StatusCode, body)
	}
	if res, body := f.call(t, browser, "GET", "/api/home", jwt, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("an API call through the tunnel = %d %s", res.StatusCode, body)
	}
	if res, _ := f.call(t, browser, "GET", "/login", jwt, ""); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
		t.Fatalf("the login page through the tunnel = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// The live streams take the Access login too: the events socket says
	// hello, with no Socrates cookie anywhere.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.tunnel.URL, "http")+"/api/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cf-Access-Jwt-Assertion": {jwt}, "Origin": {f.tunnel.URL}},
	})
	if err != nil {
		t.Fatalf("the events socket through the tunnel: %v", err)
	}
	_, hello, err := ws.Read(ctx)
	if err != nil || !strings.Contains(string(hello), `"hello"`) {
		t.Fatalf("first frame = %s, %v", hello, err)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
	if !env.srv.stillSignedIn(streamLogin{email: "owner@example.com"}) || env.srv.stillSignedIn(streamLogin{email: "mallory@example.com"}) {
		t.Error("a stream's Access login is judged wrong")
	}

	// Keeping one's own address is enforced through the tunnel.
	res, body = f.call(t, browser, "PUT", "/api/cloudflare/emails", jwt, `{"emails":["other@example.com"]}`)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "owner@example.com") {
		t.Fatalf("removing one's own address = %d %s", res.StatusCode, body)
	}
	// Signing out ends the Access session.
	res, body = f.call(t, browser, "POST", "/api/logout", jwt, "{}")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "/cdn-cgi/access/logout") {
		t.Fatalf("logout through the tunnel = %d %s", res.StatusCode, body)
	}

	// The local address does not believe the header: a token there is
	// nothing, the password is the login.
	local := &http.Client{}
	req, _ := http.NewRequest("GET", env.server.URL+"/api/state", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", jwt)
	lres, err := local.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var lstate map[string]any
	_ = json.NewDecoder(lres.Body).Decode(&lstate)
	lres.Body.Close()
	if lstate["authenticated"] != false || lstate["via"] != "local" || lstate["email"] != nil {
		t.Fatalf("state on the local address with a token = %#v", lstate)
	}
}
