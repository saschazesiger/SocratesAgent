package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// A question outranks a running turn only when that turn asked it: an ask
// left by an interrupted turn does not make the agent's next turn look
// stuck, and a closed one is not open at all.
func TestAQuestionWaitsOnlyWhileItsTurnHoldsTheAgent(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t1"},
		{Seq: 2, Type: TypeAsk, AskID: "a1", TurnID: "t1"},
	})
	if s := p.Summary(); s.Activity != ActivityWaiting || len(p.Unwaited()) != 0 {
		t.Fatalf("its own turn: %s, unwaited %d", s.Activity, len(p.Unwaited()))
	}
	p.Apply(Event{Seq: 3, Type: TypeTurnEnded, TurnID: "t1", Outcome: OutcomeInterrupted})
	if s := p.Summary(); s.Activity != ActivityWaiting || len(p.Unwaited()) != 1 {
		t.Fatalf("after the interrupt: %s, unwaited %d", s.Activity, len(p.Unwaited()))
	}
	p.Apply(Event{Seq: 4, Type: TypeTurnStarted, TurnID: "t2"})
	if s := p.Summary(); s.Activity != ActivityBusy || s.OpenAsks != 1 {
		t.Fatalf("another turn: %s, %d open", s.Activity, s.OpenAsks)
	}
	p.Apply(Event{Seq: 5, Type: TypeAskClosed, AskID: "a1"})
	p.Apply(Event{Seq: 6, Type: TypeTurnEnded, TurnID: "t2", Outcome: OutcomeCompleted})
	if s := p.Summary(); s.Activity != ActivityIdle || s.OpenAsks != 0 || !p.Ask("a1").Closed {
		t.Fatalf("closed: %s, %d open", s.Activity, s.OpenAsks)
	}
	// A closed question can still be answered.
	p.Apply(Event{Seq: 7, Type: TypeAskAnswered, AskID: "a1", ClientID: "c1"})
	if a := p.Ask("a1"); !a.Answered || len(p.UnsentAnswers()) != 1 {
		t.Fatalf("answered %+v", a)
	}
}

// What a message from the person leaves behind: the questions of ended
// turns, not one a running call waits on, not one the adapter tied to no
// turn, and never an ask_user question (answered as a message by design),
// which stays open until it is answered or withdrawn.
func TestUnwaitedIsWhatNoCallWaitsOn(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t1"},
		{Seq: 2, Type: TypeAsk, AskID: "next", TurnID: "t1", Via: ViaMessage},
		{Seq: 3, Type: TypeAsk, AskID: "waits", TurnID: "t1"},
		{Seq: 4, Type: TypeAsk, AskID: "loose"},
		{Seq: 5, Type: TypeAsk, AskID: "old", TurnID: "t0"},
		{Seq: 6, Type: TypeAsk, AskID: "done", TurnID: "t0"},
		{Seq: 7, Type: TypeAskAnswered, AskID: "done"},
		{Seq: 8, Type: TypeAsk, AskID: "earlier", TurnID: "t0", Via: ViaMessage},
	})
	var ids, waited []string
	for _, a := range p.Unwaited() {
		ids = append(ids, a.ID)
	}
	for _, a := range p.Waited() {
		waited = append(waited, a.ID)
	}
	if strings.Join(ids, ",") != "old" || strings.Join(waited, ",") != "waits,loose,old" {
		t.Fatalf("unwaited %v, waited %v", ids, waited)
	}
}

