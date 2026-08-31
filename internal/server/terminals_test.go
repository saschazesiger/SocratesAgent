package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// TestMain lets this test binary stand in for the Socrates binary, which the
// terminal manager re-executes to host a session.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "term-host" {
		dir := ""
		for i := 2; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--dir" || os.Args[i] == "-dir" {
				dir = os.Args[i+1]
			}
		}
		if err := term.RunHost(dir); err != nil {
			fmt.Fprintln(os.Stderr, "host:", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

// titleModel is answered separately from the script.
const titleModel = "test-title-model"

// scriptedModel answers the orchestration loop with a fixed script, so the
// whole path from an HTTP message to a live terminal can be exercised without
// a real model.
type scriptedModel struct {
	mu    sync.Mutex
	steps []string
	index int
	seen  int
	last  string
}

func (s *scriptedModel) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

func (s *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	// Naming a chat goes to the same endpoint on its own goroutine. It must
	// not eat a scripted answer, or the orchestration loop silently gets the
	// wrong one.
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.Model == titleModel {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"A title"},"finish_reason":"stop"}]}`)
		return
	}

	s.mu.Lock()
	s.seen++
	s.last = string(raw)
	chunk := `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`
	if s.index < len(s.steps) {
		chunk = s.steps[s.index]
		s.index++
	}
	s.mu.Unlock()
	// The session id only exists once the session does, so the script asks for
	// whatever the previous tool result mentioned.
	if id := findSession(string(raw)); id != "" {
		chunk = strings.ReplaceAll(chunk, "SESSION", id)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: "+chunk+"\n\ndata: [DONE]\n\n")
}

func findSession(payload string) string {
	i := strings.Index(payload, "term_")
	if i < 0 {
		return ""
	}
	rest := payload[i:]
	end := len(rest)
	for j, r := range rest {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			end = j
			break
		}
	}
	return rest[:end]
}

// toolCall builds one scripted tool call. The arguments are marshalled twice
// on purpose - once into the JSON string the provider sends, once into the
// event - which is exactly what a real provider does.
func toolCall(name string, args map[string]any) string {
	inner, _ := json.Marshal(args)
	encoded, _ := json.Marshal(string(inner))
	return `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function",` +
		`"function":{"name":"` + name + `","arguments":` + string(encoded) + `}}]},"finish_reason":"tool_calls"}]}`
}

