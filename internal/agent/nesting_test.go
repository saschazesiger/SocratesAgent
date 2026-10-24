package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// A part whose parent is a part the projection has belongs inside it, in
// creation order, at any depth; replacing a nested part keeps its place; an
// interrupted turn reaches the nested parts too.
func TestProjectionNestsPartsUnderTheirParent(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t"},
		{Seq: 2, Type: TypePart, MessageID: "m", PartID: "task", Kind: PartTool, State: PartRunning},
		{Seq: 3, Type: TypePart, MessageID: "m", PartID: "a", ParentPartID: "task", Kind: PartText, State: PartDone, Text: "one"},
		{Seq: 4, Type: TypePart, PartID: "b", ParentPartID: "task", Kind: PartTool, State: PartRunning},
		{Seq: 5, Type: TypePart, PartID: "c", ParentPartID: "b", Kind: PartText, State: PartStreaming},
		{Seq: 6, Type: TypePartDelta, PartID: "c", Text: "deep"},
		{Seq: 7, Type: TypePart, MessageID: "m", PartID: "after", Kind: PartText, State: PartDone},
		{Seq: 8, Type: TypePart, PartID: "orphan", ParentPartID: "nowhere", MessageID: "m", Kind: PartText},
		{Seq: 9, Type: TypePart, MessageID: "m", PartID: "a", ParentPartID: "task", Kind: PartText, State: PartDone, Text: "one, again"},
	})
	m := p.Message("m")
	var top []string
	for _, part := range m.Parts {
		top = append(top, part.ID)
	}
	if strings.Join(top, ",") != "task,after,orphan" {
		t.Fatalf("top-level parts %v", top)
	}
	task := p.Part("task")
	if len(task.Children) != 2 || task.Children[0].ID != "a" || task.Children[1].ID != "b" || task.Children[0].Text != "one, again" {
		t.Fatalf("task children %+v", task.Children)
	}
	if c := p.Part("c"); c.Text != "deep" || c.MessageID != "m" || len(p.Part("b").Children) != 1 {
		t.Fatalf("the grandchild %+v", c)
	}
	if o := p.Part("orphan"); o.ParentID != "nowhere" {
		t.Fatalf("a part whose parent is unknown keeps the id: %+v", o)
	}
	p.Apply(Event{Seq: 10, Type: TypeTurnEnded, TurnID: "t", Outcome: OutcomeInterrupted})
	if p.Part("c").State != PartInterrupted || p.Part("b").State != PartInterrupted {
		t.Fatalf("nested parts after the interrupt: %s %s", p.Part("b").State, p.Part("c").State)
	}
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"children":[`) || !strings.Contains(string(raw), `"parent_part_id":"task"`) {
		t.Fatalf("the projection's JSON does not nest: %s", raw)
	}
}

func TestAnAskCanAskForItsAnswerAsTheNextMessage(t *testing.T) {
	p := Fold([]Event{{Seq: 1, Type: TypeAsk, AskID: "k", Via: ViaMessage}, {Seq: 2, Type: TypeAsk, AskID: "j"}})
	if p.Ask("k").Deliver != ViaMessage || p.Ask("j").Deliver != "" {
		t.Fatalf("deliver %q %q", p.Ask("k").Deliver, p.Ask("j").Deliver)
	}
}
