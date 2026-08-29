package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// mockRouter serves OpenAI style streaming responses in a fixed order.
type mockRouter struct {
	mu        sync.Mutex
	responses []string
	index     int
	bodies    []map[string]any
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
	var chunk string
	if m.index < len(m.responses) {
		chunk = m.responses[m.index]
		m.index++
	} else {
		chunk = sseText("fallback")
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, chunk)
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

func newTestEngine(t *testing.T, router *mockRouter, backends []config.Backend) (*Engine, *store.Store) {
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
	settings.Backends = backends
	settings.Normalize()

	engine := New(st, NewBus(), func() config.Settings { return settings })
	return engine, st
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

func TestRunDelegatesAndAnswers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	router := &mockRouter{responses: []string{
		sseToolCall("delegate_to_agent", `{"agent":"echo","task":"say something","title":"Echoing"}`),
		sseText("All done."),
	}}
	engine, st := newTestEngine(t, router, []config.Backend{{
		ID: "echo", Kind: config.KindCustom, Name: "Echo", Enabled: true,
		Description: "echoes", Command: "sh",
		ExtraArgs:      []string{"-c", "printf 'delegate output\n'"},
		TimeoutSeconds: 30,
	}})

	chat := &store.Chat{ID: "chat1"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(chat.ID, "please do the thing", false)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRun(t, st, run.ID, store.RunDone)

	messages, err := st.ListMessages(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := messages[len(messages)-1]
	if last.Role != "assistant" || last.Content != "All done." {
		t.Fatalf("last message = %#v", last)
	}

	steps, err := st.ListSteps(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	var delegate *store.Step
	var sub *store.Step
	for i := range steps {
		if steps[i].Kind == store.StepDelegate {
			delegate = &steps[i]
		}
		if strings.HasPrefix(steps[i].Kind, "sub_") {
			sub = &steps[i]
		}
	}
	if delegate == nil {
		t.Fatal("no delegate step was recorded")
	}
	if delegate.Status != store.StatusDone {
		t.Errorf("delegate status = %q", delegate.Status)
	}
	if delegate.Body != "Echoing" {
		t.Errorf("delegate label = %q", delegate.Body)
	}
	if !strings.Contains(string(delegate.Detail), "delegate output") {
		t.Errorf("delegate detail missing the result: %s", delegate.Detail)
	}
	if sub == nil {
		t.Error("no child step from the delegate agent")
	}

	// The second model call must contain the tool result.
	if router.calls() < 2 {
		t.Fatalf("expected two model calls, got %d", router.calls())
	}
	raw, _ := json.Marshal(router.bodies[1]["messages"])
	if !strings.Contains(string(raw), "delegate output") {
		t.Errorf("tool result was not fed back to the model: %s", raw)
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
	run, err := engine.Start(chat.ID, "decide for me", false)
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

func TestUnknownAgentIsReportedToModel(t *testing.T) {
	router := &mockRouter{responses: []string{
		sseToolCall("delegate_to_agent", `{"agent":"ghost","task":"x"}`),
		sseText("Sorry, that agent is not available."),
	}}
	engine, st := newTestEngine(t, router, []config.Backend{{
		ID: "echo", Kind: config.KindCustom, Name: "Echo", Enabled: true, Command: "sh",
		ExtraArgs: []string{"-c", "true"}, TimeoutSeconds: 10,
	}})
	chat := &store.Chat{ID: "chat3"}
	if err := st.CreateChat(chat); err != nil {
		t.Fatal(err)
	}
	run, err := engine.Start(chat.ID, "use ghost", false)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunDone)
	raw, _ := json.Marshal(router.bodies[1]["messages"])
	if !strings.Contains(string(raw), "no enabled agent") {
		t.Errorf("model was not told about the bad agent id: %s", raw)
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
	run, err := engine.Start(chat.ID, "first", false)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, st, run.ID, store.RunWaiting)
	if _, err := engine.Start(chat.ID, "second", false); err == nil {
		t.Fatal("a second run should be rejected while one is in flight")
	}
	engine.Stop(chat.ID)
}
