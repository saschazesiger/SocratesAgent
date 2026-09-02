//go:build !windows

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The Status button and the operator loop are tested against the real
// substrate - a real tmux, a real shell in a real pane - and a stand in for
// OpenRouter, because what has to be proven is that the model's answer reaches
// the keyboard and that the guards hold, not that a gateway can be spoken to.

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
		"status_model": "test/status-model", "agent_model": "test/agent-model",
	})
	return &assistEnv{sessionEnv: e, gw: gw, id: e.shellSession(100, 30)}
}

// configureAgent saves the agent section the way the dashboard does.
func (e *sessionEnv) configureAgent(fields map[string]any) {
	e.t.Helper()
	_, data := e.do(e.t, e.client, "GET", "/api/settings", "")
	settings, _ := data["settings"].(map[string]any)
	if settings == nil {
		e.t.Fatalf("settings: %#v", data)
	}
	agent, _ := settings["agent"].(map[string]any)
	if agent == nil {
		agent = map[string]any{}
		settings["agent"] = agent
	}
	for key, value := range fields {
		agent[key] = value
	}
	body, _ := json.Marshal(map[string]any{"settings": settings})
	if res, payload := e.do(e.t, e.client, "PUT", "/api/settings", string(body)); res.StatusCode != http.StatusOK {
		e.t.Fatalf("saving the agent settings failed: %d %#v", res.StatusCode, payload)
	}
}

// runOf reads the run the API reports, which is null when there is none.
func (e *assistEnv) runOf() map[string]any {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "GET", "/api/sessions/"+e.id+"/agent", "")
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("GET agent: %d %#v", res.StatusCode, payload)
	}
	run, _ := payload["run"].(map[string]any)
	return run
}

// awaitRun waits for the run to satisfy a condition, which is how a test
// follows a loop that outlives the request that started it.
func (e *assistEnv) awaitRun(what string, within time.Duration, cond func(map[string]any) bool) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(within)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = e.runOf()
		if last != nil && cond(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("the run never became %s; it is %#v", what, last)
	return nil
}

func (e *assistEnv) startRun(prompt string) (*http.Response, map[string]any) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"prompt": prompt})
	return e.do(e.t, e.client, "POST", "/api/sessions/"+e.id+"/agent", string(body))
}

// agentFrame waits for an agent frame on the socket that satisfies a condition.
func (c *wsClient) agentFrame(what string, cond func(map[string]any) bool) map[string]any {
	c.t.Helper()
	var found map[string]any
	c.await("an agent frame that is "+what, 60*time.Second, func() bool {
		for _, frame := range c.ctrl {
			if frame["t"] != "agent" {
				continue
			}
			if cond(frame) {
				found = frame
				return true
			}
		}
		return false
	})
	return found
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

/* ---------------------------------------------------------- the operator */

// The operator run end to end: a model that answers with actions, keys that
// land in a real pane, and a run that ends because the model says it is done.
func TestAgentRunDrivesThePane(t *testing.T) {
	e := newAssistEnv(t)
	// The second answer arrives inside a fence, which is what a chat model
	// does however plainly it was asked not to.
	e.gw.script(
		`{"actions":[{"text":"touch operator-ran"},{"key":"Enter"}],"done":false,`+
			`"summary":"typing the command","note":"then I will look again"}`,
		"```json\n"+`{"actions":[],"done":true,"summary":"the file is there"}`+"\n```",
	)

	socket := e.dialWS(e.id, "viewer=tab-a&cols=100&rows=30")
	socket.hello()

	res, payload := e.startRun("create a file called operator-ran")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %#v", res.StatusCode, payload)
	}
	runID, _ := payload["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run id: %#v", payload)
	}

	// The keys reached the shell, and the shell ran them.
	marker := filepath.Join(e.work, "operator-ran")
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			screen, _ := e.tmux("capture-pane", "-p", "-t", termux.TmuxName(e.id))
			t.Fatalf("the operator never created %s; the pane shows:\n%s", marker, screen)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Every step was on the socket, in the shape the page renders.
	acted := socket.agentFrame("an action", func(f map[string]any) bool {
		return f["phase"] == phaseActing && f["action"] == "pressed Enter"
	})
	for _, key := range []string{"run_id", "step", "phase", "action", "note", "done", "error", "prompt", "summary", "started"} {
		if _, ok := acted[key]; !ok {
			t.Fatalf("the agent frame has no %q: %#v", key, acted)
		}
	}
	if acted["run_id"] != runID {
		t.Fatalf("the frame is for run %#v, not %q", acted["run_id"], runID)
	}
	if acted["prompt"] != "create a file called operator-ran" {
		t.Fatalf("the frame carries no prompt: %#v", acted)
	}
	done := socket.agentFrame("done", func(f map[string]any) bool { return f["done"] == true })
	if done["phase"] != phaseDone || done["summary"] != "the file is there" || done["error"] != "" {
		t.Fatalf("the last frame is %#v", done)
	}

	// And GET mirrors it, from the same view the frame was built from.
	run := e.awaitRun("done", 30*time.Second, func(r map[string]any) bool { return r["done"] == true })
	if run["run_id"] != runID || run["summary"] != "the file is there" || run["phase"] != phaseDone {
		t.Fatalf("GET agent = %#v", run)
	}
	if step, _ := run["step"].(float64); step != 2 {
		t.Fatalf("the run took %v steps, want two", run["step"])
	}
	// The model was shown the screen every time, and the second call carried
	// the first decision with it.
	calls := e.gw.seen()
	if len(calls) != 2 {
		t.Fatalf("the gateway saw %d calls, want two", len(calls))
	}
	if calls[0].Model != "test/agent-model" || calls[0].MaxTokens != 800 {
		t.Fatalf("the request was %#v", calls[0])
	}
	if !strings.Contains(calls[0].joined(), "system: You drive a terminal that runs Shell") {
		t.Fatalf("no system prompt:\n%s", calls[0].joined())
	}
	if !strings.Contains(calls[0].joined(), "Goal: create a file called operator-ran") {
		t.Fatalf("the goal is missing:\n%s", calls[0].joined())
	}
	if !strings.Contains(calls[1].joined(), "touch operator-ran") {
		t.Fatalf("the second step did not carry the first decision:\n%s", calls[1].joined())
	}

	// A run lives only in memory and is not a log: once it is old enough,
	// there is nothing to report and the page starts clean.
	e.srv.agents.setKeep(0)
	if run := e.runOf(); run != nil {
		t.Fatalf("a finished run is still reported: %#v", run)
	}
}

