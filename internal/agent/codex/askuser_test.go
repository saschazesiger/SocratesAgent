//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
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

// A chat's thread has the chat tools beside ask_user and both guidances; a
// test session's has its role's tools instead of both, its role's protocol,
// then the model's instructions and the catalog's, and nobody to ask.
func TestATestSessionsThreadHasItsRolesToolsInsteadOfAskUser(t *testing.T) {
	thread := func(ep chattools.Endpoint) (*adapter, *recorder, string) {
		lab := newFakeLab(t)
		r := newRecorder()
		spec := lab.spec("a", "")
		spec.ChatTools, spec.Instructions = ep, map[string]string{"gpt-fake": "NOTE-FAKE"}
		if ep.Test() {
			spec.TestInstructions = "CATALOG-NOTES"
		}
		a := lab.start(spec, r)
		_, n := send(t, a, r, "/thread")
		r.turnEnd(t, n, wait)
		return a, r, lastTurnText(r.projection())
	}
	sock := "/nonexistent/chat-tools.sock"
	_, _, got := thread(chattools.Endpoint{Socket: sock, Token: "c.x"})
	if !strings.Contains(got, askuser.Name) || !strings.Contains(got, chattools.List) ||
		!strings.Contains(got, "instructions: "+askGuidance+"\n\n"+chattools.Guidance("")) || strings.Contains(got, "CATALOG-NOTES") {
		t.Fatalf("a chat's thread:\n%s", got)
	}

	a, r, got := thread(chattools.Endpoint{Socket: sock, Token: "c.x", Kind: testtools.KindAnalyst})
	tools, instructions, _ := strings.Cut(got, "\n")
	for _, d := range testtools.Tools(testtools.KindAnalyst) {
		if !strings.Contains(tools, d.Name) {
			t.Errorf("the thread lacks %s: %s", d.Name, tools)
		}
	}
	if strings.Contains(tools, askuser.Name) || strings.Contains(tools, chattools.List) {
		t.Errorf("a test session's thread has ask_user or the chat tools: %s", tools)
	}
	want := "instructions: " + testtools.Guidance(testtools.KindAnalyst, "", false) + "\n\n" +
		agent.ModelPrompt("gpt-fake", "NOTE-FAKE") + "\n\nCATALOG-NOTES"
	if instructions != want {
		t.Errorf("developer instructions\n%q\nwant\n%q", instructions, want)
	}
	_, n := send(t, a, r, "/tool "+testtools.RunOverview+" {}")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "The test run could not be reached") {
		t.Errorf("a role's tool: %q", got)
	}
	_, n = send(t, a, r, "/tool ask_user")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "tool said: "+askuser.TestRefusal {
		t.Errorf("ask_user: %q", got)
	}
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			t.Fatal("a test session asked the person")
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

	// Asked again in the same turn, the questions are not asked twice: the
	// model is told they are already with the person.
	_, n = send(t, a, r, "/tooltwice ask_user")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "tool said: "+DeferredText+" "+AskedText {
		t.Fatalf("the model got %q", got)
	}
	asks := 0
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			asks++
		}
	}
	if asks != 1 {
		t.Fatalf("%d asks for the same questions in one turn", asks)
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

	// Interrupted while it waits, the request is gone as soon as codex took
	// the interrupt, before the turn's end has come.
	_, n = send(t, a, r, "/input")
	ask = r.waitFor(t, n, "the second ask", isType(agent.TypeAsk), wait)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("an answer after the interrupt: %v", err)
	}
	r.turnEnd(t, n, wait)
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

	// A question back has no place in the form: the form is declined, and
	// the answer is reported deferred, so that the host sends it as a
	// message the model reads.
	_, n = send(t, a, r, "/elicit")
	ask = r.waitFor(t, n, "the second ask", isType(agent.TypeAsk), wait)
	err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{},"follow_up":"Why staging?"}`))
	if !errors.Is(err, agent.ErrAskDeferred) {
		t.Fatalf("a question back: %v", err)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != `action: decline null` {
		t.Fatalf("the server got %q", got)
	}
}
