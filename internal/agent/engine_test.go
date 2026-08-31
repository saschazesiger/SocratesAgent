package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// mockRouter serves OpenAI style streaming responses in a fixed order.
type mockRouter struct {
	mu        sync.Mutex
	responses []string
	index     int
	bodies    []map[string]any
	// rewriteSession swaps the placeholder SESSION in a scripted tool call for
	// the session id that the previous call actually returned.
	rewriteSession bool
	session        string
}

func (m *mockRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	m.mu.Lock()
	model, _ := body["model"].(string)
	if model == "title-model" {
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"A title"},"finish_reason":"stop"}]}`)
		return
	}
	m.bodies = append(m.bodies, body)
	if m.rewriteSession {
		if id := findSessionID(raw); id != "" {
			m.session = id
		}
	}
	var chunk string
	if m.index < len(m.responses) {
		chunk = m.responses[m.index]
		m.index++
	} else {
		chunk = sseText("fallback")
	}
	if m.rewriteSession && m.session != "" {
		chunk = strings.ReplaceAll(chunk, "SESSION", m.session)
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, chunk)
}

// findSessionID picks the session id out of a tool result the engine sent.
var sessionPattern = regexp.MustCompile(`term_[0-9a-f]+`)

func findSessionID(raw []byte) string {
	return sessionPattern.FindString(string(raw))
}

func (m *mockRouter) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

func sseText(text string) string {
	payload, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": text}, "finish_reason": "stop"}},
	})
	return "data: " + string(payload) + "\n\ndata: [DONE]\n\n"
}

func sseToolCall(name, args string) string {
	payload, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			}},
		}}},
	})
	done, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}},
	})
	return "data: " + string(payload) + "\n\ndata: " + string(done) + "\n\ndata: [DONE]\n\n"
}

// TestMain lets the test binary stand in for the Socrates binary, which is
// what the terminal manager re-executes to host a session.
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

func newTestEngine(t *testing.T, router *mockRouter, tools []config.Tool) (*Engine, *store.Store) {
	t.Helper()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	settings := config.Default()
	settings.OpenRouter.APIKey = "test-key"
	settings.OpenRouter.BaseURL = server.URL
	settings.OpenRouter.ChatModel = "test-model"
	settings.OpenRouter.TitleModel = "title-model"
	settings.Agent.WorkspaceRoot = t.TempDir()
	settings.Agent.MaxIterations = 6
	if tools != nil {
		settings.Tools = tools
	}
	settings.Normalize()

	terminals, err := term.NewManager(filepath.Join(t.TempDir(), "terminals"), os.Args[0])
	if err != nil {
		t.Fatalf("terminal manager: %v", err)
	}
	t.Cleanup(func() {
		for _, h := range terminals.List("") {
			_ = terminals.Close(context.Background(), h.ID(), time.Second)
		}
		terminals.Detach()
	})

	engine := New(st, NewBus(), func() config.Settings { return settings }, terminals)
	return engine, st
}

// lastPayload returns the messages of the nth model call, as text.
func lastPayload(t *testing.T, router *mockRouter, index int) string {
	t.Helper()
	router.mu.Lock()
	defer router.mu.Unlock()
	if index >= len(router.bodies) {
		t.Fatalf("only %d model calls were made, wanted at least %d", len(router.bodies), index+1)
	}
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	// Without this, > and < come back as \u003e and \u003c and every
	// assertion about a shell prompt fails for the wrong reason.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(router.bodies[index]["messages"]); err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return buf.String()
}

func waitForRun(t *testing.T, st *store.Store, runID string, want string) *store.Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := st.GetRun(runID)
		if err == nil && run.Status == want {
			return run
		}
		if err == nil && run.Status != store.RunRunning && run.Status != store.RunWaiting && run.Status != want {
			t.Fatalf("run ended as %q (%s), wanted %q", run.Status, run.Error, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, _ := st.GetRun(runID)
	t.Fatalf("timed out waiting for %q, run is %#v", want, run)
	return nil
}

func TestShellRunFeedsOutputBackToTheModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("shell_run", `{"command":"echo hello-from-the-shell"}`),
		sseText("All done."),
	}}
	engine, st := newTestEngine(t, router, nil)

	chat := &store.Chat{ID: "chat1"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "please do the thing", Auto: false})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	messages, err := st.ListMessages(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := messages[len(messages)-1]; last.Role != "assistant" || last.Content != "All done." {
		t.Fatalf("last message = %#v", last)
	}

	if payload := lastPayload(t, router, 1); !strings.Contains(payload, "hello-from-the-shell") {
		t.Errorf("the command output never reached the model: %s", payload)
	}

	steps, err := st.ListSteps(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	var shell *store.Step
	for i := range steps {
		if steps[i].Kind == store.StepShell {
			shell = &steps[i]
		}
	}
	if shell == nil {
		t.Fatal("the command was not recorded in the process view")
	}
	if shell.Status != store.StatusDone {
		t.Errorf("shell step status = %q", shell.Status)
	}
	if !strings.Contains(shell.Body, "hello-from-the-shell") {
		t.Errorf("the step is missing the output: %q", shell.Body)
	}
}

func TestShellRunReportsAFailingCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("shell_run", `{"command":"echo nope >&2; exit 7"}`),
		sseText("That failed."),
	}}
	engine, st := newTestEngine(t, router, nil)
	chat := &store.Chat{ID: "chat-fail"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, _ := engine.Start(Turn{ChatID: chat.ID, Text: "run it", Auto: false})
	waitForRun(t, st, run.ID, store.RunDone)

	payload := lastPayload(t, router, 1)
	if !strings.Contains(payload, "Exit code 7") {
		t.Errorf("the model was not told the exit code: %s", payload)
	}
	if !strings.Contains(payload, "nope") {
		t.Errorf("stderr never reached the model: %s", payload)
	}
}

// The whole point of the rework: Socrates opens a program, types into it and
// reads the screen, rather than running it once with an unattended flag.
func TestDrivesAnInteractiveProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"tool":"fake","name":"fake agent"}`),
		sseToolCall("terminal_send", `{"session":"SESSION","text":"hello","submit":true,"settle_seconds":3}`),
		sseToolCall("terminal_wait", `{"session":"SESSION","until":"text","text":"world","seconds":10}`),
		sseText("The agent answered."),
	}}
	// The session id is only known once it exists, so the mock rewrites the
	// placeholder with whatever the previous call returned.
	router.rewriteSession = true

	// A prompt driven program, which is all a coding agent looks like from the
	// outside: it prints a prompt, waits for a line, answers, prompts again.
	engine, st := newTestEngine(t, router, []config.Tool{{
		ID: "fake", Name: "Fake Agent", Enabled: true,
		Description: "a scripted interactive program",
		Command:     "sh",
		Args: []string{"-c", `printf 'ready> '; while IFS= read -r line; do ` +
			`case "$line" in hello) printf 'world\r\n';; quit) exit 0;; ` +
			`*) printf 'you said: %s\r\n' "$line";; esac; printf 'ready> '; done`},
		Driving:     "type and press enter",
		IdleSeconds: 1, TimeoutSeconds: 60,
	}})

	chat := &store.Chat{ID: "chat-drive"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "ask the agent something", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	// Every call after the first must have seen the screen.
	opened := lastPayload(t, router, 1)
	if !strings.Contains(opened, "ready>") {
		t.Errorf("the first screen was not handed to the model: %s", opened)
	}
	if !strings.Contains(opened, "type and press enter") {
		t.Errorf("the model was not told how to drive the program: %s", opened)
	}
	answered := lastPayload(t, router, 3)
	if !strings.Contains(answered, "world") {
		t.Errorf("the program's answer never reached the model: %s", answered)
	}

	steps, err := st.ListSteps(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	var terminal *store.Step
	for i := range steps {
		if steps[i].Kind == store.StepTerminal {
			terminal = &steps[i]
		}
	}
	if terminal == nil {
		t.Fatal("the session does not show up in the process view")
	}
	if terminal.Title != "fake agent" {
		t.Errorf("session label = %q", terminal.Title)
	}

	// The session is still open: it belongs to the chat, not to the run.
	if got := len(engine.Terminals.List(chat.ID)); got != 1 {
		t.Errorf("chat has %d sessions after the run, want 1", got)
	}
}

// A tool's environment is the declarative form of writing "KEY=VALUE program"
// in a shell, and Claude Code will not skip permissions as root without it.
func TestToolEnvironmentReachesTheProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a real terminal")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"tool":"env-probe"}`),
		sseText("Checked."),
	}}
	engine, st := newTestEngine(t, router, []config.Tool{{
		ID: "env-probe", Name: "Env Probe", Enabled: true,
		Description: "prints one environment variable",
		Command:     "sh",
		Args:        []string{"-c", `printf 'sandbox=[%s]\r\n' "$IS_SANDBOX"; sleep 5`},
		Env:         []string{"IS_SANDBOX=1"},
		IdleSeconds: 1, TimeoutSeconds: 60,
	}})

	chat := &store.Chat{ID: "chat-env"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "start the probe", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	opened := lastPayload(t, router, 1)
	if !strings.Contains(opened, "sandbox=[1]") {
		t.Errorf("the tool's environment never reached the program: %s", opened)
	}
}

