//go:build !windows

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The Status button is tested against the real substrate - a real tmux, a real
// shell in a real pane - and a stand in for OpenRouter, because what has to be
// proven is that the screen somebody is looking at is the screen the model was
// shown, not that a gateway can be spoken to.

/* --------------------------------------------------------- a fake gateway */

// gatewayCall is one request the server made, as the stand in read it.
type gatewayCall struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
}

// last is the newest message, which is the screen the model was shown.
func (c gatewayCall) last() string {
	if len(c.Messages) == 0 {
		return ""
	}
	return c.Messages[len(c.Messages)-1].Content
}

func (c gatewayCall) joined() string {
	parts := make([]string, 0, len(c.Messages))
	for _, m := range c.Messages {
		parts = append(parts, m.Role+": "+m.Content)
	}
	return strings.Join(parts, "\n---\n")
}

// scriptedGateway answers /chat/completions with the next reply in its queue,
// falling back to a fixed one when the queue runs out. The operator loop needs
// a different answer per step, which is the whole reason it is a queue.
type scriptedGateway struct {
	srv *httptest.Server

	mu       sync.Mutex
	replies  []string
	fallback string
	status   int
	body     string
	calls    []gatewayCall
	gate     chan struct{}
}

func newGateway(t *testing.T) *scriptedGateway {
	t.Helper()
	g := &scriptedGateway{fallback: `{"actions":[],"done":true,"summary":"nothing to do"}`}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var call gatewayCall
		_ = json.Unmarshal(raw, &call)

		g.mu.Lock()
		g.calls = append(g.calls, call)
		gate, status, body := g.gate, g.status, g.body
		reply := g.fallback
		if len(g.replies) > 0 {
			reply, g.replies = g.replies[0], g.replies[1:]
		}
		g.mu.Unlock()

		if gate != nil {
			// A gateway that has not answered yet is a run stuck in
			// "thinking", which is where a cancel has to be able to reach it.
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		payload, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message":       map[string]any{"content": reply},
			}},
		})
		_, _ = w.Write(payload)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *scriptedGateway) script(replies ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.replies = append([]string{}, replies...)
}

func (g *scriptedGateway) always(reply string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fallback = reply
}

func (g *scriptedGateway) refuse(status int, message string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status, g.body = status, fmt.Sprintf(`{"error":{"message":%q}}`, message)
}

func (g *scriptedGateway) block() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate = make(chan struct{})
}

func (g *scriptedGateway) release() {
	g.mu.Lock()
	gate := g.gate
	g.gate = nil
	g.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (g *scriptedGateway) seen() []gatewayCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gatewayCall{}, g.calls...)
}

