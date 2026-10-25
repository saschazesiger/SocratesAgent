//go:build linux

package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// callsWith is every call whose instruction opens with marker.
func callsWith(gw *scriptedGateway, marker string) []gatewayCall {
	var out []gatewayCall
	for _, call := range gw.seen() {
		if len(call.Messages) > 0 && strings.HasPrefix(call.Messages[0].Content, marker) {
			out = append(out, call)
		}
	}
	return out
}

// An answer is read out loud as it stands when it is short and plain, and
// said for the ear by the model when it is not; the second press is no new
// call.
func TestAnAnswerIsSaidForTheEar(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("It changed two files and the tests pass.")
	id := c.create(t)
	c.send(t, id, "m1", "hello")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	c.send(t, id, "m2", "/md")
	c.waitJournal(t, id, 10*time.Second, "the second turn", turnsEnded(2))
	var answers []string
	p, err := c.srv.chats.peer(id)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.view(func(proj *agent.Projection) {
		for _, m := range proj.Messages {
			if m.Role == agent.RoleAssistant {
				answers = append(answers, m.ID)
			}
		}
	})
	if len(answers) != 2 {
		t.Fatalf("answers: %v", answers)
	}
	res, body := c.do(t, c.client, "POST", "/api/chats/"+id+"/spoken", `{"message_id":"`+answers[0]+`"}`)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(fmt.Sprint(body["text"]), "you said: hello") ||
		len(callsWith(gw, spokenMarker)) != 0 {
		t.Fatalf("a short plain answer: %d %v", res.StatusCode, body)
	}
	for i := 0; i < 2; i++ {
		res, body = c.do(t, c.client, "POST", "/api/chats/"+id+"/spoken", `{"message_id":"`+answers[1]+`"}`)
		if res.StatusCode != http.StatusOK || body["text"] != "It changed two files and the tests pass." {
			t.Fatalf("a Markdown answer: %d %v", res.StatusCode, body)
		}
	}
	if n := len(callsWith(gw, spokenMarker)); n != 1 {
		t.Fatalf("two presses made %d calls", n)
	}
	if res, _ := c.do(t, c.client, "POST", "/api/chats/"+id+"/spoken", `{"message_id":"nope"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an answer that is not there: %d", res.StatusCode)
	}
}
