package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
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

	// Wait for the session to show up in the chat's list with its first screen
	// on it. A session is listed the moment it exists, which is a moment before
	// the program inside it has printed anything, so what this waits for is the
	// screen rather than the row - otherwise the assertion below is a race with
	// a shell's first prompt and loses it on a loaded machine.
	var session map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
		terminals, _ := listed["terminals"].([]any)
		if len(terminals) > 0 {
			session = terminals[0].(map[string]any)
			if screen, _ := session["screen"].(string); strings.Contains(screen, "ready>") {
				break
			}
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
		`{"text":"typed by the user","keys":["enter"]}`)
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

// TestTerminalEventsCarryColour is the browser's side of the coloured screen:
// the fast per session stream has to deliver the styling, not just the text.
func TestTerminalEventsCarryColour(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	installedVoice(t)
	srv, err := New(st, t.TempDir())
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(srv.DetachTerminals)

	handle, err := srv.terminals.Open(t.Context(), "chat1", "colours", term.Spec{
		Command: "sh",
		Args:    []string{"-c", `printf '\033[31mred\033[0m plain\n'; sleep 30`},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.terminals.Close(ctx, handle.ID(), 2*time.Second)
	})
	if ok, state, err := handle.WaitFor(t.Context(), `red plain`, 20*time.Second); err != nil || !ok {
		t.Fatalf("the coloured line never appeared (ok=%v err=%v)\nscreen:\n%s", ok, err, state.Screen)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/terminals/"+handle.ID()+"/events", nil).WithContext(ctx)
	req.SetPathValue("id", handle.ID())
	rec := httptest.NewRecorder()
	srv.handleTerminalEvents(rec, req)

	var event struct {
		Terminal struct {
			Screen string `json:"screen"`
			Styled [][]struct {
				Text string `json:"t"`
				FG   string `json:"fg"`
			} `json:"styled"`
			Cursor *struct {
				Row     int  `json:"row"`
				Col     int  `json:"col"`
				Visible bool `json:"visible"`
			} `json:"cursor"`
		} `json:"terminal"`
	}
	found := false
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("event is not JSON: %v (%s)", err, line)
		}
		if event.Terminal.Screen != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the stream carried no screen at all:\n%s", rec.Body.String())
	}
	if !strings.Contains(event.Terminal.Screen, "red plain") {
		t.Errorf("plain screen = %q, want it to contain %q", event.Terminal.Screen, "red plain")
	}
	if len(event.Terminal.Styled) == 0 {
		t.Fatalf("the event carried no styled screen:\n%s", rec.Body.String())
	}
	first := event.Terminal.Styled[0]
	if len(first) < 2 || first[0].Text != "red" || first[0].FG != "a1" || first[1].FG != "" {
		t.Errorf("styled first line = %#v, want a red run followed by a plain one", first)
	}
	if event.Terminal.Cursor == nil {
		t.Error("the event carried no cursor")
	}
}

// The list is read on load and carries every session of a chat, so it stays
// plain text; the colours arrive with each session's own stream.
func TestTerminalListStaysPlain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	installedVoice(t)
	srv, err := New(st, t.TempDir())
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(srv.DetachTerminals)

	handle, err := srv.terminals.Open(t.Context(), "chat1", "colours", term.Spec{
		Command: "sh",
		Args:    []string{"-c", `printf '\033[31mred\033[0m\n'; sleep 30`},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.terminals.Close(ctx, handle.ID(), 2*time.Second)
	})
	if ok, _, err := handle.WaitFor(t.Context(), `red`, 20*time.Second); err != nil || !ok {
		t.Fatalf("the coloured line never appeared: %v", err)
	}
	for _, state := range srv.terminals.States("chat1") {
		if state.Plain().Styled != nil {
			t.Error("Plain left the styled screen behind")
		}
	}
}

// timedWriter records when each chunk of a stream was written, which is the
// only way to see that the screens were held back rather than merely counted.
type timedWriter struct {
	mu     sync.Mutex
	header http.Header
	body   strings.Builder
	at     []time.Time
	marks  []int // how many bytes had been written by each flush
}

func newTimedWriter() *timedWriter {
	return &timedWriter{header: http.Header{}}
}

func (w *timedWriter) Header() http.Header { return w.header }
func (w *timedWriter) WriteHeader(int)     {}

func (w *timedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(b)
}

func (w *timedWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.at = append(w.at, time.Now())
	w.marks = append(w.marks, w.body.Len())
}

