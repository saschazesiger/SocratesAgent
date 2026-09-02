package server

import (
	"encoding/json"
	"flag"
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
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// TestMain dispatches the subcommands the terminal substrate calls this
// binary as, and cleans up what the whole package shares.
func TestMain(m *testing.M) {
	// The test binary also stands in for the Socrates executable, because the
	// Manager points tmux at os.Executable() for the journal sink and for the
	// hook a dying pane runs. Without this dispatch tmux would run the whole
	// test suite again, once per pane.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "journal-sink":
			os.Exit(runJournalSink(os.Args[2:]))
		case "tmux-hook":
			os.Exit(runTmuxHook(os.Args[2:]))
		}
	}
	code := m.Run()
	// The fake CLI directory is shared by every test through a sync.Once, so
	// no single test owns it and t.TempDir cannot remove it. Here is the one
	// point at which the last test that needed it has finished.
	removeFakeBin()
	os.Exit(code)
}

// runJournalSink and runTmuxHook are the two subcommands the substrate calls
// this binary as.
func runJournalSink(args []string) int {
	fs := flag.NewFlagSet("journal-sink", flag.ContinueOnError)
	path := fs.String("path", "", "")
	maxBytes := fs.Int64("max-bytes", termux.JournalMaxBytes, "")
	keep := fs.Int("keep", termux.JournalKeep, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := termux.RunJournalSink(*path, *maxBytes, *keep); err != nil {
		return 1
	}
	return 0
}

func runTmuxHook(args []string) int {
	fs := flag.NewFlagSet("tmux-hook", flag.ContinueOnError)
	sock := fs.String("sock", "", "")
	event := fs.String("event", "", "")
	session := fs.String("session", "", "")
	status := fs.String("status", "", "")
	signal := fs.String("signal", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := termux.SendHook(*sock, termux.Hook{
		Event: *event, Session: *session, Status: *status, Signal: *signal,
	}); err != nil {
		return 1
	}
	return 0
}

type testEnv struct {
	server *httptest.Server
	client *http.Client
	anon   *http.Client
	store  *store.Store
	srv    *Server
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	// One root for the whole environment, taken first so that its removal is
	// the last cleanup to run. Everything the server keeps on disk lives under
	// it, and the cleanup registered below - which stops the server, the
	// tunnel and the database - runs before it: a directory removed while a
	// goroutine of the server is still writing into it comes straight back,
	// and that is how a test leaves a tree behind.
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "db", "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv, err := New(st, filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.StopTunnel()
		st.Close()
	})

	jar, _ := cookiejar.New(nil)
	noRedirect := func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	return &testEnv{
		server: ts,
		srv:    srv,
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

// After the rewrite the app serves login, setup, the admin settings and
// health, and nothing else. This is the whole surface until WP4 brings the
// session API.
func TestSetupAndTheAuthWall(t *testing.T) {
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

	// Anonymous access is refused, and the health check is the one endpoint
	// that answers without a password because a container asks it.
	res, _ = env.do(t, env.anon, "GET", "/api/settings", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous API access should be 401, got %d", res.StatusCode)
	}
	res, health := env.do(t, env.anon, "GET", "/api/health", "")
	if res.StatusCode != http.StatusOK || health["ok"] != true {
		t.Fatalf("health = %d %#v", res.StatusCode, health)
	}
}

// The chat API went with the chat. Nothing in the tree may answer on those
// paths any more: a route that survived a deletion is how a browser ends up
// talking to half a product.
func TestTheChatRoutesAreGone(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	for _, path := range []string{
		"/api/chats",
		"/api/chats/anything",
		"/api/chats/anything/events",
		"/api/agents",
		"/api/models",
		"/api/terminals/anything",
	} {
		res, _ := env.do(t, env.client, "GET", path, "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestLoginLogout(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	env.do(t, env.client, "POST", "/api/logout", "")

	res, _ := env.do(t, env.client, "GET", "/api/settings", "")
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
	res, _ = env.do(t, env.client, "GET", "/api/settings", "")
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
	settings["openrouter"].(map[string]any)["title_model"] = "openai/gpt-5"
	body, _ := json.Marshal(map[string]any{"settings": settings})
	res, saved := env.do(t, env.client, "PUT", "/api/settings", string(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	updated := saved["settings"].(map[string]any)["openrouter"].(map[string]any)
	if updated["title_model"] != "openai/gpt-5" {
		t.Fatalf("model was not stored: %#v", updated)
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

// StripANSI came across from the deleted terminal package with these two
// cases: a coding agent that prints its version with a colour in it is the
// only reason it is still here.
func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"colour", "\x1b[1;32mgreen\x1b[0m", "green"},
		{"cursor move", "a\x1b[3;5Hb", "ab"},
		{"clear screen", "\x1b[2J\x1b[Hclean", "clean"},
		{"window title", "\x1b]0;a title\x07text", "text"},
		{"osc string terminator", "\x1b]8;;https://example.com\x1b\\link", "link"},
		{"private mode", "\x1b[?1049htext", "text"},
		{"charset", "\x1b(Btext", "text"},
		{"bel and nul", "a\x07b\x00c", "abc"},
		{"plain", "nothing to strip", "nothing to strip"},
	}
	for _, c := range cases {
		if got := StripANSI(c.in); got != c.want {
			t.Errorf("%s: StripANSI(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Saving settings starts from the document that is live, not from a zero
// value. A body that leaves a section out - an older dashboard, a curl call, a
// field the page has never heard of - keeps what it has; decoded into a zero
// value it would arrive with every switch off, Normalize does not put switches
// back, and Shell would silently vanish from the picker while Codex came up
// blocked on its trust prompt.
func TestSettingsPutKeepsTheSectionsItWasNotSent(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	body := `{"settings":{"openrouter":{"title_model":"openai/gpt-5"}}}`
	res, saved := env.do(t, env.client, "PUT", "/api/settings", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	settings := saved["settings"].(map[string]any)
	if settings["openrouter"].(map[string]any)["title_model"] != "openai/gpt-5" {
		t.Fatalf("the field that was sent was not stored: %#v", settings["openrouter"])
	}
	harnesses := settings["harnesses"].(map[string]any)
	for _, check := range []struct {
		harness string
		key     string
	}{
		{"shell", "enabled"},
		{"shell", "login"},
		{"claude", "enabled"},
		{"claude", "pin_light_theme"},
		{"codex", "trust_workdir"},
		{"codex", "no_alt_screen"},
		{"opencode", "disable_models_fetch"},
	} {
		if harnesses[check.harness].(map[string]any)[check.key] != true {
			t.Errorf("%s.%s was turned off by a request that never mentioned it", check.harness, check.key)
		}
	}
	if settings["terminal"].(map[string]any)["webgl"] != true {
		t.Error("the terminal settings were cleared")
	}
	if settings["workspace"].(map[string]any)["allow_custom"] != true {
		t.Error("the workspace settings were cleared")
	}

	// And a switch that is sent as off stays off: keeping what was not sent is
	// not the same as ignoring what was.
	body = `{"settings":{"harnesses":{"shell":{"enabled":false,"login":true}}}}`
	_, saved = env.do(t, env.client, "PUT", "/api/settings", body)
	shell := saved["settings"].(map[string]any)["harnesses"].(map[string]any)["shell"].(map[string]any)
	if shell["enabled"] != false {
		t.Errorf("a switch that was turned off came back on: %#v", shell)
	}
}

// A JSON textarea with a typo in it is a save-time error, not a launch-time
// surprise, and the document that is live is left alone.
func TestSettingsPutRefusesInvalidJSONFields(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	body := `{"settings":{"harnesses":{"opencode":{"permission_json":"{not json"}}}}`
	res, data := env.do(t, env.client, "PUT", "/api/settings", body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid JSON was accepted: %d", res.StatusCode)
	}
	if data["error"] == nil {
		t.Error("the refusal does not say what is wrong")
	}
	_, current := env.do(t, env.client, "GET", "/api/settings", "")
	opencode := current["settings"].(map[string]any)["harnesses"].(map[string]any)["opencode"].(map[string]any)
	if opencode["permission_json"] != "" {
		t.Errorf("the refused value was stored anyway: %#v", opencode["permission_json"])
	}
}
