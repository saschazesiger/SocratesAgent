package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

type testEnv struct {
	server *httptest.Server
	client *http.Client
	anon   *http.Client
	store  *store.Store
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv, err := New(st, t.TempDir())
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	noRedirect := func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	return &testEnv{
		server: ts,
		client: &http.Client{Jar: jar, CheckRedirect: noRedirect},
		anon:   &http.Client{CheckRedirect: noRedirect},
		store:  st,
	}
}

func (e *testEnv) do(t *testing.T, client *http.Client, method, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { res.Body.Close() })
	var decoded map[string]any
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(res.Body).Decode(&decoded)
	}
	return res, decoded
}

func TestSetupLoginAndChats(t *testing.T) {
	env := newEnv(t)

	_, state := env.do(t, env.client, "GET", "/api/state", "")
	if state["setup_required"] != true {
		t.Fatalf("expected setup to be required: %#v", state)
	}

	res, _ := env.do(t, env.client, "GET", "/", "")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/setup" {
		t.Fatalf("root should redirect to setup, got %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	res, _ = env.do(t, env.client, "POST", "/api/setup", `{"password":"short"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("short password should be rejected, got %d", res.StatusCode)
	}

	res, _ = env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup failed: %d", res.StatusCode)
	}

	res, _ = env.do(t, env.client, "GET", "/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("root should render after setup, got %d", res.StatusCode)
	}

	res, _ = env.do(t, env.client, "POST", "/api/setup", `{"password":"another"}`)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("setup must only run once, got %d", res.StatusCode)
	}

	// anonymous access is refused
	res, _ = env.do(t, env.anon, "GET", "/api/chats", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous API access should be 401, got %d", res.StatusCode)
	}

	_, created := env.do(t, env.client, "POST", "/api/chats", `{"title":"Test chat"}`)
	chat, _ := created["chat"].(map[string]any)
	chatID, _ := chat["id"].(string)
	if chatID == "" {
		t.Fatalf("no chat id in %#v", created)
	}

	_, list := env.do(t, env.client, "GET", "/api/chats", "")
	chats, _ := list["chats"].([]any)
	if len(chats) != 1 {
		t.Fatalf("expected one chat, got %#v", list)
	}

	res, _ = env.do(t, env.client, "POST", "/api/chats/"+chatID+"/messages", `{"text":"  "}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty message should be rejected, got %d", res.StatusCode)
	}

	_, snapshot := env.do(t, env.client, "GET", "/api/chats/"+chatID, "")
	if snapshot["chat"] == nil || snapshot["messages"] == nil {
		t.Fatalf("snapshot incomplete: %#v", snapshot)
	}

	res, _ = env.do(t, env.client, "DELETE", "/api/chats/"+chatID, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete failed: %d", res.StatusCode)
	}
	_, list = env.do(t, env.client, "GET", "/api/chats", "")
	chats, _ = list["chats"].([]any)
	if len(chats) != 0 {
		t.Fatalf("chat was not deleted: %#v", list)
	}
}

func TestLoginLogout(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.do(t, env.client, "POST", "/api/logout", "")

	res, _ := env.do(t, env.client, "GET", "/api/chats", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout did not end the session: %d", res.StatusCode)
	}
	res, _ = env.do(t, env.client, "POST", "/api/login", `{"password":"wrong"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password accepted: %d", res.StatusCode)
	}
	res, _ = env.do(t, env.client, "POST", "/api/login", `{"password":"a-good-password"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", res.StatusCode)
	}
	res, _ = env.do(t, env.client, "GET", "/api/chats", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session not restored: %d", res.StatusCode)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	_, data := env.do(t, env.client, "GET", "/api/settings", "")
	settings, _ := data["settings"].(map[string]any)
	if settings == nil {
		t.Fatalf("no settings in %#v", data)
	}
	settings["openrouter"].(map[string]any)["chat_model"] = "openai/gpt-5"
	body, _ := json.Marshal(map[string]any{"settings": settings})
	res, saved := env.do(t, env.client, "PUT", "/api/settings", string(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	updated := saved["settings"].(map[string]any)["openrouter"].(map[string]any)
	if updated["chat_model"] != "openai/gpt-5" {
		t.Fatalf("model was not stored: %#v", updated)
	}

	// duplicate agent ids are rejected
	settings["backends"] = []any{
		map[string]any{"id": "same", "kind": "claude", "name": "A", "command": "claude"},
		map[string]any{"id": "same", "kind": "codex", "name": "B", "command": "codex"},
	}
	body, _ = json.Marshal(map[string]any{"settings": settings})
	res, _ = env.do(t, env.client, "PUT", "/api/settings", string(body))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate ids should be rejected, got %d", res.StatusCode)
	}
}

func TestBridgeRequiresToken(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	res, _ := env.do(t, env.client, "POST", "/api/bridge/permission", `{"run_id":"x"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bridge must reject requests without the token, got %d", res.StatusCode)
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(hash, "correct horse") {
		t.Error("valid password rejected")
	}
	if verifyPassword(hash, "wrong horse") {
		t.Error("wrong password accepted")
	}
	if verifyPassword("garbage", "correct horse") {
		t.Error("malformed hash accepted")
	}
	if strings.Contains(hash, "correct") {
		t.Error("hash leaks the password")
	}
}

// fakeCloudflared writes a stand-in binary that answers --version and then
// idles, so the tunnel endpoints can be exercised without the real thing.
func fakeCloudflared(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "cloudflared")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo \"cloudflared version 2026.1.0\"; exit 0;; esac\n" +
		"echo \"|  https://socrates-test.trycloudflare.com  |\" >&2\n" +
		"sleep 20\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTunnelEndpoints(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	res, _ := env.do(t, env.anon, "GET", "/api/tunnel", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the tunnel API must require a session, got %d", res.StatusCode)
	}

	_, data := env.do(t, env.client, "GET", "/api/tunnel", "")
	status := data["status"].(map[string]any)
	if status["state"] != "stopped" {
		t.Fatalf("a fresh install should have no tunnel: %#v", status)
	}
	if data["install"] == nil {
		t.Error("install hints are missing")
	}

	res, _ = env.do(t, env.client, "POST", "/api/tunnel/start", `{"tunnel":{"mode":"token","token":""}}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a named tunnel without a token should be rejected, got %d", res.StatusCode)
	}

	binary := fakeCloudflared(t)
	body := `{"tunnel":{"mode":"quick","command":"` + binary + `"}}`
	res, started := env.do(t, env.client, "POST", "/api/tunnel/start", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start failed: %d", res.StatusCode)
	}
	if started["tunnel"].(map[string]any)["enabled"] != true {
		t.Fatalf("the tunnel should be marked enabled: %#v", started["tunnel"])
	}

	// the public URL shows up once cloudflared prints it
	var url string
	for i := 0; i < 60; i++ {
		_, polled := env.do(t, env.client, "GET", "/api/tunnel", "")
		if s, ok := polled["status"].(map[string]any); ok {
			if v, _ := s["url"].(string); v != "" {
				url = v
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if url != "https://socrates-test.trycloudflare.com" {
		t.Fatalf("public url = %q", url)
	}

	res, stopped := env.do(t, env.client, "POST", "/api/tunnel/stop", `{}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stop failed: %d", res.StatusCode)
	}
	if stopped["tunnel"].(map[string]any)["enabled"] != false {
		t.Fatalf("stop should disable the tunnel: %#v", stopped["tunnel"])
	}
}