// An ask_user question leaves a running turn busy, because the agent went
// on; once no turn runs, the chat waits for the person. check_questions
// reads each one's state, and a withdrawn one keeps its reason.
func TestAnAskUserQuestionDoesNotHoldTheAgentUp(t *testing.T) {
	one := func(q string) json.RawMessage {
		p, err := askuser.Parse(json.RawMessage(`{"question":"` + q + `","blocks":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		return p.JSON()
	}
	p := Fold([]Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t1"},
		{Seq: 2, Type: TypeAsk, AskID: "ask_1", TurnID: "t1", Via: ViaMessage, Payload: one("A?")},
		{Seq: 3, Type: TypeAsk, AskID: "ask_2", TurnID: "t1", Via: ViaMessage, Payload: one("B?")},
		{Seq: 4, Type: TypeAsk, AskID: "ask_3", TurnID: "t1", Via: ViaMessage, Payload: one("C?")},
		{Seq: 5, Type: TypeAskAnswered, AskID: "ask_1", Answer: json.RawMessage(`{"general_note":"yes"}`)},
		{Seq: 6, Type: TypeAskClosed, AskID: "ask_2", Code: AskClosedWithdrawn, Text: "found it"},
	})
	if s := p.Summary(); s.Activity != ActivityBusy || s.OpenAsks != 1 || s.Questions != 1 || !s.PersonWaits() ||
		s.PersonActivity() != ActivityWaiting {
		t.Fatalf("while the turn runs: %s, %d open, %d questions", s.Activity, s.OpenAsks, s.Questions)
	}
	var got []string
	for _, q := range p.AskedQuestions() {
		got = append(got, q.ID+":"+q.State+":"+q.Reason)
	}
	if strings.Join(got, ",") != "ask_1:answered:,ask_2:withdrawn:found it,ask_3:open:" {
		t.Fatalf("questions %v", got)
	}
	p.Apply(Event{Seq: 7, Type: TypeTurnEnded, TurnID: "t1", Outcome: OutcomeCompleted})
	if s := p.Summary(); s.Activity != ActivityWaiting {
		t.Fatalf("after the turn: %s", s.Activity)
	}
}

// A subagent or workflow left running after the turn keeps the agent at
// work; a shell or a monitor left running is background work, not work.
func TestABackgroundSubagentKeepsTheAgentBusy(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t1"},
		{Seq: 2, Type: TypeTasks, Count: 2, Agents: 1},
		{Seq: 3, Type: TypeTurnEnded, TurnID: "t1", Outcome: OutcomeCompleted},
	})
	if s := p.Summary(); s.Activity != ActivityBusy || s.TurnRunning {
		t.Fatalf("with a subagent: %s, turn running %v", s.Activity, s.TurnRunning)
	}
	p.Apply(Event{Seq: 4, Type: TypeTasks, Count: 1})
	if s := p.Summary(); s.Activity != ActivityTasks {
		t.Fatalf("with a shell: %s", s.Activity)
	}
	p.Apply(Event{Seq: 5, Type: TypeTasks})
	if s := p.Summary(); s.Activity != ActivityIdle {
		t.Fatalf("with nothing: %s", s.Activity)
	}
}

// A host that journals the count of its tasks without how many are agents
// (one started before the count was journaled) is read from the parts: a
// workflow still running is the agent at work.
func TestARunningWorkflowPartKeepsTheAgentBusy(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t1", MessageID: "a1"},
		{Seq: 2, Type: TypePart, PartID: "w1", MessageID: "a1", Kind: PartTool, State: PartRunning,
			Content: []byte(`{"tool":"Workflow","workflow":{"status":"running"}}`)},
		{Seq: 3, Type: TypeTasks, Count: 1},
		{Seq: 4, Type: TypeTurnEnded, TurnID: "t1", Outcome: OutcomeCompleted},
	})
	if s := p.Summary(); s.Activity != ActivityBusy {
		t.Fatalf("with a workflow part running: %s", s.Activity)
	}
	p.Apply(Event{Seq: 5, Type: TypePart, PartID: "w1", MessageID: "a1", Kind: PartTool, State: PartDone,
		Content: []byte(`{"tool":"Workflow","workflow":{"status":"completed"}}`)})
	if s := p.Summary(); s.Activity != ActivityTasks {
		t.Fatalf("with the workflow done and a task left: %s", s.Activity)
	}
}
