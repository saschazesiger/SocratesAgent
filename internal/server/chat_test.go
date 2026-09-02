//go:build !windows

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The chat beside the terminal, against a real pane and a stand in gateway:
// what it stores, what it broadcasts, what it shows the model, and the one
// decision the model is allowed to make about the keyboard.

// chatFramesLocked is every chat message this socket has heard, oldest first.
// It is only ever called from inside a `cond`, which `await` already runs
// under the client's lock - taking it again here would deadlock the reader.
func (c *wsClient) chatFramesLocked() []map[string]any {
	var out []map[string]any
	for _, frame := range c.ctrl {
		if frame["t"] != "chat" {
			continue
		}
		if msg, _ := frame["msg"].(map[string]any); msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

// awaitChat waits for the conversation on the socket to satisfy a condition.
func (c *wsClient) awaitChat(what string, cond func([]map[string]any) bool) []map[string]any {
	c.t.Helper()
	var found []map[string]any
	c.await("a chat that is "+what, 60*time.Second, func() bool {
		found = c.chatFramesLocked()
		return cond(found)
	})
	return found
}

// say posts one message the way the panel does.
func (e *assistEnv) say(text string) (*http.Response, map[string]any) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"text": text})
	return e.do(e.t, e.client, "POST", "/api/sessions/"+e.id+"/chat", string(body))
}

// chatHistory is what a browser with no socket reads on attach.
func (e *assistEnv) chatHistory() []map[string]any {
	e.t.Helper()
	res, payload := e.do(e.t, e.client, "GET", "/api/sessions/"+e.id+"/chat", "")
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("GET chat: %d %#v", res.StatusCode, payload)
	}
	raw, _ := payload["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if msg, _ := item.(map[string]any); msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

func (e *assistEnv) awaitHistory(what string, within time.Duration, cond func([]map[string]any) bool) []map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(within)
	var last []map[string]any
	for time.Now().Before(deadline) {
		last = e.chatHistory()
		if cond(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("the conversation never became %s; it is %#v", what, last)
	return nil
}

func texts(messages []map[string]any) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		out = append(out, fmt.Sprintf("%v:%v", msg["role"], msg["text"]))
	}
	return out
}

/* --------------------------------------------------------- a plain answer */

// A question that is a question: the model answers in words, the answer is
// stored, and nothing was typed into the pane.
func TestChatAnswersInWords(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always(`{"reply":"It is sitting at a prompt with nothing to do.","act":null}`)

	e.typeLine(e.id, "echo chat-probe-4times")
	e.waitForPane(e.id, "chat-probe-4times")

	socket := e.dialWS(e.id, "viewer=tab-chat&cols=100&rows=30")
	socket.hello()

	res, payload := e.say("what is it doing?")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("chat: %d %#v", res.StatusCode, payload)
	}

	got := socket.awaitChat("a question and an answer", func(msgs []map[string]any) bool {
		return len(msgs) >= 2
	})
	if got[0]["role"] != "user" || got[0]["text"] != "what is it doing?" {
		t.Fatalf("the first frame is %#v", got[0])
	}
	if got[1]["role"] != "assistant" ||
		!strings.Contains(fmt.Sprint(got[1]["text"]), "nothing to do") {
		t.Fatalf("the answer is %#v", got[1])
	}
	if _, acted := got[1]["run_id"]; acted {
		t.Fatalf("a question started a run: %#v", got[1])
	}

	// It is persisted, so a phone that reloads sees what was said.
	stored := e.chatHistory()
	if len(stored) != 2 || stored[0]["role"] != "user" || stored[1]["role"] != "assistant" {
		t.Fatalf("the stored conversation is %#v", texts(stored))
	}

	// The model saw the screen, was told what the pane runs, and was given the
	// rule about when it may act.
	calls := e.gw.seen()
	if len(calls) != 1 {
		t.Fatalf("the gateway saw %d calls", len(calls))
	}
	if calls[0].Model != "test/agent-model" {
		t.Fatalf("the request named %q", calls[0].Model)
	}
	if calls[0].MaxTokens != chatMaxTokens {
		t.Fatalf("max_tokens = %d", calls[0].MaxTokens)
	}
	system := calls[0].Messages[0].Content
	if !strings.Contains(system, "chat-probe-4times") {
		t.Fatalf("the model was not shown the screen:\n%s", system)
	}
	if !strings.Contains(system, "Shell") || !strings.Contains(system, `"act"`) {
		t.Fatalf("the system prompt is missing its subject or its rule:\n%s", system)
	}
	if last := calls[0].last(); last != "what is it doing?" {
		t.Fatalf("the question reached the model as %q", last)
	}
	if role := calls[0].Messages[len(calls[0].Messages)-1].Role; role != "user" {
		t.Fatalf("the question was sent as %q", role)
	}
}

