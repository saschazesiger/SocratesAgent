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

// A question about a passage of a chat is answered by the status model from
// the chat's whole record, the passage and the question; a follow-up carries
// the dialog so far, and nothing of it reaches the chat.
func TestAQuestionAboutAPassageIsAnsweredFromTheRecord(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("**echo** prints its arguments.")
	id := c.create(t)
	ask := func(body string) (*http.Response, map[string]any) {
		return c.do(t, c.client, "POST", "/api/sessions/"+id+"/ask", body)
	}

	// An empty chat has nothing to be asked about, and nothing is asked.
	res, body := ask(`{"quote":"echo","question":"What is that?"}`)
	if res.StatusCode != http.StatusBadRequest || len(statusCalls(gw)) != 0 {
		t.Fatalf("an empty chat: %d %v", res.StatusCode, body)
	}
	c.send(t, id, "m1", "/tool")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	c.waitActivity(t, id, termux.StateIdle)
	events := len(c.journal(t, id))

	// A question needs a passage and words.
	for _, bad := range []string{`{"quote":"echo","question":"  "}`, `{"quote":"","question":"What?"}`} {
		if res, body := ask(bad); res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", bad, res.StatusCode, body)
		}
	}
	if len(statusCalls(gw)) != 0 {
		t.Fatal("an incomplete question reached the model")
	}

	res, body = ask(`{"quote":"Running echo hello","question":"Was bedeutet das?"}`)
	if res.StatusCode != http.StatusOK || body["answer"] != "**echo** prints its arguments." ||
		body["model"] != "test/status-model" {
		t.Fatalf("the answer: %d %v", res.StatusCode, body)
	}
	call := statusCalls(gw)[0]
	if len(call.Messages) != 2 || call.Messages[0].Role != "system" ||
		!strings.HasPrefix(call.Messages[0].Content, askMarker) {
		t.Fatalf("not asked with its instruction: %#v", call.Messages)
	}
	prompt := call.last()
	for _, want := range []string{"A chat with Scripted agent", "Record:", "] Person: /tool",
		"] Call: Running echo hello", "<<<\nRunning echo hello\n>>>", "Their question: Was bedeutet das?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the prompt has no %q:\n%s", want, prompt)
		}
	}

	// A follow-up is the dialog so far: the chat once, at the start.
	res, body = ask(`{"quote":"Running echo hello","question":"Und warum?",` +
		`"thread":[{"question":"Was bedeutet das?","answer":"It prints."},{"question":"","answer":"dropped"}]}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a follow-up: %d %v", res.StatusCode, body)
	}
	call = statusCalls(gw)[1]
	roles := make([]string, 0, len(call.Messages))
	for _, m := range call.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" ||
		!strings.Contains(call.Messages[1].Content, "Their question: Was bedeutet das?") ||
		call.Messages[2].Content != "It prints." || call.Messages[3].Content != "Und warum?" {
		t.Fatalf("the follow-up's messages: %#v", call.Messages)
	}

	// Nothing of it reached the chat.
	if got := len(c.journal(t, id)); got != events {
		t.Fatalf("the journal has %d events after the questions, want %d", got, events)
	}

	// A refusal is said in the server's words.
	gw.refuse(http.StatusBadRequest, "no such model")
	res, body = ask(`{"quote":"echo","question":"What?"}`)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "nknown model test/status-model") {
		t.Fatalf("a refused question: %d %v", res.StatusCode, body)
	}
}

// A long selection keeps its start and its end.
func TestALongPassageKeepsItsEnds(t *testing.T) {
	long := "START " + strings.Repeat("x", 3*askQuoteRunes) + " END"
	got := clipAsk(long, askQuoteRunes, true)
	if !strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "END") || !strings.Contains(got, "[…]") ||
		len([]rune(got)) > askQuoteRunes+8 {
		t.Fatalf("a long passage: %d runes, %q … %q", len([]rune(got)), got[:12], got[len(got)-12:])
	}
	if clipAsk("short", 10, true) != "short" {
		t.Fatal("a short passage was changed")
	}
}