// dumpSteps prints the process view, which is where a failure explains itself.
func dumpSteps(t *testing.T, env *testEnv, chatID string) {
	t.Helper()
	steps, err := env.store.ListSteps(chatID)
	if err != nil {
		t.Logf("could not read the steps: %v", err)
		return
	}
	for _, step := range steps {
		t.Logf("step %s/%s %q body=%q detail=%s", step.Kind, step.Status, step.Title,
			truncate(step.Body, 200), truncate(string(step.Detail), 300))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// TestTerminalIsLiveAndTakesInputFromTheUser walks the whole feature: a message
// makes Socrates open a session, the browser can list it, watch it, type into
// it and close it.
func TestTerminalIsLiveAndTakesInputFromTheUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	model := &scriptedModel{steps: []string{
		toolCall("terminal_open", map[string]any{
			"command": `sh -c 'printf "ready> "; while IFS= read -r l; do echo "you said: $l"; printf "ready> "; done'`,
			"name":    "a shell",
		}),
		`{"choices":[{"delta":{"content":"The session is open."},"finish_reason":"stop"}]}`,
	}}
	modelServer := httptest.NewServer(model)
	t.Cleanup(modelServer.Close)

	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	// Point the engine at the scripted model.
	_, data := env.do(t, env.client, "GET", "/api/settings", "")
	settings := data["settings"].(map[string]any)
	openrouter := settings["openrouter"].(map[string]any)
	openrouter["api_key"] = "test-key"
	openrouter["base_url"] = modelServer.URL
	openrouter["title_model"] = titleModel
	// Keep the chat's files inside the test's own directory.
	settings["agent"].(map[string]any)["workspace_root"] = t.TempDir()
	body, _ := json.Marshal(map[string]any{"settings": settings})
	if res, _ := env.do(t, env.client, "PUT", "/api/settings", string(body)); res.StatusCode != http.StatusOK {
		t.Fatalf("could not configure the model: %d", res.StatusCode)
	}

	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
	chatID := created["chat"].(map[string]any)["id"].(string)

	res, sent := env.do(t, env.client, "POST", "/api/chats/"+chatID+"/messages", `{"text":"open a shell"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sending the message failed: %d %#v", res.StatusCode, sent)
	}

	// Wait for the session to show up in the chat's list.
	var session map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
		terminals, _ := listed["terminals"].([]any)
		if len(terminals) > 0 {
			session = terminals[0].(map[string]any)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if session == nil {
		dumpSteps(t, env, chatID)
		_, snapshot := env.do(t, env.client, "GET", "/api/chats/"+chatID, "")
		t.Logf("chat snapshot: %#v", snapshot)
		t.Logf("model was called %d time(s)", model.calls())
		model.mu.Lock()
		t.Logf("last payload: %s", model.last)
		model.mu.Unlock()
		t.Fatal("no terminal session was opened for the chat")
	}
	id := session["id"].(string)
	if session["name"] != "a shell" {
		t.Errorf("session name = %v, want \"a shell\"", session["name"])
	}
	if session["running"] != true {
		t.Errorf("the session should be running: %#v", session)
	}
	if !strings.Contains(session["screen"].(string), "ready>") {
		t.Errorf("the screen was not captured: %q", session["screen"])
	}

	// The user takes the keyboard.
	res, _ = env.do(t, env.client, "POST", "/api/terminals/"+id+"/input",
		`{"text":"typed by the user","submit":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("typing into the session failed: %d", res.StatusCode)
	}
	deadline = time.Now().Add(15 * time.Second)
	seen := ""
	for time.Now().Before(deadline) {
		_, got := env.do(t, env.client, "GET", "/api/terminals/"+id, "")
		seen = got["terminal"].(map[string]any)["screen"].(string)
		if strings.Contains(seen, "you said: typed by the user") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(seen, "you said: typed by the user") {
		t.Errorf("what the user typed never reached the program:\n%s", seen)
	}

	// Named keys work too, and a nonsense one is rejected rather than sent.
	res, _ = env.do(t, env.client, "POST", "/api/terminals/"+id+"/input", `{"keys":["ctrl+l"]}`)
	if res.StatusCode != http.StatusOK {
		t.Errorf("a valid key was rejected: %d", res.StatusCode)
	}
	res, _ = env.do(t, env.client, "POST", "/api/terminals/"+id+"/input", `{"keys":["banana"]}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown key should be rejected, got %d", res.StatusCode)
	}

	// Resizing is what a browser does when the pane changes size.
	res, _ = env.do(t, env.client, "POST", "/api/terminals/"+id+"/resize", `{"cols":100,"rows":30}`)
	if res.StatusCode != http.StatusOK {
		t.Errorf("resize failed: %d", res.StatusCode)
	}

	// And the session can be ended from the browser.
	res, _ = env.do(t, env.client, "POST", "/api/terminals/"+id+"/close", `{}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("closing failed: %d", res.StatusCode)
	}
	_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
	if terminals, _ := listed["terminals"].([]any); len(terminals) != 0 {
		t.Errorf("the closed session is still listed: %#v", terminals)
	}
}

// Deleting a chat has to take its sessions with it, since nothing else ever
// will - they deliberately outlive the run that started them.
func TestDeletingAChatEndsItsTerminals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	model := &scriptedModel{steps: []string{
		toolCall("terminal_open", map[string]any{"command": `sh -c 'printf "ready> "; sleep 120'`}),
		`{"choices":[{"delta":{"content":"open"},"finish_reason":"stop"}]}`,
	}}
	modelServer := httptest.NewServer(model)
	t.Cleanup(modelServer.Close)

	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	_, data := env.do(t, env.client, "GET", "/api/settings", "")
	settings := data["settings"].(map[string]any)
	openrouter := settings["openrouter"].(map[string]any)
	openrouter["api_key"] = "test-key"
	openrouter["base_url"] = modelServer.URL
	openrouter["title_model"] = titleModel
	settings["agent"].(map[string]any)["workspace_root"] = t.TempDir()
	body, _ := json.Marshal(map[string]any{"settings": settings})
	env.do(t, env.client, "PUT", "/api/settings", string(body))

	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
	chatID := created["chat"].(map[string]any)["id"].(string)
	env.do(t, env.client, "POST", "/api/chats/"+chatID+"/messages", `{"text":"open a shell"}`)

	opened := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
		if terminals, _ := listed["terminals"].([]any); len(terminals) > 0 {
			opened = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !opened {
		dumpSteps(t, env, chatID)
		t.Fatal("no session was opened, so this proves nothing about deleting one")
	}

	res, _ := env.do(t, env.client, "DELETE", "/api/chats/"+chatID, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete failed: %d", res.StatusCode)
	}
	_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
	if terminals, _ := listed["terminals"].([]any); len(terminals) != 0 {
		t.Errorf("deleting the chat left %d sessions running", len(terminals))
	}
}