// An answer that is not the object it was asked for is still an answer. A
// model that wrote prose answered the question, and throwing that away would
// make the panel worse exactly when the model is being unhelpful.
func TestChatTakesProseAsAnAnswer(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always("The build finished and the tests passed.")

	if res, payload := e.say("how did it go?"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("chat: %d %#v", res.StatusCode, payload)
	}
	stored := e.awaitHistory("answered", 30*time.Second, func(msgs []map[string]any) bool {
		return len(msgs) >= 2
	})
	if !strings.Contains(fmt.Sprint(stored[1]["text"]), "tests passed") {
		t.Fatalf("the answer is %#v", stored[1])
	}
	if stored[1]["failed"] == true {
		t.Fatalf("prose was treated as a failure: %#v", stored[1])
	}
}

/* ------------------------------------------------------- an answer that acts */

// A request that needs the terminal: the model says so with `act`, the
// operator run starts, the keys land in the pane, and the run's ending is a
// message in the conversation that asked for it.
func TestChatActsOnTheTerminalWhenItIsAsked(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.script(
		`{"reply":"Making the file now.","act":"create a file called chat-operator-ran"}`,
		`{"actions":[{"text":"touch chat-operator-ran"},{"key":"Enter"}],"done":false,"summary":"typing it"}`,
		`{"actions":[],"done":true,"summary":"the file was made"}`,
	)
	e.gw.always(`{"actions":[],"done":true,"summary":"the file was made"}`)

	socket := e.dialWS(e.id, "viewer=tab-act&cols=100&rows=30")
	socket.hello()

	if res, payload := e.say("make a file called chat-operator-ran"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("chat: %d %#v", res.StatusCode, payload)
	}

	// The reply carries the run, which is what makes the bubble the place the
	// Cancel button lives.
	got := socket.awaitChat("a reply that started a run", func(msgs []map[string]any) bool {
		for _, msg := range msgs {
			if msg["role"] == "assistant" && msg["run_id"] != nil && msg["run_id"] != "" {
				return true
			}
		}
		return false
	})
	var runID string
	for _, msg := range got {
		if id, _ := msg["run_id"].(string); id != "" {
			runID = id
		}
	}
	if runID == "" {
		t.Fatalf("no run id in %#v", texts(got))
	}
	if run := e.runOf(); run == nil || run["run_id"] != runID {
		t.Fatalf("GET .../agent does not know the chat's run: %#v", run)
	}

	// The keys reached the real pane.
	e.waitForPane(e.id, "chat-operator-ran")

	// And the ending is in the conversation, not only in a frame.
	stored := e.awaitHistory("finished", 90*time.Second, func(msgs []map[string]any) bool {
		for _, msg := range msgs {
			if msg["role"] == "assistant" && strings.Contains(fmt.Sprint(msg["text"]), "the file was made") {
				return true
			}
		}
		return false
	})
	if len(stored) < 3 {
		t.Fatalf("the conversation is %#v", texts(stored))
	}
}

// A run that cannot be started says so in the conversation rather than
// vanishing: the reply is kept, and the reason follows it.
func TestChatSaysWhyARunWasRefused(t *testing.T) {
	e := newAssistEnv(t)
	e.configureAgent(map[string]any{"allow_shell": false})
	e.gw.always(`{"reply":"I will run it.","act":"run the tests"}`)

	if res, payload := e.say("run the tests"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("chat: %d %#v", res.StatusCode, payload)
	}
	stored := e.awaitHistory("refused", 30*time.Second, func(msgs []map[string]any) bool {
		for _, msg := range msgs {
			if msg["failed"] == true {
				return true
			}
		}
		return false
	})
	joined := strings.Join(texts(stored), " | ")
	if !strings.Contains(joined, "/admin") {
		t.Fatalf("the refusal does not say where to go: %s", joined)
	}
	if e.runOf() != nil {
		t.Fatalf("a refused request still started a run: %#v", e.runOf())
	}
}

/* ---------------------------------------------------------- the housekeeping */

