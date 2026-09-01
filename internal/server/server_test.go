package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
	"github.com/saschazesiger/SocratesAgent/internal/store"

	// The adapters register themselves in init, exactly as they do for the
	// real binary through main.go. Without them this package's tests would be
	// talking to an empty registry.
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/claude"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/codex"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/opencode"
)

// TestMain puts a stand-in piper where every server built in this package will
// find one, for the whole test binary rather than per test.
//
// It has to be process-wide. server.New starts the voice install on a
// goroutine of its own - that is what stops a first answer from waiting on a
// 150 MB download - and that goroutine outlives the test that started it. With
// the environment set per test and restored by its cleanup, a goroutine
// scheduled a moment late finds no installation, decides one is needed, and
// downloads 145 MB into a data directory the test has already removed. A few
// hundred of those fill a tmpfs and every build on the machine starts failing
// for want of space.
func TestMain(m *testing.M) {
	// A chat that sends a message re-executes this binary as an agent host,
	// because that is what Manager.Open does with os.Executable(). Without
	// this branch the child runs the whole test suite again - every host spawn
	// forking a fresh copy of every test in this package, each with its own
	// temp directories and its own voice install. That is how a few hundred
	// megabytes of /tmp per run became thirteen gigabytes.
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		dir := ""
		for i := 2; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--dir" || os.Args[i] == "-dir" {
				dir = os.Args[i+1]
			}
		}
		if err := agenthost.RunHost(dir); err != nil {
			fmt.Fprintln(os.Stderr, "host:", err)
			os.Exit(1)
		}
		return
	}
	root, err := os.MkdirTemp("", "socrates-piper")
	if err != nil {
		fmt.Fprintln(os.Stderr, "test setup:", err)
		os.Exit(1)
	}
	if err := bakePiper(root); err != nil {
		fmt.Fprintln(os.Stderr, "test setup:", err)
		os.RemoveAll(root)
		os.Exit(1)
	}
	os.Setenv(piper.EnvDir, root)
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

