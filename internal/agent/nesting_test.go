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

// An image a tool call returned stands beside the call: at the top of the
// message for a call there, inside the agent for a call of a subagent. It
// keeps the call's id, and a replay sends it although the call holds back
// what is nested in it.
func TestAToolsImageStandsBesideTheCall(t *testing.T) {
	events := []Event{
		{Seq: 1, Type: TypeTurnStarted, TurnID: "t"},
		{Seq: 2, Type: TypePart, MessageID: "m", PartID: "shot", Kind: PartTool, State: PartDone},
		{Seq: 3, Type: TypePart, PartID: "shot_img0", ParentPartID: "shot", Kind: PartImage, State: PartDone, Content: json.RawMessage(`{"path":"/x.png"}`)},
		{Seq: 4, Type: TypePart, MessageID: "m", PartID: "task", Kind: PartTool, State: PartRunning},
		{Seq: 5, Type: TypePart, MessageID: "m", PartID: "sub", ParentPartID: "task", Kind: PartTool, State: PartDone},
		{Seq: 6, Type: TypePart, MessageID: "m", PartID: "sub_img0", ParentPartID: "sub", Kind: PartImage, State: PartDone},
	}
	p := Fold(events)
	var top []string
	for _, part := range p.Message("m").Parts {
		top = append(top, part.ID)
	}
	if strings.Join(top, ",") != "shot,shot_img0,task" {
		t.Fatalf("top-level parts %v", top)
	}
	if img := p.Part("shot_img0"); img.MessageID != "m" || img.ParentID != "shot" || len(p.Part("shot").Children) != 0 {
		t.Fatalf("the image %+v, the call's children %d", img, len(p.Part("shot").Children))
	}
	var inTask []string
	for _, part := range p.Part("task").Children {
		inTask = append(inTask, part.ID)
	}
	if strings.Join(inTask, ",") != "sub,sub_img0" {
		t.Fatalf("the agent's parts %v", inTask)
	}
	sent := map[string]bool{}
	for _, ev := range Lighten(events, 0, p) {
		sent[ev.PartID] = true
	}
	if !sent["shot_img0"] {
		t.Fatal("a replay held back the image of a call at the top")
	}
}

func TestAnAskCanAskForItsAnswerAsTheNextMessage(t *testing.T) {
	p := Fold([]Event{{Seq: 1, Type: TypeAsk, AskID: "k", Via: ViaMessage}, {Seq: 2, Type: TypeAsk, AskID: "j"}})
	if p.Ask("k").Deliver != ViaMessage || p.Ask("j").Deliver != "" {
		t.Fatalf("deliver %q %q", p.Ask("k").Deliver, p.Ask("j").Deliver)
	}
}

// A file a question names (its context, a step, a section, an option's
// details) is one the chat may show, as one an answer names is; a path
// that only the JSON escapes differently is still the path.
func TestAQuestionNamesTheFilesItShows(t *testing.T) {
	p := Fold([]Event{
		{Seq: 1, Type: TypePart, MessageID: "m", PartID: "p", Kind: PartText, State: PartDone, Text: "Here: ![a](/tmp/answer.png)"},
		{Seq: 2, Type: TypeAsk, AskID: "k", Payload: json.RawMessage(`{"questions":[{"id":"q","question":"Which?","context":"See ![shot](\/tmp\/shöt.png)","options":[{"id":"a","label":"A","details":"![b](rel/b.png)"}]}],"steps":["Look at ![c](/tmp/c.png)"]}`)},
	})
	for _, named := range []string{"/tmp/answer.png", "/tmp/shöt.png", "rel/b.png", "/tmp/c.png"} {
		if !p.AgentNames(named) {
			t.Errorf("%s is named but not served", named)
		}
	}
	for _, other := range []string{"/etc/passwd", "/tmp/other.png", ""} {
		if p.AgentNames(other) {
			t.Errorf("%q is not named but would be served", other)
		}
	}
}