// One run per session, and a Cancel that reaches a run which is waiting on the
// model rather than on the terminal.
func TestAgentRefusesASecondRunAndCancels(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.block()
	e.gw.always(`{"actions":[{"key":"Escape"}],"done":false}`)

	res, payload := e.startRun("look at the screen")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %#v", res.StatusCode, payload)
	}
	runID, _ := payload["run_id"].(string)

	// A live run is in hello, so a phone that reconnects mid-run sees it
	// without asking for it.
	e.awaitRun("live", 30*time.Second, func(r map[string]any) bool { return r["done"] == false })
	socket := e.dialWS(e.id, "viewer=tab-b&cols=100&rows=30")
	hello := socket.hello()
	live, _ := hello["agent"].(map[string]any)
	if live == nil || live["run_id"] != runID {
		t.Fatalf("hello carried %#v for agent", hello["agent"])
	}

	res, payload = e.startRun("something else")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a second run = %d %#v", res.StatusCode, payload)
	}
	if other, _ := payload["run"].(map[string]any); other == nil || other["run_id"] != runID {
		t.Fatalf("the refusal did not name the live run: %#v", payload)
	}

	began := time.Now()
	res, payload = e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/agent/cancel", "")
	if res.StatusCode != http.StatusOK || payload["ok"] != true {
		t.Fatalf("cancel: %d %#v", res.StatusCode, payload)
	}
	run := e.awaitRun("cancelled", 10*time.Second, func(r map[string]any) bool { return r["done"] == true })
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the cancel took %s", took)
	}
	if run["phase"] != phaseError {
		t.Fatalf("a cancelled run is %#v", run)
	}
	if failure, _ := run["error"].(string); !strings.Contains(failure, "cancelled") {
		t.Fatalf("error = %q", failure)
	}
	e.gw.release()

	// And the session is free again: cancelling is not the end of the button.
	e.gw.always(`{"actions":[],"done":true,"summary":"nothing needed doing"}`)
	if res, payload := e.startRun("try again"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("a run after a cancel = %d %#v", res.StatusCode, payload)
	}
	e.awaitRun("done", 60*time.Second, func(r map[string]any) bool { return r["done"] == true })
}

// A model that never says done stops at the step limit rather than at the
// budget of whoever is paying for it.
func TestAgentStopsAtTheStepLimit(t *testing.T) {
	e := newAssistEnv(t)
	e.configureAgent(map[string]any{"max_steps": 2})
	e.gw.always(`{"actions":[{"key":"Escape"}],"done":false,"summary":"still looking"}`)

	if res, payload := e.startRun("keep going for ever"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %#v", res.StatusCode, payload)
	}
	run := e.awaitRun("done", 90*time.Second, func(r map[string]any) bool { return r["done"] == true })
	if run["phase"] != phaseDone {
		t.Fatalf("a capped run is %#v", run)
	}
	if note, _ := run["note"].(string); !strings.Contains(note, "step limit of 2") {
		t.Fatalf("note = %q", note)
	}
	if step, _ := run["step"].(float64); step != 2 {
		t.Fatalf("the run took %v steps", run["step"])
	}
	if calls := e.gw.count(); calls != 2 {
		t.Fatalf("the model was asked %d times, want two", calls)
	}
}