func TestSetupAcceptsTunnelConfiguration(t *testing.T) {
	env := newEnv(t)
	binary := fakeCloudflared(t)
	body := `{"password":"a-good-password","tunnel":{"enabled":true,"mode":"quick","command":"` + binary + `"}}`
	res, data := env.do(t, env.client, "POST", "/api/setup", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup failed: %d", res.StatusCode)
	}
	if data["tunnel_error"] != "" {
		t.Fatalf("the tunnel should have started: %v", data["tunnel_error"])
	}
	_, settings := env.do(t, env.client, "GET", "/api/settings", "")
	tunnelCfg := settings["settings"].(map[string]any)["tunnel"].(map[string]any)
	if tunnelCfg["enabled"] != true || tunnelCfg["mode"] != "quick" {
		t.Fatalf("tunnel settings were not stored: %#v", tunnelCfg)
	}
	env.do(t, env.client, "POST", "/api/tunnel/stop", `{}`)
}

func TestStateExposesCloudflaredDuringSetup(t *testing.T) {
	env := newEnv(t)
	_, state := env.do(t, env.client, "GET", "/api/state", "")
	if state["cloudflared"] == nil {
		t.Fatal("the setup wizard needs to know whether cloudflared is installed")
	}
}

func TestClientIPPrefersCloudflareHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("CF-Connecting-IP", "198.51.100.7")
	if got := clientIP(req); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q", got)
	}
	req.Header.Del("CF-Connecting-IP")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q", got)
	}
}
