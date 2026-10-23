package agenthost

import (
	"path/filepath"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A subagent or workflow group can outlive its turn; when its host dies,
// the next one closes it, or the chat would show it running for ever.
func TestRecoverStopsBackgroundGroupsOfADeadHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, _, err := agent.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(
		agent.Event{Type: agent.TypeTurnStarted, TurnID: "t1"},
		agent.Event{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, State: agent.MessageStreaming},
		agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "wf", Kind: agent.PartTool, State: agent.PartRunning, Content: []byte(`{"tool":"Workflow"}`)},
		agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "wf_a1", ParentPartID: "wf", Kind: agent.PartTool, State: agent.PartRunning},
		agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "x", Kind: agent.PartText, State: agent.PartDone, Text: "ok"},
		agent.Event{Type: agent.TypeMessageState, MessageID: "a1", State: agent.MessageDone},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted},
	); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, events, err := agent.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &host{rec: agent.NewRecorder(j, events, 0)}
	defer h.rec.Close()
	if err := h.recover(); err != nil {
		t.Fatal(err)
	}
	h.rec.View(func(p *agent.Projection) {
		wf := p.Part("wf")
		if wf.State != agent.PartInterrupted || string(wf.Content) != `{"tool":"Workflow"}` {
			t.Fatalf("workflow part = %+v", wf)
		}
		if a := p.Part("wf_a1"); a.State != agent.PartInterrupted || a.ParentID != "wf" || len(wf.Children) != 1 {
			t.Fatalf("agent part = %+v", a)
		}
		if p.Part("x").State != agent.PartDone {
			t.Fatal("a finished part was touched")
		}
		if p.Turn != nil {
			t.Fatal("a turn was opened")
		}
	})
}