// The conversation is a chat beside a terminal, not a transcript. It is capped,
// and the cap keeps the newest.
func TestChatKeepsOnlyTheLastMessages(t *testing.T) {
	e := newAssistEnv(t)
	// Straight into the store, because the cap is about the store and driving
	// sixty model calls through a pane would be a test about tmux.
	seed := make([]chatMessage, 0, chatKeepMessages+10)
	for i := 0; i < chatKeepMessages+10; i++ {
		seed = append(seed, chatMessage{Role: "user", Text: fmt.Sprintf("old-%d", i), TS: int64(i + 1)})
	}
	if err := e.srv.store.SetJSON(chatKey(e.id), seed); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	e.gw.always(`{"reply":"noted","act":null}`)
	if res, _ := e.say("the newest question"); res.StatusCode != http.StatusAccepted {
		t.Fatal("chat was refused")
	}
	stored := e.awaitHistory("capped", 30*time.Second, func(msgs []map[string]any) bool {
		return len(msgs) == chatKeepMessages && fmt.Sprint(msgs[len(msgs)-1]["role"]) == "assistant"
	})
	if first := fmt.Sprint(stored[0]["text"]); strings.HasSuffix(first, "old-0") {
		t.Fatalf("the cap dropped the newest instead of the oldest: %q", first)
	}
	if !strings.Contains(fmt.Sprint(stored[len(stored)-2]["text"]), "the newest question") {
		t.Fatalf("the question is not in the tail: %#v", texts(stored[len(stored)-3:]))
	}
	// And the model is shown only the tail of it.
	call := e.gw.seen()[0]
	if len(call.Messages) > chatHistoryMessages+1 {
		t.Fatalf("the model was shown %d messages", len(call.Messages))
	}
}

// Deleting a session takes its conversation with it. A key/value row about a
// screen that no longer exists is litter that outlives everything else.
func TestChatIsDeletedWithItsSession(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always(`{"reply":"noted","act":null}`)
	if res, _ := e.say("remember this"); res.StatusCode != http.StatusAccepted {
		t.Fatal("chat was refused")
	}
	e.awaitHistory("answered", 30*time.Second, func(msgs []map[string]any) bool { return len(msgs) >= 2 })

	if res, payload := e.do(t, e.client, "DELETE", "/api/sessions/"+e.id, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %#v", res.StatusCode, payload)
	}
	if _, err := e.srv.store.GetKV(chatKey(e.id)); err == nil {
		t.Fatal("the conversation outlived the session it was about")
	}
}

// No key is the one thing this route refuses outright, and it refuses with the
// sentence that says what to do about it.
func TestChatRefusesWithoutAKey(t *testing.T) {
	e := newAssistEnv(t)
	e.configureOpenRouter(t, map[string]any{"api_key": ""})
	res, payload := e.say("what is it doing?")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("chat without a key = %d %#v", res.StatusCode, payload)
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "/admin") || !strings.Contains(message, "agent model") {
		t.Fatalf("error = %q", message)
	}
	if len(e.chatHistory()) != 0 {
		t.Fatalf("a refused message was stored anyway: %#v", e.chatHistory())
	}
}

// An empty message is not a message.
func TestChatRefusesNothing(t *testing.T) {
	e := newAssistEnv(t)
	if res, _ := e.say("   "); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an empty message = %d", res.StatusCode)
	}
}

// A gateway that will not answer is an answer too: the reason is stored, so
// the phone that reloads finds out why nothing came back.
func TestChatStoresTheReasonThereIsNoAnswer(t *testing.T) {
	e := newAssistEnv(t)
	e.configureOpenRouter(t, map[string]any{"agent_model": "nobody/nothing"})
	e.gw.refuse(http.StatusNotFound, "nobody/nothing is not a valid model ID")
	if res, _ := e.say("what is it doing?"); res.StatusCode != http.StatusAccepted {
		t.Fatal("chat was refused before it was tried")
	}
	stored := e.awaitHistory("failed", 30*time.Second, func(msgs []map[string]any) bool {
		return len(msgs) >= 2 && msgs[1]["failed"] == true
	})
	if message := fmt.Sprint(stored[1]["text"]); !strings.Contains(message, "unknown model nobody/nothing") {
		t.Fatalf("the reason is %q", message)
	}
}

// The conversation is in hello, so a reconnect draws the panel without asking
// for anything.
func TestHelloCarriesTheConversation(t *testing.T) {
	e := newAssistEnv(t)
	e.gw.always(`{"reply":"noted","act":null}`)
	if res, _ := e.say("remember this"); res.StatusCode != http.StatusAccepted {
		t.Fatal("chat was refused")
	}
	e.awaitHistory("answered", 30*time.Second, func(msgs []map[string]any) bool { return len(msgs) >= 2 })

	socket := e.dialWS(e.id, "viewer=tab-hello&cols=100&rows=30")
	hello := socket.hello()
	list, _ := hello["chat"].([]any)
	if len(list) < 2 {
		t.Fatalf("hello carried %#v for chat", hello["chat"])
	}
	first, _ := list[0].(map[string]any)
	if first == nil || first["text"] != "remember this" {
		t.Fatalf("the first message is %#v", list[0])
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