// Prose instead of an object is worth exactly one more try, with the parser's
// complaint attached. A model that cannot answer with JSON twice will not
// answer with JSON on the fifth attempt either, and every attempt is a
// screenful of tokens.
func TestAgentAbortsAfterASecondUnusableAnswer(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("I would press Enter now, I think.")

	if res, payload := e.startRun("do the thing"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %#v", res.StatusCode, payload)
	}
	run := e.awaitRun("failed", 60*time.Second, func(r map[string]any) bool { return r["done"] == true })
	if run["phase"] != phaseError {
		t.Fatalf("run = %#v", run)
	}
	if failure, _ := run["error"].(string); failure != errAgentJSON.Error() {
		t.Fatalf("error = %q", failure)
	}
	if calls := e.gw.count(); calls != 2 {
		t.Fatalf("the model was asked %d times, want one retry and no more", calls)
	}
	if seen := e.gw.seen(); !strings.Contains(seen[1].joined(), "not usable JSON") {
		t.Fatalf("the retry did not carry the complaint:\n%s", seen[1].joined())
	}
}

// A model id nothing validated on the way in is found out here, and the run
// has to say so in words rather than dying of a 500 somewhere.
func TestAgentReportsAnUnknownModel(t *testing.T) {
	e := newAssistEnv(t)
	e.configureOpenRouter(t, map[string]any{"agent_model": "nobody/nothing"})
	e.gw.refuse(http.StatusNotFound, "nobody/nothing is not a valid model ID")

	if res, payload := e.startRun("do the thing"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %#v", res.StatusCode, payload)
	}
	run := e.awaitRun("failed", 60*time.Second, func(r map[string]any) bool { return r["done"] == true })
	failure, _ := run["error"].(string)
	if !strings.Contains(failure, "unknown model nobody/nothing") || !strings.Contains(failure, "/admin") {
		t.Fatalf("error = %q", failure)
	}
}

// The one policy lever: with it off the operator may not drive a bare shell,
// which is the harness with no permission prompt of its own.
func TestAgentRefusesAShellWhenTheLeverIsOff(t *testing.T) {
	e := newAssistEnv(t)
	e.configureAgent(map[string]any{"allow_shell": false})

	res, payload := e.startRun("rm everything")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a shell run = %d %#v", res.StatusCode, payload)
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, "shell") ||
		!strings.Contains(message, "/admin") {
		t.Fatalf("error = %q", message)
	}
	if calls := e.gw.count(); calls != 0 {
		t.Fatalf("a refused run still called the model %d times", calls)
	}

	// The switch back on is the whole difference.
	e.configureAgent(map[string]any{"allow_shell": true})
	e.gw.always(`{"actions":[],"done":true,"summary":"nothing needed doing"}`)
	if res, payload := e.startRun("look around"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("with the lever on = %d %#v", res.StatusCode, payload)
	}
}