func TestUnknownToolIsReportedToModel(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_open", `{"tool":"ghost"}`),
		sseText("Sorry, that program is not available."),
	}}
	engine, st := newTestEngine(t, router, []config.Tool{{
		ID: "echo", Name: "Echo", Enabled: true, Command: "sh", TimeoutSeconds: 10,
	}})
	chat := &store.Chat{ID: "chat3"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "use ghost", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)
	if payload := lastPayload(t, router, 1); !strings.Contains(payload, "no enabled program called") {
		t.Errorf("the model was not told about the bad tool id: %s", payload)
	}
}

func TestUnknownSessionIsReportedToModel(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("terminal_send", `{"session":"term_nonexistent","text":"hi"}`),
		sseText("There is no such session."),
	}}
	engine, st := newTestEngine(t, router, nil)
	chat := &store.Chat{ID: "chat-nosession"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, _ := engine.Start(Turn{ChatID: chat.ID, Text: "type into nothing", Auto: false})
	waitForRun(t, st, run.ID, store.RunDone)
	if payload := lastPayload(t, router, 1); !strings.Contains(payload, "no session called") {
		t.Errorf("the model was not told the session is unknown: %s", payload)
	}
}

func TestRunAsksUserAndResumes(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("ask_user", `{"question":"Which one?","options":[{"label":"Left"},{"label":"Right"}]}`),
		sseText("Going left then."),
	}}
	engine, st := newTestEngine(t, router, nil)

	chat := &store.Chat{ID: "chat2"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "decide for me", Auto: false})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRun(t, st, run.ID, store.RunWaiting)

	question, err := st.PendingQuestion(chat.ID)
	if err != nil {
		t.Fatalf("pending question: %v", err)
	}
	if len(question.Options) != 2 || question.Options[0].Label != "Left" {
		t.Fatalf("options = %#v", question.Options)
	}
	if err := engine.Answer(question.ID, "Left"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	messages, _ := st.ListMessages(chat.ID)
	if messages[len(messages)-1].Content != "Going left then." {
		t.Fatalf("unexpected answer %#v", messages[len(messages)-1])
	}
	raw, _ := json.Marshal(router.bodies[1]["messages"])
	if !strings.Contains(string(raw), "The user answered: Left") {
		t.Errorf("the answer never reached the model: %s", raw)
	}
}

