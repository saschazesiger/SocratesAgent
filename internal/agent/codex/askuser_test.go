//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// ask_user is a dynamic tool of every thread: a call is asked as the ask of
// its turn, drawn with its tool part, and the model gets §5.2 back.
func TestAskUserIsADynamicToolThatWaits(t *testing.T) {
	if !strings.Contains(string(askToolSpec()), `"name":"ask_user"`) || !strings.Contains(string(askToolSpec()), `"maxItems":20`) {
		t.Fatalf("spec %s", askToolSpec())
	}
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/tool ask_user")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), wait)
	p := askuser.Decode(ask.Payload)
	if p == nil || len(p.Questions) != 2 || ask.Via != "" || ask.TurnID == "" || p.PartID == "" {
		t.Fatalf("ask %+v", ask)
	}
	var tool *agent.Part
	for _, pt := range partsOf(r.projection()) {
		if pt.ID == p.PartID {
			tool = pt
		}
	}
	if tool == nil || tool.Kind != agent.PartTool || tool.State != agent.PartRunning {
		t.Fatalf("the ask's part %+v", tool)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{"db":{"selected":["pg"]},"name":{"delegated":true}}}`)); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	got := lastTurnText(r.projection())
	if !strings.HasPrefix(got, `tool said: {"answers":{"db":{"selected":["pg"]`) || !strings.Contains(got, `"delegated":true`) ||
		!strings.Contains(got, "decide them yourself") {
		t.Fatalf("the model got %q", got)
	}

	// A call the schema does not allow goes back to the model.
	_, n = send(t, a, r, `/tool ask_user {"questions":[]}`)
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "ask_user was called with problems") {
		t.Fatalf("a bad call: %q", got)
	}
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			t.Fatal("a bad call was asked")
		}
	}
}

// A code-mode model would poll a blocking tool for as long as the person
// takes, so there the call is delivered at once and the answer starts the
// next turn.
func TestAskUserOnACodeModeModelIsAnsweredAsTheNextMessage(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	spec := lab.spec("a", "")
	spec.Model = "gpt-6-astra"
	a := lab.start(spec, r)
	_, n := send(t, a, r, "/tool ask_user")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), wait)
	if ask.Via != agent.ViaMessage {
		t.Fatalf("ask %+v", ask)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "tool said: "+DeferredText {
		t.Fatalf("the model got %q", got)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("a deferred ask answered through the tool: %v", err)
	}
}

// Plan mode's request_user_input is asked as the same payload and answered
// in its own shape.
func TestRequestUserInputIsAskedAndAnswered(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/input")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), wait)
	p := askuser.Decode(ask.Payload)
	if p == nil || p.Source != askuser.SourceUserInput || p.Questions[0].ID != "color" || !p.Questions[0].Options[0].Recommended ||
		p.Questions[1].AllowOther {
		t.Fatalf("payload %s", ask.Payload)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{"color":{"selected":["Red"]},"size":{"selected":["Large"],"note":"really"}}}`)); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != `answers: {"answers":{"color":{"answers":["Red"]},"size":{"answers":["Large","Note: really"]}}}` {
		t.Fatalf("codex got %q", got)
	}

	// Interrupted while it waits, the request is gone with its turn.
	_, n = send(t, a, r, "/input")
	ask = r.waitFor(t, n, "the second ask", isType(agent.TypeAsk), wait)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	waitGone(t, a, ask.AskID)
}

// waitGone waits until an ask's call is no longer there.
func waitGone(t *testing.T, a *adapter, askID string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		a.mu.Lock()
		_, held := a.asks[askID]
		a.mu.Unlock()
		if !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ask outlived its turn")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := a.Answer(context.Background(), askID, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("an answer to a call that is gone: %v", err)
	}
}

// The questions of an async agent message are an ask answered as the next
// message; the turn goes on meanwhile.
func TestAsyncQuestionsAreAnsweredAsTheNextMessage(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/async")
	r.turnEnd(t, n, wait)
	var asks []agent.Event
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			asks = append(asks, ev)
		}
	}
	if len(asks) != 1 || asks[0].Via != agent.ViaMessage {
		t.Fatalf("asks %+v", asks)
	}
	p := askuser.Decode(asks[0].Payload)
	if p.Source != askuser.SourceAsync || p.Questions[0].Options[0].Label != "Yes" || p.Questions[1].Kind != askuser.KindText {
		t.Fatalf("payload %s", asks[0].Payload)
	}
	if got := lastTurnText(r.projection()); !strings.HasSuffix(got, "asked") {
		t.Fatalf("the turn did not go on: %q", got)
	}
}

// An MCP elicitation form is asked as one question per field and answered
// with the values typed as the form says.
func TestAnElicitationFormIsAsked(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/elicit")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), wait)
	p := askuser.Decode(ask.Payload)
	if p == nil || p.Source != askuser.SourceElicitation || p.Title != "Deploy now?" || len(p.Questions) != 2 {
		t.Fatalf("payload %s", ask.Payload)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{"target":{"selected":["staging"]},"notes":{"other":"after lunch"}}}`)); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != `action: accept {"notes":"after lunch","target":"staging"}` {
		t.Fatalf("the server got %q", got)
	}
}
