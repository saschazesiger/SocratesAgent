package server

import (
	"bufio"
	"encoding/json"
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

	// The shipped presets come along with the settings, because the dashboard
	// offers them to an installation that already has its own skills.
	presets, _ := data["presets"].([]any)
	if len(presets) != 3 {
		t.Fatalf("expected the three presets in the response, got %#v", data["presets"])
	}
	if presets[0].(map[string]any)["startup"] == "" {
		t.Fatal("a preset arrived without its manual")
	}

	// Two skills with the same id would make the orchestrator's choice
	// ambiguous, so saving has to separate them rather than fail.
	settings["skills"] = []any{
		map[string]any{"id": "same", "name": "A", "command": "claude", "enabled": true,
			"startup": "press enter", "interactive_only": true},
		map[string]any{"id": "same", "name": "B", "command": "codex", "enabled": true},
	}
	body, _ = json.Marshal(map[string]any{"settings": settings})
	res, saved = env.do(t, env.client, "PUT", "/api/settings", string(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save failed: %d", res.StatusCode)
	}
	skills, _ := saved["settings"].(map[string]any)["skills"].([]any)
	if len(skills) != 2 {
		t.Fatalf("expected two skills, got %#v", skills)
	}
	first := skills[0].(map[string]any)
	second := skills[1].(map[string]any)
	if first["id"] == second["id"] {
		t.Fatalf("duplicate skill ids survived the save: %v and %v", first["id"], second["id"])
	}
	if first["startup"] != "press enter" {
		t.Fatalf("the manual was not stored: %#v", first)
	}
	if second["interactive_only"] != true {
		t.Fatalf("a skill that says nothing has to come back interactive only: %#v", second)
	}

	// And it survives a reload, which is what the dashboard reads.
	_, again := env.do(t, env.client, "GET", "/api/settings", "")
	reloaded := again["settings"].(map[string]any)["skills"].([]any)
	if len(reloaded) != 2 || reloaded[0].(map[string]any)["startup"] != "press enter" {
		t.Fatalf("skills did not survive the reload: %#v", reloaded)
	}
}

func TestTerminalEndpointsNeedAuth(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	// A session that does not exist is a 404, not a crash.
	res, _ := env.do(t, env.client, "GET", "/api/terminals/term_missing", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session should be 404, got %d", res.StatusCode)
	}

	// And without a session cookie nothing is reachable at all.
	anonymous := &http.Client{}
	res, _ = env.do(t, anonymous, "GET", "/api/terminals/term_missing", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal endpoints must require a login, got %d", res.StatusCode)
	}
}

func TestListTerminalsIsEmptyForANewChat(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, chat := env.do(t, env.client, "POST", "/api/chats", `{}`)
	id := chat["chat"].(map[string]any)["id"].(string)

	_, data := env.do(t, env.client, "GET", "/api/chats/"+id+"/terminals", "")
	terminals, ok := data["terminals"].([]any)
	if !ok {
		t.Fatalf("no terminal list in %#v", data)
	}
	if len(terminals) != 0 {
		t.Fatalf("a new chat should have no sessions, got %#v", terminals)
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

	_, created := env.do(t, env.client, "POST", "/api/chats", `{"client_id":"chat-key"}`)
	chat := created["chat"].(map[string]any)
	id := chat["id"].(string)

	// Creating the chat again with the same key gives back the same chat.
	_, again := env.do(t, env.client, "POST", "/api/chats", `{"client_id":"chat-key"}`)
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
	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
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
	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
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

// The question tool is gone. Its endpoint must be gone with it, and the
// snapshot the browser opens on must not offer a question to answer - a
// leftover field would put the old panel back on screen.
func TestTheQuestionEndpointIsGone(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"correct horse"}`)
	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
	id := created["chat"].(map[string]any)["id"].(string)

	res, _ := env.do(t, env.client, "POST", "/api/questions/q1/answer", `{"value":"Left"}`)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("answering a question = %d, wanted 404", res.StatusCode)
	}

	_, chat := env.do(t, env.client, "GET", "/api/chats/"+id, "")
	if _, ok := chat["questions"]; ok {
		t.Errorf("the chat snapshot still carries questions: %#v", chat)
	}

	for _, ev := range env.readEvents(t, "/api/chats/"+id+"/events") {
		if ev["type"] == "question" {
			t.Fatalf("the stream still publishes questions: %#v", ev)
		}
		if ev["type"] != "ready" {
			continue
		}
		if _, ok := ev["pending_question"]; ok {
			t.Fatalf("the ready snapshot still carries a pending question: %#v", ev)
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

	_, created := env.do(t, env.client, "POST", "/api/chats", `{"title":"Old work"}`)
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