// A run cannot start without a key, and the refusal has to say where to put
// one - the alternative is a spinner that never becomes anything.
func TestAgentRefusesWithoutAKey(t *testing.T) {
	e := newAssistEnv(t)
	e.configureOpenRouter(t, map[string]any{"api_key": ""})

	res, payload := e.startRun("do the thing")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("start without a key = %d %#v", res.StatusCode, payload)
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, "/admin") {
		t.Fatalf("error = %q", message)
	}

	// An empty prompt is the other refusal on that route.
	if res, _ := e.do(t, e.client, "POST", "/api/sessions/"+e.id+"/agent", `{"prompt":"   "}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an empty goal = %d", res.StatusCode)
	}
}

/* ----------------------------------------------------------- the guards */

// The guards are what make an operator loop something to leave running: they
// are all here, in one pure function, so they can be read in one go.
func TestPlanActionsGuards(t *testing.T) {
	names := func(plan []plannedKey) []string {
		out := make([]string, 0, len(plan))
		for _, p := range plan {
			switch {
			case p.key.Text != "":
				out = append(out, "text:"+p.key.Text)
			case p.key.Name != "":
				out = append(out, p.key.Name)
			default:
				out = append(out, "wait:"+p.key.Wait.String())
			}
		}
		return out
	}

	t.Run("a second interrupt in a row is dropped", func(t *testing.T) {
		interrupts, last := 0, false
		plan, notes := planActions([]agentAction{{Key: "C-c"}, {Key: "Ctrl-C"}}, config.HarnessClaude, &interrupts, &last)
		if got := names(plan); len(got) != 1 || got[0] != "C-c" {
			t.Fatalf("plan = %v, want one interrupt", got)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "dropped") {
			t.Fatalf("notes = %v", notes)
		}
		if interrupts != 1 {
			t.Fatalf("interrupts = %d", interrupts)
		}
		// And the counter survives the step: the third one anywhere in a run
		// is what ends it.
		plan, _ = planActions([]agentAction{{Key: "Enter"}, {Key: "C-c"}}, config.HarnessClaude, &interrupts, &last)
		if got := names(plan); len(got) != 2 {
			t.Fatalf("plan = %v", got)
		}
		if interrupts != 2 {
			t.Fatalf("interrupts = %d", interrupts)
		}
	})

	t.Run("C-d never reaches a shell", func(t *testing.T) {
		interrupts, last := 0, false
		plan, notes := planActions([]agentAction{{Key: "C-d"}}, config.HarnessShell, &interrupts, &last)
		if len(plan) != 0 || len(notes) != 1 {
			t.Fatalf("plan = %v, notes = %v", names(plan), notes)
		}
		// On a harness where it means "end of input" it is an ordinary key.
		plan, _ = planActions([]agentAction{{Key: "C-d"}}, config.HarnessClaude, &interrupts, &last)
		if got := names(plan); len(got) != 1 || got[0] != "C-d" {
			t.Fatalf("plan = %v", got)
		}
	})

	t.Run("a newline never submits by itself", func(t *testing.T) {
		interrupts, last := 0, false
		plan, notes := planActions([]agentAction{{Text: "first\nsecond"}}, config.HarnessCodex, &interrupts, &last)
		if got := names(plan); len(got) != 1 || got[0] != "text:first second" {
			t.Fatalf("plan = %v", got)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "Enter") {
			t.Fatalf("notes = %v", notes)
		}
	})

	t.Run("text is bounded", func(t *testing.T) {
		interrupts, last := 0, false
		plan, notes := planActions([]agentAction{{Text: strings.Repeat("x", agentMaxTextRunes+50)}},
			config.HarnessCodex, &interrupts, &last)
		if len([]rune(plan[0].key.Text)) != agentMaxTextRunes {
			t.Fatalf("text is %d runes", len([]rune(plan[0].key.Text)))
		}
		if len(notes) != 1 {
			t.Fatalf("notes = %v", notes)
		}
	})

	t.Run("a step is bounded", func(t *testing.T) {
		interrupts, last := 0, false
		var many []agentAction
		for i := 0; i < agentMaxActions+4; i++ {
			many = append(many, agentAction{Key: "Down"})
		}
		plan, notes := planActions(many, config.HarnessOpenCode, &interrupts, &last)
		if len(plan) != agentMaxActions {
			t.Fatalf("plan has %d actions", len(plan))
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "first 8") {
			t.Fatalf("notes = %v", notes)
		}
	})

	t.Run("a wait is bounded and an unknown key is refused", func(t *testing.T) {
		interrupts, last := 0, false
		plan, notes := planActions([]agentAction{
			{WaitMS: 60000}, {Key: "F13"}, {Key: "pgdn"}, {},
		}, config.HarnessCodex, &interrupts, &last)
		got := names(plan)
		if len(got) != 2 || got[0] != "wait:"+termux.MaxKeyWait.String() || got[1] != "PageDown" {
			t.Fatalf("plan = %v", got)
		}
		if len(notes) != 3 {
			t.Fatalf("notes = %v", notes)
		}
	})

	t.Run("every key in the vocabulary is a tmux key name", func(t *testing.T) {
		for _, name := range agentKeyVocabulary {
			if got, ok := agentKeyName(strings.ToUpper(name)); !ok || got != name {
				t.Fatalf("%q came back as %q/%v", name, got, ok)
			}
		}
	})
}

// The fence a chat model puts round its answer, and the prose it puts in front
// of it, are two different things: the first is worth stripping, the second is
// worth a retry.
func TestParseDecision(t *testing.T) {
	decision, err := parseDecision("```json\n{\"actions\":[{\"key\":\"Enter\"}],\"done\":true,\"summary\":\"ok\"}\n```")
	if err != nil {
		t.Fatalf("a fenced object: %v", err)
	}
	if !decision.Done || decision.Summary != "ok" || len(decision.Actions) != 1 {
		t.Fatalf("decision = %#v", decision)
	}
	if _, err := parseDecision("Sure, I would press Enter."); err == nil {
		t.Fatal("prose parsed as a decision")
	}
	if _, err := parseDecision("   "); err == nil {
		t.Fatal("an empty answer parsed as a decision")
	}
}
