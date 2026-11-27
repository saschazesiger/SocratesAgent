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
)

// ask_user is a dynamic tool of every thread: a call is put as an ask of
// its turn, drawn with its tool part, whose answer goes as a message; the
// call returns the question's id at once.
func TestAskUserIsADynamicToolThatReturnsAtOnce(t *testing.T) {
	if !strings.Contains(string(askToolSpec()), `"name":"ask_user"`) || !strings.Contains(string(askToolSpec()), `"required":["question","blocks"]`) {
		t.Fatalf("spec %s", askToolSpec())
	}
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	_, n := send(t, a, r, "/tool ask_user")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), wait)
	p := askuser.Decode(ask.Payload)
	if p == nil || len(p.Questions) != 1 || p.Blocks != "The schema" || ask.Via != agent.ViaMessage || ask.TurnID == "" || p.PartID == "" {
		t.Fatalf("ask %+v", ask)
	}
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); got != "tool said: "+askuser.QueuedText(ask.AskID, p) {
		t.Fatalf("the model got %q", got)
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{}`)); !errors.Is(err, agent.ErrAskGone) {
		t.Fatalf("an ask answered through the tool: %v", err)
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

	// A subagent thread's call asks nothing.
	_, n = send(t, a, r, "/subask")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "only the main agent can ask the person") {
		t.Fatalf("a subagent's call: %q", got)
	}
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			t.Fatal("a subagent's call was asked")
		}
	}
}

// check_questions and withdraw_question are dynamic tools that reach the
// host's questions.
func TestTheQuestionToolsAreDynamicTools(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	spec := lab.spec("a", "")
	q := &fakeQuestions{}
	spec.Questions = q
	a := lab.start(spec, r)
	_, n := send(t, a, r, `/tool withdraw_question {"question_id":"ask_00000001","reason":"found it"}`)
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "is withdrawn") || q.withdrawn != "ask_00000001:found it" {
		t.Fatalf("withdraw: %q %q", got, q.withdrawn)
	}
	_, n = send(t, a, r, "/tool check_questions")
	r.turnEnd(t, n, wait)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "ask_00000001 [withdrawn] Which database?") {
		t.Fatalf("check: %q", got)
	}
}

// fakeQuestions is a host's questions with one question.
type fakeQuestions struct{ withdrawn string }

func (f *fakeQuestions) List(context.Context) ([]askuser.Asked, error) {
	p, _ := askuser.Parse(json.RawMessage(`{"question":"Which database?","blocks":"x"}`))
	state := askuser.StateOpen
	if f.withdrawn != "" {
		state = askuser.StateWithdrawn
	}
	return []askuser.Asked{{ID: "ask_00000001", Payload: p.JSON(), State: state}}, nil
}

func (f *fakeQuestions) Withdraw(_ context.Context, id, reason string) (bool, error) {
	f.withdrawn = id + ":" + reason
	return true, nil
}

// A chat's thread has the chat tools beside ask_user and both guidances,
// the docs' and its pages folder's, then the model's instructions.
func TestAChatsThreadHasTheChatToolsBesideAskUser(t *testing.T) {
	thread := func(ep chattools.Endpoint) (*adapter, *recorder, string) {
		lab := newFakeLab(t)
		r := newRecorder()
		spec := lab.spec("a", "")
		spec.ChatTools, spec.Instructions = ep, map[string]string{"gpt-fake": "NOTE-FAKE"}
		spec.DataDir = "/data"
		a := lab.start(spec, r)
		_, n := send(t, a, r, "/thread")
		r.turnEnd(t, n, wait)
		return a, r, lastTurnText(r.projection())
	}
	sock := "/nonexistent/chat-tools.sock"
	_, _, got := thread(chattools.Endpoint{Socket: sock, Token: "c.x"})
	if !strings.Contains(got, askuser.Name) || !strings.Contains(got, chattools.List) ||
		!strings.Contains(got, "instructions: "+askGuidance+"\n\n"+chattools.Guidance("")) || !strings.Contains(got, todoGuidance+"\n\n"+agent.DocsGuidance+"\n\n"+agent.PagesGuidance("/data/chats/c1/pages")) || !strings.Contains(got, agent.ModelPrompt("gpt-fake", "NOTE-FAKE")) {
		t.Fatalf("a chat's thread:\n%s", got)
	}
}

// Asked again in the same turn, a question is not asked twice: the model
// is told it is already with the person.
func TestTheSameQuestionTwiceInATurnIsAskedOnce(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	spec := lab.spec("a", "")
	spec.Model = "gpt-6-astra"
	a := lab.start(spec, r)
	_, n := send(t, a, r, "/tooltwice ask_user")
	r.turnEnd(t, n, wait)
	var asks []agent.Event
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			asks = append(asks, ev)
		}
	}
	if len(asks) != 1 {
		t.Fatalf("%d asks for the same question in one turn", len(asks))
	}
	if got := lastTurnText(r.projection()); got != "tool said: "+askuser.QueuedText(asks[0].AskID, askuser.Decode(asks[0].Payload))+" "+AskedText(asks[0].AskID) {
		t.Fatalf("the model got %q", got)
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
