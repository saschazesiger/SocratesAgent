//go:build linux

package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The spoken Status of a chat is told from its journal: the last turn with
// its calls, and the question it waits on, through the same model and voice
// path as a terminal's.
func TestAChatsStatusIsToldFromItsJournal(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("It ran echo and said hello.")
	id := c.create(t)
	res, body := c.do(t, c.client, "POST", "/api/sessions/"+id+"/status", "")
	if res.StatusCode != http.StatusBadRequest || len(statusCalls(gw)) != 0 {
		t.Fatalf("a chat with nothing in it: %d %v", res.StatusCode, body)
	}
	c.send(t, id, "m1", "/tool")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	c.waitActivity(t, id, termux.StateIdle)
	res, body = c.do(t, c.client, "POST", "/api/sessions/"+id+"/status", "")
	if res.StatusCode != http.StatusOK || body["text"] != "It ran echo and said hello." || body["state"] != "idle" {
		t.Fatalf("status: %d %v", res.StatusCode, body)
	}
	prompt := statusCalls(gw)[0].last()
	for _, want := range []string{"a chat with Scripted agent", "It is idle", "The chat:", "/tool", "- Running echo hello"} {
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
	if prompt := statusCalls(gw)[1].last(); !strings.Contains(prompt, "It waits for the person to answer:\n- Pick one (A, B)") {
		t.Fatalf("the status prompt while it asks:\n%s", prompt)
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
