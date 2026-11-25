package server

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/testhome"
	zdash "github.com/saschazesiger/Z-Dash/server"
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
		case "agent-host":
			os.Exit(agenthost.Main(os.Args[2:], "test"))
		case "mcp-ask":
			// An OpenCode chat's ask_user server, which its host runs as
			// this binary (agenthost.MCPAskMain).
			os.Exit(agenthost.MCPAskMain(os.Args[2:], "test"))
		case "git-credential":
			// A chat's git asking for its repository's token.
			os.Exit(agenthost.GitCredentialMain(os.Args[2:]))
		}
	}
	// The chats of these tests work in folders the tests made: presets and
	// typed folders are gone for everyone else (folderChatsAllowed).
	os.Setenv("SOCRATES_FOLDER_CHATS", "1")
	// The real adapters are registered here too (the accounts package
	// imports them), so Claude Code, Codex and OpenCode are chats, as in the
	// product; most chats here run the scripted agent, and a test that wants
	// an agent's own puts its protocol fake on the PATH (withFakeAgents).
	os.Exit(testhome.Main(m))
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
	return newEnvWith(t, false)
}

// newEnvWith is newEnv, with the database inside the data directory, where
// main keeps it, when inData is set: the server a backup of the data
// directory is taken from. Every other test keeps it beside the data
// directory, so that a restore into one never replaces an open database.
func newEnvWith(t *testing.T, inData bool) *testEnv {
	t.Helper()
	return newEnvSeeded(t, inData, nil)
}

