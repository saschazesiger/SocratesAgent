package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// liteChat is a workflow of three agents, each with calls of its own: one
// agent still running, one call a question names, and a big call and a
// small one at the top of the answer.
func liteChat() []Event {
	big := strings.Repeat("output line\n", 400)
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	events := []Event{
		{Type: TypeMessage, MessageID: "u", Role: RoleUser, State: MessageDelivered, Text: "go"},
		{Type: TypeTurnStarted, TurnID: "t", MessageID: "u"},
		{Type: TypeMessage, MessageID: "a", Role: RoleAssistant, State: MessageStreaming, TurnID: "t"},
		{Type: TypePart, MessageID: "a", PartID: "read", Kind: PartTool, State: PartDone, Text: big,
			Content: raw(`{"tool":"Read","input":{"file_path":"/x/y.go","limit":9},"summary":"/x/y.go","duration_ms":3}`)},
		{Type: TypePart, MessageID: "a", PartID: "small", Kind: PartTool, State: PartDone, Text: "ok", Content: raw(`{"tool":"Bash","command":"ls"}`)},
		{Type: TypePart, MessageID: "a", PartID: "wf", Kind: PartTool, State: PartRunning,
			Content: raw(`{"tool":"Workflow","input":{"script":"` + strings.Repeat("x", 3000) + `"},"workflow":{"status":"running","agents":3,"summary":"later"}}`)},
	}
	for i := 0; i < 3; i++ {
		agentID := fmt.Sprintf("ag%d", i)
		state := PartDone
		if i == 2 {
			state = PartRunning
		}
		events = append(events, Event{Type: TypePart, PartID: agentID, ParentPartID: "wf", Kind: PartTool, State: PartRunning,
			Content: raw(`{"tool":"WorkflowAgent","agent":{"label":"one","prompt":"` + strings.Repeat("p", 3000) + `","status":"running"}}`)})
		for j := 0; j < 2; j++ {
			events = append(events, Event{Type: TypePart, PartID: fmt.Sprintf("%s.c%d", agentID, j), ParentPartID: agentID, Kind: PartTool,
				State: PartDone, Text: big, Content: raw(`{"tool":"Grep","input":{"pattern":"x"}}`)})
		}
		events = append(events, Event{Type: TypePart, PartID: agentID, ParentPartID: "wf", Kind: PartTool, State: state,
			Content: raw(`{"tool":"WorkflowAgent","agent":{"label":"one","prompt":"` + strings.Repeat("p", 3000) + `","status":"` + state + `"}}`)})
	}
	events = append(events,
		Event{Type: TypeAsk, AskID: "k", TurnID: "t", Payload: raw(`{"part_id":"ag0.c1","questions":[]}`)},
		Event{Type: TypePartDelta, MessageID: "a", PartID: "ag1.c0", Offset: len(big), Text: "more"},
	)
	for i := range events {
		events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
	}
	return events
}

func TestLightenHoldsBackWhatACollapsedChatDoesNotDraw(t *testing.T) {
	events := liteChat()
	p := Fold(events)
	out := Lighten(Compact(events), 0, p)
	byID := map[string]Event{}
	for _, ev := range out {
		if ev.Type == TypePartDelta && ev.PartID == "ag1.c0" {
			t.Fatalf("a delta of a part held back went out: %+v", ev)
		}
		if ev.Type == TypePart {
			byID[ev.PartID] = ev
		}
	}
	for _, id := range []string{"read", "small", "wf", "ag0", "ag0.c1", "ag2"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("%s was held back", id)
		}
	}
	for _, id := range []string{"ag1", "ag1.c0", "ag1.c1", "ag0.c0", "ag2.c0", "ag2.c1"} {
		if _, ok := byID[id]; ok {
			t.Errorf("%s went out, finished and nested", id)
		}
	}
	if got := byID["wf"].Held; got != 1 {
		t.Errorf("the workflow holds back %d of its own, want 1 (ag1)", got)
	}
	if got := byID["ag0"].Held; got != 1 {
		t.Errorf("ag0 holds back %d, want 1 (c0; c1 is a question's)", got)
	}
	if got := byID["ag2"].Held; got != 2 {
		t.Errorf("the running agent holds back %d, want its 2 finished calls", got)
	}
	read := byID["read"]
	if !read.Lite || read.Text != "" || !strings.Contains(string(read.Content), `"file_path":"/x/y.go"`) || strings.Contains(string(read.Content), "limit") {
		t.Errorf("the big read is %+v %s", read.Lite, read.Content)
	}
	if small := byID["small"]; small.Lite || small.Text != "ok" {
		t.Errorf("a small call is sent whole: %+v", small)
	}
	wf := byID["wf"]
	if !wf.Lite || strings.Contains(string(wf.Content), "script") || strings.Contains(string(wf.Content), "later") || !strings.Contains(string(wf.Content), `"agents":3`) {
		t.Errorf("the workflow is %s", wf.Content)
	}
	if ag := byID["ag2"]; !ag.Lite || strings.Contains(string(ag.Content), "prompt") || !strings.Contains(string(ag.Content), `"label":"one"`) {
		t.Errorf("an agent is %s", ag.Content)
	}

	// What went out folds into the same messages, asks and turn, and the
	// same parts at the top of every message.
	light := Fold(out)
	top := func(p *Projection) string {
		var b strings.Builder
		for _, m := range p.Messages {
			fmt.Fprintf(&b, "%s:%s[", m.ID, m.State)
			for _, part := range m.Parts {
				fmt.Fprintf(&b, "%s:%s:%s ", part.ID, part.Kind, part.State)
			}
			b.WriteString("] ")
		}
		fmt.Fprintf(&b, "asks %d open %d turn %v", len(p.Asks), len(p.OpenAsks()), p.Turn != nil)
		return b.String()
	}
	if a, b := top(light), top(p); a != b {
		t.Fatalf("the light replay folds differently:\n%s\n%s", a, b)
	}
}