func TestStartRejectsSecondRun(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("ask_user", `{"question":"wait?"}`),
	}}
	engine, st := newTestEngine(t, router, nil)
	chat := &store.Chat{ID: "chat4"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(Turn{ChatID: chat.ID, Text: "first", Auto: false})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunWaiting)
	if _, err := engine.Start(Turn{ChatID: chat.ID, Text: "second", Auto: false}); err == nil {
		t.Fatal("a second run should be rejected while one is in flight")
	}
	engine.Stop(chat.ID)
}

// The answer is read out loud by a voice that speaks one language. An English
// answer coming out of a German voice is the worst of both, so the language
// chosen for speech is also the language the agent is told to write in.
func TestSystemPromptCarriesTheSpokenLanguage(t *testing.T) {
	engine, _ := newTestEngine(t, &mockRouter{}, nil)
	chat := &store.Chat{ID: "c"}

	settings := config.Default()
	settings.Agent.WorkspaceRoot = t.TempDir()
	settings.Voice.Language = config.LanguageDE
	settings.Normalize()
	prompt := engine.systemPrompt(chat, settings, nil)
	if !strings.Contains(prompt, "in German") {
		t.Fatalf("german was not requested:\n%s", prompt)
	}

	settings.Voice.Language = config.LanguageAuto
	prompt = engine.systemPrompt(chat, settings, nil)
	if strings.Contains(prompt, "in German") {
		t.Fatalf("automatic pinned a language:\n%s", prompt)
	}
	if !strings.Contains(prompt, "language they wrote or spoke to you in") {
		t.Fatalf("automatic did not follow the user:\n%s", prompt)
	}
}