func (w *timedWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

// events returns the payloads on the stream, each with the moment it was
// flushed to the browser.
func (w *timedWriter) events() []struct {
	When time.Time
	Data string
} {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []struct {
		When time.Time
		Data string
	}
	seen := 0
	body := w.body.String()
	for i, mark := range w.marks {
		chunk := body[seen:mark]
		seen = mark
		for _, line := range strings.Split(chunk, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			out = append(out, struct {
				When time.Time
				Data string
			}{w.at[i], strings.TrimPrefix(line, "data: ")})
		}
	}
	return out
}

// TestTerminalEventsAreCoalesced is the promise the dock makes to a phone on a
// weak connection: a busy screen is sent at a pace the connection can carry,
// and the screen the person is left looking at is the latest one.
func TestTerminalEventsAreCoalesced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	installedVoice(t)
	srv, err := New(st, t.TempDir())
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(srv.DetachTerminals)

	// Twenty changes over about a second and a half - a program writing as fast
	// as a person could ever want to watch.
	handle, err := srv.terminals.Open(t.Context(), "chat1", "busy", term.Spec{
		Command: "sh",
		Args: []string{"-c",
			`i=1; while [ $i -le 20 ]; do printf '\033[3%dmline %d\033[0m\n' $((i % 8)) $i; sleep 0.07; i=$((i+1)); done; sleep 30`},
		Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.terminals.Close(ctx, handle.ID(), 2*time.Second)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/terminals/"+handle.ID()+"/events", nil).WithContext(ctx)
	req.SetPathValue("id", handle.ID())
	rec := newTimedWriter()
	srv.handleTerminalEvents(rec, req)

	type screen struct {
		Terminal *struct {
			Revision int64  `json:"revision"`
			Screen   string `json:"screen"`
		} `json:"terminal"`
	}
	var (
		times     []time.Time
		revisions []int64
	)
	for _, event := range rec.events() {
		var parsed screen
		if err := json.Unmarshal([]byte(event.Data), &parsed); err != nil {
			t.Fatalf("event is not JSON: %v (%s)", err, event.Data)
		}
		if parsed.Terminal == nil {
			continue
		}
		times = append(times, event.When)
		revisions = append(revisions, parsed.Terminal.Revision)
	}
	if len(revisions) < 2 {
		t.Fatalf("the stream carried %d screens, expected several:\n%s", len(revisions), rec.text())
	}
	// The burst lasts about a second and a half, so a stream that passed every
	// change straight through would carry a dozen or more screens.
	if len(revisions) > 12 {
		t.Errorf("the screens were barely held back at all: %d of them", len(revisions))
	}
	// The gap is checked against a written down number rather than the constant,
	// so that lowering the constant makes this test fail rather than agree.
	const minGap = 120 * time.Millisecond
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < minGap {
			t.Errorf("screens %d and %d arrived %v apart, closer than the %v minimum",
				i-1, i, gap, terminalCoalesce)
		}
	}
	// And whatever was dropped, the last screen is the current one.
	final := handle.State()
	last := revisions[len(revisions)-1]
	if last != final.Revision {
		t.Errorf("the stream stopped at revision %d while the session is at %d", last, final.Revision)
	}
	if !strings.Contains(final.Screen, "line 20") {
		t.Errorf("the burst never finished, so this proves nothing:\n%s", final.Screen)
	}
}

// Archiving a chat has to leave nothing of it running. The transcript stays,
// but the terminal sessions - which deliberately outlive a run - are ended
// exactly as a deletion would end them.
func TestArchivingAChatClosesItsTerminals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	model := &scriptedModel{steps: []string{
		toolCall("terminal_open", map[string]any{
			"command": `sh -c 'printf "ready> "; while IFS= read -r l; do echo "$l"; done'`,
			"name":    "a shell",
		}),
		`{"choices":[{"delta":{"content":"The session is open."},"finish_reason":"stop"}]}`,
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
	if res, _ := env.do(t, env.client, "PUT", "/api/settings", string(body)); res.StatusCode != http.StatusOK {
		t.Fatalf("could not configure the model: %d", res.StatusCode)
	}

	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
	chatID := created["chat"].(map[string]any)["id"].(string)
	if res, sent := env.do(t, env.client, "POST", "/api/chats/"+chatID+"/messages", `{"text":"open a shell"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("sending the message failed: %d %#v", res.StatusCode, sent)
	}

	sessionID := ""
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
		if terminals, _ := listed["terminals"].([]any); len(terminals) > 0 {
			sessionID = terminals[0].(map[string]any)["id"].(string)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if sessionID == "" {
		dumpSteps(t, env, chatID)
		t.Fatal("no terminal session was opened for the chat")
	}

	if res, archived := env.do(t, env.client, "POST", "/api/chats/"+chatID+"/archive", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d %v", res.StatusCode, archived)
	}

	_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
	if terminals, _ := listed["terminals"].([]any); len(terminals) != 0 {
		t.Fatalf("the chat still has terminal sessions after archiving: %#v", listed)
	}
	if res, _ := env.do(t, env.client, "GET", "/api/terminals/"+sessionID, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("the session is still there after archiving: %d", res.StatusCode)
	}
	// What archiving keeps is the conversation itself.
	_, snapshot := env.do(t, env.client, "GET", "/api/chats/"+chatID, "")
	if messages, _ := snapshot["messages"].([]any); len(messages) == 0 {
		t.Fatalf("the transcript was lost: %#v", snapshot)
	}
	if snapshot["chat"].(map[string]any)["archived"] != true {
		t.Fatalf("the chat does not report itself as archived: %#v", snapshot["chat"])
	}
}

// TestUserOpensATerminal is the button in the dock: the person wants a shell in
// the chat's working directory without asking Socrates for one. There is only
// ever one terminal per chat, so asking twice hands back the one that is
// already there rather than a second screen.
func TestUserOpensATerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)

	workspaces := t.TempDir()
	_, data := env.do(t, env.client, "GET", "/api/settings", "")
	settings := data["settings"].(map[string]any)
	settings["agent"].(map[string]any)["workspace_root"] = workspaces
	body, _ := json.Marshal(map[string]any{"settings": settings})
	if res, _ := env.do(t, env.client, "PUT", "/api/settings", string(body)); res.StatusCode != http.StatusOK {
		t.Fatalf("could not set the workspace root: %d", res.StatusCode)
	}

	_, created := env.do(t, env.client, "POST", "/api/chats", `{}`)
	chatID := created["chat"].(map[string]any)["id"].(string)

	res, opened := env.do(t, env.client, "POST", "/api/chats/"+chatID+"/terminals", `{}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("opening a terminal failed: %d %#v", res.StatusCode, opened)
	}
	terminal, _ := opened["terminal"].(map[string]any)
	if terminal == nil {
		t.Fatalf("no terminal in the answer: %#v", opened)
	}
	id, _ := terminal["id"].(string)
	if !strings.HasPrefix(id, "term_") {
		t.Fatalf("terminal id = %q", id)
	}
	if terminal["name"] != "terminal" {
		t.Errorf("name = %v, want \"terminal\"", terminal["name"])
	}
	if terminal["chat_id"] != chatID {
		t.Errorf("chat_id = %v, want %q", terminal["chat_id"], chatID)
	}
	if terminal["running"] != true {
		t.Errorf("the session should be running: %#v", terminal)
	}
	if dir, _ := terminal["dir"].(string); dir != filepath.Join(workspaces, chatID) {
		t.Errorf("dir = %q, want the chat's workspace under %q", dir, workspaces)
	}
	t.Cleanup(func() { env.do(t, env.client, "POST", "/api/terminals/"+id+"/close", `{}`) })

	// It is an ordinary session: the chat lists it like any other.
	_, listed := env.do(t, env.client, "GET", "/api/chats/"+chatID+"/terminals", "")
	terminals, _ := listed["terminals"].([]any)
	if len(terminals) != 1 || terminals[0].(map[string]any)["id"] != id {
		t.Fatalf("the chat should list exactly the new session: %#v", terminals)
	}

	// A second one is refused, and told so plainly: the browser fetches the
	// list from here, which is where the session it already has is described.
	res, again := env.do(t, env.client, "POST", "/api/chats/"+chatID+"/terminals", `{}`)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a second terminal should be refused, got %d %#v", res.StatusCode, again)
	}
	if s, _ := again["error"].(string); strings.TrimSpace(s) == "" {
		t.Error("the refusal came without an explanation")
	}

	// And a chat that does not exist is a 404, not a stray shell.
	res, _ = env.do(t, env.client, "POST", "/api/chats/does-not-exist/terminals", `{}`)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("opening a terminal for a missing chat = %d, want 404", res.StatusCode)
	}
}