// A part the browser already had - made at or before the seq it holds - is
// never held back: it could not tell it apart from one that went.
func TestLightenHoldsBackOnlyWhatIsNew(t *testing.T) {
	events := liteChat()
	p := Fold(events)
	since := int64(9) // ag0 and its calls are made by then
	out := Lighten(Compact(events[since:]), since, p)
	got := map[string]bool{}
	for _, ev := range out {
		if ev.Type == TypePart {
			got[ev.PartID] = true
		}
	}
	if !got["ag0"] {
		t.Errorf("ag0, known to the browser and replaced since, was held back")
	}
	if got["ag1.c0"] {
		t.Errorf("ag1.c0, new and finished, went out")
	}
}

// Opened, a part is sent whole, with the parts nested directly in it light,
// each saying how many it holds in turn.
func TestDetailIsAPartWholeAndItsOwnLight(t *testing.T) {
	p := Fold(liteChat())
	part, children, ok := p.Detail("wf")
	if !ok || part.PartID != "wf" || !strings.Contains(string(part.Content), "script") || part.Lite {
		t.Fatalf("the workflow is %+v", part)
	}
	if len(children) != 3 {
		t.Fatalf("%d children", len(children))
	}
	for i, child := range children {
		if child.PartID != fmt.Sprintf("ag%d", i) || child.Held != 2 || !child.Lite || child.ParentPartID != "wf" || child.Seq == 0 {
			t.Errorf("child %d is %+v", i, child)
		}
	}
	if _, _, ok := p.Detail("nothing"); ok {
		t.Error("a part the chat does not have")
	}
	leaf, kids, _ := p.Detail("ag1.c0")
	if !strings.HasSuffix(leaf.Text, "more") || len(kids) != 0 {
		t.Errorf("a call opened: %q, %d children", leaf.Text[len(leaf.Text)-10:], len(kids))
	}
}

// A message the agent sent another chat goes whole, however long: the chat
// draws it as a message, not as a row.
func TestLightKeepsAMessageToAnotherChatWhole(t *testing.T) {
	long := strings.Repeat("words ", LiteFrom)
	for _, content := range []string{
		`{"tool":"mcp__socrates__send_to_chat","input":{"chat":"c1","text":"` + long + `"}}`,
		`{"tool":"dynamic","name":"send_to_chat","arguments":{"chat":"c1","text":"` + long + `"}}`,
	} {
		ev := Event{Type: TypePart, Kind: PartTool, PartID: "p", Text: "Delivered.", Content: json.RawMessage(content)}
		if got := Light(ev); got.Lite || string(got.Content) != content {
			t.Fatalf("lightened: %+v", got)
		}
	}
	ev := Event{Type: TypePart, Kind: PartTool, PartID: "p", Content: json.RawMessage(`{"tool":"Bash","input":{"command":"` + long + `"}}`)}
	if !Light(ev).Lite {
		t.Fatal("a long call of another tool stays whole")
	}
}