// newEnvSeeded is newEnvWith on a store that seed has written first: what a
// start finds from the run before it (a tunnel's state, the logins and the
// password of an older build).
func newEnvSeeded(t *testing.T, inData bool, seed func(st *store.Store, dataDir string)) *testEnv {
	t.Helper()
	// One root for the whole environment, taken first so that its removal is
	// the last cleanup to run. Everything the server keeps on disk lives under
	// it, and the cleanup registered below - which stops the server, the
	// tunnel and the database - runs before it: a directory removed while a
	// goroutine of the server is still writing into it comes straight back,
	// and that is how a test leaves a tree behind.
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "test.db")
	if inData {
		dbPath = filepath.Join(root, "data", "socrates.db")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if seed != nil {
		seed(st, filepath.Join(root, "data"))
	}
	srv, err := New(st, filepath.Join(root, "data"), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		// A question still waiting for its words would be pushed from a
		// closed store when its timer fired.
		srv.push.stop()
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
	// What api.js sends with everything that changes something.
	if method != http.MethodGet {
		req.Header.Set(zdash.CSRFHeader, "1")
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

	res, _ = env.do(t, env.client, "POST", "/api/setup", `{"password":"another-good-password"}`)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("setup must only run once, got %d", res.StatusCode)
	}

	// Anonymous access is refused, and the health check is the one endpoint
	// that answers without a password because scripts/update.sh asks it.
	res, _ = env.do(t, env.anon, "GET", "/api/settings", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous API access should be 401, got %d", res.StatusCode)
	}
	res, health := env.do(t, env.anon, "GET", "/api/health", "")
	if res.StatusCode != http.StatusOK || health["ok"] != true {
		t.Fatalf("health = %d %#v", res.StatusCode, health)
	}
}

// The chat API of the terminal rewrite went with its chat, and the operator's
// routes went with the operator. Nothing in the tree may answer on those paths
// any more: a route that survived a deletion is how a browser ends up talking
// to half a product. The rebuild's chats (chats.go) answer under /api/chats/{id}
// with their own routes, and a chat that does not exist is a 404 there too.
func TestTheChatRoutesAreGone(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	for _, path := range []string{
		"/api/chats/anything",
		"/api/chats/anything/events",
		"/api/agents",
		"/api/models",
		"/api/terminals/anything",
		"/api/sessions/anything/agent",
		"/api/sessions/anything/chat",
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
	if res, out := env.do(t, env.client, "POST", "/api/logout", ""); res.StatusCode != http.StatusOK || out["next"] != "" {
		t.Fatalf("logout on the local address = %d %#v", res.StatusCode, out)
	}

	res, _ := env.do(t, env.client, "GET", "/api/settings", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout did not end the session: %d", res.StatusCode)
	}
	// A wrong password is an error under its field, never a 401: the web
	// client takes a 401 for a lost session.
	res, out := env.do(t, env.client, "POST", "/api/login", `{"password":"wrong"}`)
	fields, _ := out["fields"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || fields["password"] == nil {
		t.Fatalf("a wrong password = %d %#v", res.StatusCode, out)
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

// A request that changes something needs the header a form on another site
// cannot send, and an Origin that is this host.
func TestTheCSRFGuard(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	send := func(header, origin string) int {
		req, _ := http.NewRequest("PUT", env.server.URL+"/api/boards", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set(zdash.CSRFHeader, header)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := env.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := send("", ""); got != http.StatusForbidden {
		t.Errorf("without the header = %d, want 403", got)
	}
	if got := send("1", "https://evil.example"); got != http.StatusForbidden {
		t.Errorf("from another origin = %d, want 403", got)
	}
	if got := send("1", env.server.URL); got == http.StatusForbidden || got == http.StatusUnauthorized {
		t.Errorf("from the page itself = %d", got)
	}
	// The setup page's requests are guarded too, before there is a password.
	fresh := newEnv(t)
	req, _ := http.NewRequest("POST", fresh.server.URL+"/api/setup", strings.NewReader(`{"password":"a-good-password"}`))
	res, err := fresh.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || fresh.srv.hasPassword() {
		t.Errorf("a setup without the header = %d, password set: %v", res.StatusCode, fresh.srv.hasPassword())
	}
}

// A wrong current password is refused without looking like a lost session:
// the client sends any 401 to the sign-in, and the message would never show.
func TestChangePasswordWrongCurrent(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	res, payload := env.do(t, env.client, "POST", "/api/password",
		`{"current":"not-the-password","password":"another-good-password"}`)
	fields, _ := payload["fields"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || fields["current"] != "The current password is wrong." {
		t.Fatalf("a wrong current password answered %d %#v", res.StatusCode, payload)
	}
	res, _ = env.do(t, env.client, "GET", "/api/settings", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the refusal ended the session: %d", res.StatusCode)
	}
	res, _ = env.do(t, env.client, "POST", "/api/login", `{"password":"a-good-password"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the old password no longer works: %d", res.StatusCode)
	}
}

// The setup page's first step takes the OpenRouter key with the password,
// and signs the browser in.
func TestSetupKeepsTheOpenRouterKey(t *testing.T) {
	env := newEnv(t)
	res, _ := env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password","openrouter_key":" sk-or-test "}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup = %d", res.StatusCode)
	}
	if got := env.srv.Settings().OpenRouter.APIKey; got != "sk-or-test" {
		t.Fatalf("the key = %q", got)
	}
	if _, state := env.do(t, env.client, "GET", "/api/state", ""); state["authenticated"] != true || state["setup_required"] != false {
		t.Fatalf("state after the setup = %#v", state)
	}
	// The password is the Z-Dash server's, in its format; nothing is written
	// in the old one.
	if v, err := env.store.GetKV("auth.password"); err != nil || !strings.Contains(v, `"salt"`) {
		t.Fatalf("auth.password = %q, %v", v, err)
	}
	if _, err := env.store.GetKV(PasswordKey); err == nil {
		t.Fatal("a new password was written in the old format")
	}
}

// legacyHash is a password as the builds before the Z-Dash server stored it.
func legacyHash(t *testing.T, password string) string {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key, err := pbkdf2.Key(sha256.New, password, salt, 1000, 32)
	if err != nil {
		t.Fatal(err)
	}
	return "pbkdf2-sha256$1000$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
}

// A data directory of a build before the Z-Dash server: its password still
// signs in, however short, and the first sign-in moves it to the server's
// format; until then nobody can set a first password over it.
func TestTheOldPasswordSignsInAndMoves(t *testing.T) {
	env := newEnvSeeded(t, false, func(st *store.Store, _ string) {
		if err := st.SetKV(PasswordKey, legacyHash(t, "secret")); err != nil {
			t.Fatal(err)
		}
	})
	if _, state := env.do(t, env.client, "GET", "/api/state", ""); state["setup_required"] != false {
		t.Fatalf("an old password is a password: %#v", state)
	}
	if res, _ := env.do(t, env.client, "GET", "/", ""); res.Header.Get("Location") != "/login" {
		t.Fatalf("root = %d %s, want the sign-in", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := env.do(t, env.client, "POST", "/api/setup", `{"password":"another-good-password"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a setup over the old password = %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/setup/password", `{"password":"another-good-password"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a first password over the old one = %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/login", `{"password":"wrong!"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a wrong password = %d", res.StatusCode)
	}
	if _, err := env.store.GetKV(PasswordKey); err != nil {
		t.Fatalf("a wrong password dropped the old one: %v", err)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/login", `{"password":"secret"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("the old password = %d", res.StatusCode)
	}
	if res, _ := env.do(t, env.client, "GET", "/api/settings", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("signed in with the old password, settings = %d", res.StatusCode)
	}
	if _, err := env.store.GetKV(PasswordKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the old password is still stored: %v", err)
	}
	if _, err := env.store.GetKV("auth.password"); err != nil {
		t.Fatalf("the password was not moved: %v", err)
	}
	// And it keeps signing in from its new place.
	env.do(t, env.client, "POST", "/api/logout", "")
	if res, _ := env.do(t, env.client, "POST", "/api/login", `{"password":"secret"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("the moved password = %d", res.StatusCode)
	}
}

// The logins of a build before the Z-Dash server are its sessions after the
// update: the browser's cookie still signs in, an expired one does not, and
// signing out ends it.
func TestTheOldLoginsCarryOver(t *testing.T) {
	env := newEnvSeeded(t, false, func(st *store.Store, _ string) {
		if err := st.SetKV(PasswordKey, legacyHash(t, "secret")); err != nil {
			t.Fatal(err)
		}
		if err := st.AddLogin("live-token", time.Now().Add(time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if err := st.AddLogin("stale-token", time.Now().Add(-time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	})
	get := func(token string) int {
		req, _ := http.NewRequest("GET", env.server.URL+"/api/settings", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		res, err := env.anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := get("live-token"); got != http.StatusOK {
		t.Fatalf("the old login = %d, want 200", got)
	}
	if got := get("stale-token"); got != http.StatusUnauthorized {
		t.Fatalf("an expired login = %d, want 401", got)
	}
	if got := get("made-up"); got != http.StatusUnauthorized {
		t.Fatalf("an unknown cookie = %d, want 401", got)
	}
	// The store holds the hash of the token, not the token.
	raw, err := env.store.GetKV(sessionsKey)
	sum := sha256.Sum256([]byte("live-token"))
	if err != nil || !strings.Contains(raw, hex.EncodeToString(sum[:])) || strings.Contains(raw, "live-token") {
		t.Fatalf("auth.sessions = %s, %v", raw, err)
	}
	// Signing out ends the carried-over session like any other.
	req, _ := http.NewRequest("POST", env.server.URL+"/api/logout", nil)
	req.Header.Set(zdash.CSRFHeader, "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "live-token"})
	res, err := env.anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := get("live-token"); got != http.StatusUnauthorized {
		t.Fatalf("after signing out = %d, want 401", got)
	}
}

// The import happens once: sessions the server already keeps are never
// replaced by the old table.
func TestTheOldLoginsAreImportedOnce(t *testing.T) {
	env := newEnvSeeded(t, false, func(st *store.Store, _ string) {
		if err := st.SetKV(sessionsKey, `{}`); err != nil {
			t.Fatal(err)
		}
		if err := st.AddLogin("signed-out-since", time.Now().Add(time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	})
	if raw, err := env.store.GetKV(sessionsKey); err != nil || raw != `{}` {
		t.Fatalf("auth.sessions = %s, %v", raw, err)
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
	hash := legacyHash(t, "correct horse")
	if !verifyPassword(hash, "correct horse") {
		t.Error("valid password rejected")
	}
	if verifyPassword(hash, "wrong horse") {
		t.Error("wrong password accepted")
	}
	if verifyPassword("garbage", "correct horse") {
		t.Error("malformed hash accepted")
	}
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
		{"claude", "enabled"},
		{"codex", "enabled"},
		{"opencode", "enabled"},
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
	body = `{"settings":{"harnesses":{"shell":{"enabled":false}}}}`
	_, saved = env.do(t, env.client, "PUT", "/api/settings", body)
	shell := saved["settings"].(map[string]any)["harnesses"].(map[string]any)["shell"].(map[string]any)
	if shell["enabled"] != false {
		t.Errorf("a switch that was turned off came back on: %#v", shell)
	}
}

// The harness section has no free-text JSON left in it, so a save can no
// longer be refused for a typo in one - and a stored document that still
// carries the old textareas is read past rather than refused.
func TestSettingsPutIgnoresTheOldTextareas(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	body := `{"settings":{"harnesses":{"opencode":{"permission_json":"{not json",` +
		`"config_content":"[","default_model":"anthropic/claude-sonnet-4-5"}}}}`
	res, data := env.do(t, env.client, "PUT", "/api/settings", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an old document was refused: %d %#v", res.StatusCode, data)
	}
	_, current := env.do(t, env.client, "GET", "/api/settings", "")
	opencode := current["settings"].(map[string]any)["harnesses"].(map[string]any)["opencode"].(map[string]any)
	if opencode["default_model"] != "anthropic/claude-sonnet-4-5" {
		t.Errorf("the setting that is still a setting was lost: %#v", opencode)
	}
	if _, found := opencode["permission_json"]; found {
		t.Errorf("a dead key came back out of the stored document: %#v", opencode)
	}
}
