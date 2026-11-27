//go:build !windows

package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// The real codex offers ask_user, check_questions and withdraw_question to
// a classic model as top-level tools and to a code-mode model inside its
// script: a call is put at once, the turn ends, and the answer reaches the
// model as the next message.
func TestRealAskUserReturnsAtOnce(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-6-astra"} {
		t.Run(model, func(t *testing.T) {
			lab := newRealLab(t)
			r := newRecorder()
			a := lab.start(lab.spec(lab.home("a", "", model), filepath.Join(lab.root, "state"), ""), r)
			_, n := send(t, a, r, "ASK:")
			ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), 30*time.Second)
			p := askuser.Decode(ask.Payload)
			if p == nil || len(p.Questions) != 1 || ask.Via != agent.ViaMessage || p.PartID == "" {
				t.Fatalf("ask %+v\n%s", ask, dump(r.since(n)))
			}
			if log := lab.modelLog(t); !strings.Contains(log, "ask_user") || !strings.Contains(log, "check_questions") ||
				!strings.Contains(log, "Questions to the person go through the ask_user tool") {
				t.Fatal("the model was not offered the question tools, or not told to use them")
			}
			end := r.turnEnd(t, n, 30*time.Second)
			if got := lastTurnText(r.projection()); end.Outcome != agent.OutcomeCompleted || !strings.Contains(got, "is now with the person") {
				t.Fatalf("the model got %q\n%s", got, dump(r.since(n)))
			}
			answer := agent.AnswerText(&agent.Ask{ID: ask.AskID, Payload: ask.Payload}, json.RawMessage(`{"answers":{"q1":{"selected":["pg"]}}}`))
			_, n = send(t, a, r, answer)
			r.turnEnd(t, n, 30*time.Second)
			if !strings.Contains(lab.modelLog(t), "The person answered your question "+ask.AskID) {
				t.Fatal("the answer did not reach the model as a message")
			}
		})
	}
}

// Plan mode's request_user_input reaches the person and its answer the
// model, in codex's own shape.
func TestRealRequestUserInputInPlanMode(t *testing.T) {
	lab := newRealLab(t)
	r := newRecorder()
	a := lab.start(lab.spec(lab.home("a", "", "gpt-5.5"), filepath.Join(lab.root, "state"), ""), r)
	if err := a.Command(context.Background(), "plan", ""); err != nil {
		t.Fatal(err)
	}
	_, n := send(t, a, r, "INPUT")
	ask := r.waitFor(t, n, "the ask", isType(agent.TypeAsk), 30*time.Second)
	p := askuser.Decode(ask.Payload)
	if p == nil || p.Source != askuser.SourceUserInput || p.Questions[0].ID != "color" {
		t.Fatalf("ask %s\n%s", ask.Payload, dump(r.since(n)))
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"answers":{"color":{"selected":["Red"],"note":"dark red"}}}`)); err != nil {
		t.Fatal(err)
	}
	r.turnEnd(t, n, 30*time.Second)
	if got := lastTurnText(r.projection()); !strings.Contains(got, "Red") || !strings.Contains(got, "dark red") {
		t.Fatalf("the model got %q\n%s", got, dump(r.since(n)))
	}
}

// request_user_input_async surfaces as an agent message with questions,
// which become an ask answered as the next message.
func TestRealAsyncQuestions(t *testing.T) {
	lab := newRealLab(t)
	r := newRecorder()
	a := lab.start(lab.spec(lab.home("a", "", "gpt-6-astra"), filepath.Join(lab.root, "state"), ""), r)
	_, n := send(t, a, r, "ASYNC")
	r.turnEnd(t, n, 30*time.Second)
	var ask *agent.Event
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeAsk {
			ev := ev
			ask = &ev
		}
	}
	if ask == nil || ask.Via != agent.ViaMessage {
		t.Fatalf("no deferred ask\n%s", dump(r.since(n)))
	}
	if p := askuser.Decode(ask.Payload); p == nil || p.Source != askuser.SourceAsync || p.Questions[0].Options[0].Label != "Yes" {
		t.Fatalf("payload %s", ask.Payload)
	}
}

// modelLog is every request codex sent the fake model.
func (l *realLab) modelLog(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(l.root, "model.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
