//go:build linux

package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The Status of a chat is an analysis of its whole journal: the record with
// every message and call and its time, and the measured facts about its
// time, through the same model and voice path as a terminal's. The page gets
// the parts of the answer and the server's own clock.
func TestAChatsStatusIsToldFromItsJournal(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always(`{"summary":"It said hello.","now":"It is done.","eta":"Nothing is running.",` +
		`"eta_minutes":null,"spoken":"It ran **echo** and said hello."}`)
	id := c.create(t)
	res, body := c.do(t, c.client, "POST", "/api/sessions/"+id+"/status", "")
	if res.StatusCode != http.StatusBadRequest || len(statusCalls(gw)) != 0 {
		t.Fatalf("a chat with nothing in it: %d %v", res.StatusCode, body)
	}
	c.send(t, id, "m1", "/tool")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	c.waitActivity(t, id, termux.StateIdle)
	events := c.dialEvents(t)
	res, body = c.do(t, c.client, "POST", "/api/sessions/"+id+"/status", "")
	if res.StatusCode != http.StatusOK || body["text"] != "It ran echo and said hello." || body["state"] != "idle" {
		t.Fatalf("status: %d %v", res.StatusCode, body)
	}
	analysis, _ := body["analysis"].(map[string]any)
	if analysis["summary"] != "It said hello." || analysis["now"] != "It is done." ||
		analysis["eta"] != "Nothing is running." || analysis["eta_minutes"] != nil {
		t.Fatalf("the analysis: %v", body["analysis"])
	}
	clock, _ := body["clock"].(map[string]any)
	if clock["turns"] != float64(1) || clock["first_message"].(float64) <= 0 || clock["last_ended"].(float64) <= 0 ||
		clock["turn_started"] != float64(0) {
		t.Fatalf("the clock: %v", body["clock"])
	}
	// A chat has no terminal socket: its phases reach the page on the event
	// stream, in order, the answer last.
	var phases []string
	events.await("the phases of the chat's status", 5*time.Second, func() bool {
		phases = phases[:0]
		for _, frame := range events.ctrl {
			if frame["t"] == "status" && frame["id"] == id {
				phases = append(phases, frame["phase"].(string))
			}
		}
		return len(phases) >= 3
	})
	if strings.Join(phases, ",") != "asking,speaking,done" {
		t.Fatalf("the phases on the event stream: %v", phases)
	}
	call := statusCalls(gw)[0]
	if len(call.Messages) != 2 || !strings.HasPrefix(call.Messages[0].Content, statusMarker) {
		t.Fatalf("the analysis is not asked with its instruction: %#v", call.Messages)
	}
	prompt := call.last()
	for _, want := range []string{"A chat with Scripted agent", "Facts:", "Turns so far: 1", "State: idle",
		"The last turn ended:", "Record:", "] Person: /tool", "] Call: Running echo hello", "--- Turn 1 started",
		"--- Turn 1 ended after"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the status prompt has no %q:\n%s", want, prompt)
		}
	}

	c.send(t, id, "m2", "/ask")
	c.waitActivity(t, id, termux.StateWaiting)
	res, body = c.do(t, c.client, "POST", "/api/sessions/"+id+"/status", "")
	if res.StatusCode != http.StatusOK || body["state"] != "waiting" {
		t.Fatalf("status while it asks: %d %v", res.StatusCode, body)
	}
	prompt = statusCalls(gw)[1].last()
	for _, want := range []string{"It has waited for the person's answer since:", "Agent asked the person: Pick one (A, B)",
		"The current turn started:", "Turns so far: 2"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the status prompt while it asks has no %q:\n%s", want, prompt)
		}
	}

	// An answer that is not the object is asked for once more, and then
	// said to be what it is.
	gw.always("Just prose.")
	res, body = c.do(t, c.client, "POST", "/api/sessions/"+id+"/status", "")
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(fmt.Sprint(body["error"]), "usable JSON") {
		t.Fatalf("a model that will not answer with JSON: %d %v", res.StatusCode, body)
	}
}

// statusCalls is every call the Status button made: the namer's go to the
// title model.
func statusCalls(gw *scriptedGateway) []gatewayCall {
	var out []gatewayCall
	for _, call := range gw.seen() {
		if call.Model == "test/status-model" {
			out = append(out, call)
		}
	}
	return out
}