func (g *scriptedGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

/* ------------------------------------------------------------- the set up */

// assistEnv is a signed in server with a live shell session and a stand in
// gateway the server's OpenRouter client is pointed at.
type assistEnv struct {
	*sessionEnv
	gw *scriptedGateway
	id string
}

func newAssistEnv(t *testing.T) *assistEnv {
	t.Helper()
	e := newSessionEnv(t)
	gw := newGateway(t)
	e.configureOpenRouter(t, map[string]any{
		"base_url": gw.srv.URL, "api_key": "k",
		"status_model": "test/status-model", "title_model": "test/title-model",
	})
	return &assistEnv{sessionEnv: e, gw: gw, id: e.shellSession(100, 30)}
}

/* ---------------------------------------------------------------- status */

// The whole point of the Status button: a model is shown the screen and the
// sentence it answers with comes back, in the language the voice speaks,
// together with what the detector says the session is doing.
func TestStatusSaysWhatTheScreenShows(t *testing.T) {
	e := newAssistEnv(t)
	e.configureVoice(t, map[string]any{"language": "de"})
	e.gw.always("Der Testlauf ist durch, ohne Fehler.")

	// Something on the screen that only this test could have put there.
	e.typeLine(e.id, "echo status-probe-9times")
	e.waitForPane(e.id, "status-probe-9times")

	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/status", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %#v", res.StatusCode, payload)
	}
	if payload["text"] != "Der Testlauf ist durch, ohne Fehler." {
		t.Fatalf("text = %#v", payload["text"])
	}
	if payload["language"] != "de" {
		t.Fatalf("language = %#v, want the voice's language", payload["language"])
	}
	if payload["model"] != "test/status-model" {
		t.Fatalf("model = %#v", payload["model"])
	}
	state, _ := payload["state"].(string)
	switch termux.State(state) {
	case termux.StateIdle, termux.StateBusy, termux.StateWaiting, termux.StateUnknown:
	default:
		t.Fatalf("state = %#v", payload["state"])
	}

	calls := e.gw.seen()
	if len(calls) != 1 {
		t.Fatalf("the gateway saw %d calls", len(calls))
	}
	call := calls[0]
	if call.Model != "test/status-model" {
		t.Fatalf("the request named %q", call.Model)
	}
	if call.MaxTokens != 300 {
		t.Fatalf("max_tokens = %d", call.MaxTokens)
	}
	prompt := call.last()
	if !strings.Contains(prompt, "status-probe-9times") {
		t.Fatalf("the model was not shown the screen:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Answer in German") {
		t.Fatalf("the model was not told which language to answer in:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Shell") {
		t.Fatalf("the model was not told what runs in the pane:\n%s", prompt)
	}
	// The screen is a screen, not a scrollback: at most the lines the spec
	// gives it, plus the instruction in front of them.
	screen := prompt[strings.Index(prompt, "Screen:\n")+len("Screen:\n"):]
	if lines := strings.Count(screen, "\n") + 1; lines > statusScreenLines {
		t.Fatalf("the model was shown %d lines, want at most %d", lines, statusScreenLines)
	}
}

// Nothing here validates a model id on the way in - there is no catalogue
// endpoint left - so the only place a wrong one can be reported is the refusal
// itself, and it has to be a 4xx that names the id and where to change it. A
// bare 500 would send the page into its retry loop for a model that will never
// exist.
func TestStatusReportsAMissingKeyAndAnUnknownModel(t *testing.T) {
	e := newAssistEnv(t)

	e.configureOpenRouter(t, map[string]any{"api_key": ""})
	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/status", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status without a key = %d %#v", res.StatusCode, payload)
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, "/admin") {
		t.Fatalf("error = %q, want somewhere to go", message)
	}

	e.configureOpenRouter(t, map[string]any{"api_key": "k", "status_model": "nobody/nothing"})
	e.gw.refuse(http.StatusNotFound, "nobody/nothing is not a valid model ID")
	res, payload = e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/status", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status with an unknown model = %d %#v", res.StatusCode, payload)
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "unknown model nobody/nothing") || !strings.Contains(message, "/admin") {
		t.Fatalf("error = %q", message)
	}
}

// A key OpenRouter will not take is not a model that does not exist, and the
// two send an owner to different halves of the dashboard.
func TestAssistReportsARejectedKey(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.refuse(http.StatusUnauthorized, "No auth credentials found")

	res, payload := e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/status", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status with a rejected key = %d %#v", res.StatusCode, payload)
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "rejected the API key") || !strings.Contains(message, "/admin") {
		t.Fatalf("error = %q", message)
	}
	if strings.Contains(message, "unknown model") {
		t.Fatalf("a bad key was reported as a bad model: %q", message)
	}
}

/* ------------------------------------------------------- the status phases */

// Pressing Status is a question to a model over a network, and the page has to
// be able to show that something is happening. The phases arrive on the socket,
// in order, and the last one carries the answer.
func TestStatusStreamsItsPhases(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("It is sitting at a prompt.")
	socket := e.dialWS(e.id, "viewer=tab-status&cols=100&rows=30")
	socket.hello()

	if res, payload := e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/status", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %#v", res.StatusCode, payload)
	}

	var phases []string
	var last map[string]any
	socket.await("the status phases", 30*time.Second, func() bool {
		phases = phases[:0]
		for _, frame := range socket.ctrl {
			if frame["t"] != "status" {
				continue
			}
			phases = append(phases, fmt.Sprint(frame["phase"]))
			last = frame
		}
		return len(phases) >= 4
	})
	if got := strings.Join(phases, ","); got != "capturing,asking,speaking,done" {
		t.Fatalf("the phases were %s", got)
	}
	if last["id"] != e.id {
		t.Fatalf("the frame names %v", last["id"])
	}
	if last["text"] != "It is sitting at a prompt." {
		t.Fatalf("the last phase carries %#v", last["text"])
	}

	// And the failing path says so in the same place.
	e.gw.refuse(http.StatusNotFound, "nobody/nothing is not a valid model ID")
	e.configureOpenRouter(t, map[string]any{"status_model": "nobody/nothing"})
	if res, _ := e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/status", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a refused status = %d", res.StatusCode)
	}
	socket.await("an error phase", 30*time.Second, func() bool {
		for _, frame := range socket.ctrl {
			if frame["t"] == "status" && frame["phase"] == statusPhaseError {
				return true
			}
		}
		return false
	})
}