// bakePiper writes the smallest tree the voice engine accepts as installed: a
// binary that exits at once, the espeak data it names on every command line,
// and both voices at a size that is not mistaken for a truncated download.
func bakePiper(root string) error {
	binary := filepath.Join(root, "piper", "piper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := os.MkdirAll(filepath.Join(root, "piper", "espeak-ng-data"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		return err
	}
	voices := filepath.Join(root, "voices")
	if err := os.MkdirAll(voices, 0o755); err != nil {
		return err
	}
	for _, voice := range []string{piper.VoiceEnglish, piper.VoiceGerman} {
		model := filepath.Join(voices, voice+".onnx")
		if err := os.WriteFile(model, nil, 0o644); err != nil {
			return err
		}
		if err := os.Truncate(model, 2<<20); err != nil {
			return err
		}
		config := `{"audio":{"sample_rate":22050},"espeak":{"voice":"` + voice + `"},"padding":"` +
			strings.Repeat("x", 200) + `"}`
		if err := os.WriteFile(model+".json", []byte(config), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type testEnv struct {
	server *httptest.Server
	client *http.Client
	anon   *http.Client
	store  *store.Store
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	installedVoice(t)
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
		srv.DetachAgents()
		st.Close()
	})

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

	_, created := env.do(t, env.client, "POST", "/api/chats", `{"title":"Test chat","agent":"claude"}`)
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

// A phone that lost signal between sending and hearing back cannot tell a lost
// request from a lost reply, so it sends again. The key it carries has to make
// that a no-op rather than a second message in the conversation.
func TestRepeatedSendWithTheSameKeyIsOneMessage(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"correct horse"}`)

	_, created := env.do(t, env.client, "POST", "/api/chats", `{"client_id":"chat-key","agent":"claude"}`)
	chat := created["chat"].(map[string]any)
	id := chat["id"].(string)

	// Creating the chat again with the same key gives back the same chat.
	_, again := env.do(t, env.client, "POST", "/api/chats", `{"client_id":"chat-key","agent":"claude"}`)
	if again["chat"].(map[string]any)["id"] != id {
		t.Fatalf("a repeated create made a second chat: %v", again)
	}
	if chats, err := env.store.ListChats(true); err != nil || len(chats) != 1 {
		t.Fatalf("expected exactly one chat, got %#v (%v)", chats, err)
	}

	body := `{"text":"do the thing","client_id":"msg-key"}`
	res, first := env.do(t, env.client, "POST", "/api/chats/"+id+"/messages", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first send = %d %v", res.StatusCode, first)
	}
	runID := first["run"].(map[string]any)["id"]

	// The retry lands while the first run is still going. Without the key this
	// would be a 409; with it, it is the same answer as before.
	res, second := env.do(t, env.client, "POST", "/api/chats/"+id+"/messages", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the retry should be accepted, got %d %v", res.StatusCode, second)
	}
	if second["run"].(map[string]any)["id"] != runID {
		t.Fatalf("the retry started a different run: %v then %v", runID, second["run"])
	}

	messages, err := env.store.ListMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range messages {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("the same message was stored %d times", users)
	}
}

// The event stream is what keeps the browser honest. A reconnect names the last
// revision it saw and must be told everything that changed since, plus which
// steps still exist so deleted ones do not linger on screen.
func TestEventStreamReplaysFromARevision(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"correct horse"}`)
	_, created := env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude"}`)
	id := created["chat"].(map[string]any)["id"].(string)

	// Two steps and a message written before the client connects; one of the
	// steps is then removed, the way a streamed answer is when it becomes a
	// real chat bubble.
	for _, stepID := range []string{"s-old", "s-gone"} {
		if err := env.store.PutStep(&store.Step{
			ID: stepID, ChatID: id, RunID: "r1", Kind: "text", Body: stepID, Status: "done",
		}); err != nil {
			t.Fatal(err)
		}
	}
	mark := env.store.Rev()
	if err := env.store.PutStep(&store.Step{
		ID: "s-new", ChatID: id, RunID: "r1", Kind: "text", Body: "after the outage", Status: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.AddMessage(&store.Message{
		ID: "m-new", ChatID: id, RunID: "r1", Role: "assistant", Content: "landed while away",
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.DeleteStep("s-gone"); err != nil {
		t.Fatal(err)
	}

	events := env.readEvents(t, "/api/chats/"+id+"/events?rev="+strconv.FormatInt(mark, 10))

	var ready map[string]any
	steps := map[string]bool{}
	for _, ev := range events {
		switch ev["type"] {
		case "step":
			steps[ev["step"].(map[string]any)["id"].(string)] = true
		case "ready":
			ready = ev
		}
	}
	if ready == nil {
		t.Fatalf("the stream never said it was ready: %#v", events)
	}
	if !steps["s-new"] {
		t.Errorf("the step written during the outage was not replayed")
	}
	if steps["s-old"] {
		t.Errorf("a step the client already had was replayed again")
	}

	messages, _ := ready["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["id"] != "m-new" {
		t.Errorf("expected only the missed message, got %#v", messages)
	}

	ids, _ := ready["step_ids"].([]any)
	live := map[string]bool{}
	for _, v := range ids {
		live[v.(string)] = true
	}
	if !live["s-old"] || !live["s-new"] {
		t.Errorf("step_ids should list what still exists, got %#v", ids)
	}
	if live["s-gone"] {
		t.Errorf("step_ids still lists a step that was deleted")
	}
}

// A first connection has just loaded the transcript over the JSON API, so it is
// not told about revisions it never had - but it does get the tail of the
// conversation, because a message published a moment before the subscription
// existed would otherwise be lost until a reload.
func TestFirstConnectionGetsTheRecentMessages(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"correct horse"}`)
	_, created := env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude"}`)
	id := created["chat"].(map[string]any)["id"].(string)
	if err := env.store.AddMessage(&store.Message{
		ID: "m1", ChatID: id, RunID: "r1", Role: "user", Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	for _, ev := range env.readEvents(t, "/api/chats/"+id+"/events") {
		if ev["type"] != "ready" {
			continue
		}
		messages, _ := ev["messages"].([]any)
		if len(messages) != 1 || messages[0].(map[string]any)["id"] != "m1" {
			t.Fatalf("a fresh stream should carry the recent messages, got %#v", messages)
		}
		if _, ok := ev["step_ids"]; ok {
			t.Errorf("a fresh stream does not need the reconciliation set")
		}
		return
	}
	t.Fatal("the stream never said it was ready")
}

// readEvents opens an SSE stream and collects what it sends up to and including
// the ready event, then hangs up.
func (e *testEnv) readEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	req, err := http.NewRequest("GET", e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", res.StatusCode)
	}

	out := []map[string]any{}
	scanner := bufio.NewScanner(res.Body)
	deadline := time.Now().Add(10 * time.Second)
	for scanner.Scan() {
		if time.Now().After(deadline) {
			t.Fatal("the stream never became ready")
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			continue
		}
		out = append(out, event)
		if event["type"] == "ready" {
			return out
		}
	}
	return out
}

// An archived chat keeps its transcript but drops out of the sidebar, and
// talking to it again is what brings it back.
func TestArchiveAndRestoreChat(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	_, created := env.do(t, env.client, "POST", "/api/chats", `{"title":"Old work","agent":"claude"}`)
	id := created["chat"].(map[string]any)["id"].(string)

	res, archived := env.do(t, env.client, "POST", "/api/chats/"+id+"/archive", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d %v", res.StatusCode, archived)
	}
	if archived["chat"].(map[string]any)["archived"] != true {
		t.Fatalf("archive did not report the new state: %v", archived)
	}

	_, list := env.do(t, env.client, "GET", "/api/chats", "")
	if chats, _ := list["chats"].([]any); len(chats) != 0 {
		t.Fatalf("an archived chat is still in the default list: %v", list)
	}
	_, all := env.do(t, env.client, "GET", "/api/chats?scope=all", "")
	if chats, _ := all["chats"].([]any); len(chats) != 1 {
		t.Fatalf("scope=all did not return the archive: %v", all)
	}
	_, snapshot := env.do(t, env.client, "GET", "/api/chats/"+id, "")
	if snapshot["chat"].(map[string]any)["archived"] != true {
		t.Fatalf("the chat itself does not say it is archived: %v", snapshot)
	}

	// Sending a message is enough to make it active again - no separate step.
	res, sent := env.do(t, env.client, "POST", "/api/chats/"+id+"/messages", `{"text":"carry on"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("send = %d %v", res.StatusCode, sent)
	}
	chat, err := env.store.GetChat(id)
	if err != nil || chat.Archived {
		t.Fatalf("chatting did not restore the chat: %#v (%v)", chat, err)
	}

	// And it can be put away and taken back by hand as well.
	env.do(t, env.client, "POST", "/api/chats/"+id+"/archive", "")
	res, restored := env.do(t, env.client, "POST", "/api/chats/"+id+"/unarchive", "")
	if res.StatusCode != http.StatusOK || restored["chat"].(map[string]any)["archived"] != false {
		t.Fatalf("unarchive = %d %v", res.StatusCode, restored)
	}
	res, _ = env.do(t, env.client, "POST", "/api/chats/nope/archive", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("archiving an unknown chat = %d", res.StatusCode)
	}
}

// noAgentsOnPATH keeps every test in this file hermetic: nothing that could be
// a real coding agent is found, so no discovery ever starts a process.
func noAgentsOnPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// The picker and the admin card are built from this, so every field has to be
// there even for an agent that is not installed - "missing" is an answer, not
// an absence.
func TestAgentsEndpoint(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	res, _ := env.do(t, env.anon, "GET", "/api/agents", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the agent list must require a login, got %d", res.StatusCode)
	}

	_, data := env.do(t, env.client, "GET", "/api/agents", "")
	agents, _ := data["agents"].([]any)
	if len(agents) != 3 {
		t.Fatalf("expected one entry per agent, got %#v", data)
	}
	byID := map[string]map[string]any{}
	for _, entry := range agents {
		a := entry.(map[string]any)
		byID[a["id"].(string)] = a
		for _, field := range []string{"label", "enabled", "installed", "path", "version",
			"has_effort", "default_model", "default_effort", "static", "models", "notes", "error"} {
			if _, ok := a[field]; !ok {
				t.Errorf("%s arrived without %s", a["id"], field)
			}
		}
	}
	claude := byID["claude"]
	if claude == nil {
		t.Fatal("no claude entry")
	}
	if claude["installed"] != false {
		t.Errorf("claude was found on an empty PATH: %#v", claude)
	}
	if claude["error"] == "" {
		t.Error("a missing agent did not say why")
	}
	if data["refreshed_at"] == nil {
		t.Error("the catalogue did not say when it was made")
	}

	// Refresh is the dashboard's button: same shape, discovered again.
	res, again := env.do(t, env.client, "POST", "/api/agents/refresh", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh failed: %d", res.StatusCode)
	}
	if list, _ := again["agents"].([]any); len(list) != 3 {
		t.Fatalf("refresh returned %#v", again)
	}
}

// A chat is bound at creation, and every refusal here is permanent - so it is
// 422 and never 409, which the browser would retry until the end of time.
func TestCreateChatValidatesItsBinding(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	res, body := env.do(t, env.client, "POST", "/api/chats", `{}`)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a chat without an agent = %d, want 422: %#v", res.StatusCode, body)
	}
	res, _ = env.do(t, env.client, "POST", "/api/chats", `{"agent":"invented"}`)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown agent = %d, want 422", res.StatusCode)
	}

	// An empty model falls back to the agent's default.
	_, created := env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude"}`)
	chat := created["chat"].(map[string]any)
	if chat["agent"] != "claude" || chat["model"] != "sonnet" {
		t.Fatalf("binding = %#v", chat)
	}

	// Claude's list is curated, not a whitelist: a model id nobody has heard
	// of yet is accepted, and Claude reports a bad one as a clean run error.
	_, typed := env.do(t, env.client, "POST", "/api/chats",
		`{"agent":"claude","model":"some-new-alias","effort":"high"}`)
	c := typed["chat"].(map[string]any)
	if c["model"] != "some-new-alias" || c["effort"] != "high" {
		t.Fatalf("a typed model was not accepted: %#v", c)
	}

	// A switched-off agent is refused by name.
	settings := env.settings(t)
	settings["agents"].(map[string]any)["claude"].(map[string]any)["enabled"] = false
	env.saveSettings(t, settings)
	res, body = env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude"}`)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a switched-off agent = %d, want 422", res.StatusCode)
	}
	if detail, _ := body["error"].(string); !strings.Contains(detail, "dashboard") {
		t.Errorf("the refusal should say where to turn it back on, got %q", detail)
	}
}

// The idempotency key answers before any validation runs: a chat that already
// exists is an answer, whatever the settings have since become.
func TestCreateChatIsIdempotentBeforeItValidates(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	_, first := env.do(t, env.client, "POST", "/api/chats", `{"client_id":"k","agent":"claude"}`)
	id := first["chat"].(map[string]any)["id"].(string)

	settings := env.settings(t)
	settings["agents"].(map[string]any)["claude"].(map[string]any)["enabled"] = false
	env.saveSettings(t, settings)

	res, again := env.do(t, env.client, "POST", "/api/chats", `{"client_id":"k"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a repeated create = %d", res.StatusCode)
	}
	if again["chat"].(map[string]any)["id"] != id {
		t.Fatalf("a repeated create made a second chat: %#v", again)
	}
}

// A chat that predates the rewrite is a read-only transcript: the API says so
// with 422, which the Outbox marks failed and offers a retry for, rather than
// with 409, which it would retry forever behind a message that is not true.
func TestALegacyChatIsReadOnly(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	if err := env.store.CreateChat(&store.Chat{ID: "chat_legacy", Title: "From before"}); err != nil {
		t.Fatal(err)
	}

	_, snapshot := env.do(t, env.client, "GET", "/api/chats/chat_legacy", "")
	if snapshot["legacy"] != true || snapshot["agent_ok"] != false {
		t.Fatalf("a legacy chat was not marked as one: %#v", snapshot)
	}

	res, body := env.do(t, env.client, "POST", "/api/chats/chat_legacy/messages", `{"text":"hello"}`)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("sending into a legacy chat = %d, want 422", res.StatusCode)
	}
	if detail, _ := body["error"].(string); !strings.Contains(detail, "start a new chat") {
		t.Errorf("the refusal should say what to do instead, got %q", detail)
	}
}

// The model may change between turns; the agent never. A different agent is a
// different conversation, and there is no CLI that saw the first half of this
// one.
func TestPatchChangesTheModelButNeverTheAgent(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, created := env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude"}`)
	id := created["chat"].(map[string]any)["id"].(string)

	res, updated := env.do(t, env.client, "PATCH", "/api/chats/"+id, `{"model":"opus","effort":"high"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("changing the model = %d: %#v", res.StatusCode, updated)
	}
	chat, _ := env.store.GetChat(id)
	if chat.Model != "opus" || chat.Effort != "high" {
		t.Fatalf("the change was not stored: %#v", chat)
	}

	res, _ = env.do(t, env.client, "PATCH", "/api/chats/"+id, `{"agent":"codex"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("changing the agent = %d, want 400", res.StatusCode)
	}
	chat, _ = env.store.GetChat(id)
	if chat.Agent != "claude" {
		t.Fatalf("the agent changed anyway: %q", chat.Agent)
	}
}

// The header shows what this chat is bound to, and whether that still works.
func TestChatSnapshotDescribesItsBinding(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, created := env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude","model":"sonnet"}`)
	id := created["chat"].(map[string]any)["id"].(string)

	_, snapshot := env.do(t, env.client, "GET", "/api/chats/"+id, "")
	if snapshot["agent_label"] != "Claude Code" {
		t.Errorf("agent_label = %#v", snapshot["agent_label"])
	}
	if snapshot["legacy"] != false {
		t.Errorf("a new chat was called legacy: %#v", snapshot["legacy"])
	}
	if _, ok := snapshot["model_label"]; !ok {
		t.Error("no model_label in the snapshot")
	}
	if _, ok := snapshot["agent_ok"]; !ok {
		t.Error("no agent_ok in the snapshot")
	}
}

// Every route the terminal used to own is gone, and gone means 404 rather than
// an endpoint that answers with nothing.
func TestTheTerminalRoutesAreGone(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	for _, path := range []string{
		"/api/terminals/anything",
		"/api/terminals/anything/events",
	} {
		res, _ := env.do(t, env.client, "GET", path, "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
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

// settings reads the current settings document as the dashboard does.
func (e *testEnv) settings(t *testing.T) map[string]any {
	t.Helper()
	_, data := e.do(t, e.client, "GET", "/api/settings", "")
	settings, _ := data["settings"].(map[string]any)
	if settings == nil {
		t.Fatalf("no settings in %#v", data)
	}
	return settings
}

func (e *testEnv) saveSettings(t *testing.T, settings map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"settings": settings})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := e.do(t, e.client, "PUT", "/api/settings", string(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("saving settings = %d", res.StatusCode)
	}
}

// A send that already landed is answered with the run it produced, whatever
// the settings have become since. Refusing the retry of a message that is
// already in the transcript would be a permanent error the person did nothing
// to earn - and the browser would show it as one.
func TestARetriedSendSurvivesTheAgentBeingSwitchedOff(t *testing.T) {
	noAgentsOnPATH(t)
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, created := env.do(t, env.client, "POST", "/api/chats", `{"agent":"claude"}`)
	id := created["chat"].(map[string]any)["id"].(string)

	res, first := env.do(t, env.client, "POST", "/api/chats/"+id+"/messages",
		`{"text":"hello","client_id":"send-key"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the first send = %d: %#v", res.StatusCode, first)
	}
	runID := first["run"].(map[string]any)["id"].(string)

	settings := env.settings(t)
	settings["agents"].(map[string]any)["claude"].(map[string]any)["enabled"] = false
	env.saveSettings(t, settings)

	// A fresh send is refused, permanently and by name.
	res, _ = env.do(t, env.client, "POST", "/api/chats/"+id+"/messages",
		`{"text":"another","client_id":"other-key"}`)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a send to a switched-off agent = %d, want 422", res.StatusCode)
	}
	// The retry of the one that landed is not.
	res, again := env.do(t, env.client, "POST", "/api/chats/"+id+"/messages",
		`{"text":"hello","client_id":"send-key"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the retried send = %d: %#v", res.StatusCode, again)
	}
	if again["run"].(map[string]any)["id"] != runID {
		t.Fatalf("the retry produced a second run: %#v", again)
	}
}
