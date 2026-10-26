package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A light browser's replay holds back what a collapsed chat does not draw -
// a finished workflow's agents and what they did - and the workflow says how
// many of its own it holds; opened, the workflow is asked for, whole, with
// its agents light. A browser that does not ask for light gets everything.
func TestALightReplayHoldsBackAWorkflowsAgentsUntilItIsOpened(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/workflow 20")
	journal := c.waitJournal(t, id, 15*time.Second, "the workflow", turnsEnded(1))
	wf := ""
	for _, ev := range journal {
		if ev.Type == agent.TypePart && strings.HasSuffix(ev.PartID, "_wf") {
			wf = ev.PartID
		}
	}
	if wf == "" {
		t.Fatal("the workflow made no part")
	}

	light := c.streamHello(t, id, map[string]any{"t": "hello", "since": 0, "lite": true}).untilSynced(10 * time.Second)
	held := -1
	for _, ev := range light {
		if ev.ParentPartID != "" {
			t.Fatalf("a nested part went out: %+v", ev)
		}
		if ev.PartID == wf {
			held = ev.Held
		}
	}
	if held != 4 {
		t.Fatalf("the workflow holds back %d of its agents, want 4", held)
	}
	whole := c.stream(t, id, 0).untilSynced(10 * time.Second)
	nested := 0
	for _, ev := range whole {
		if ev.ParentPartID != "" {
			nested++
		}
	}
	if nested != 8 {
		t.Fatalf("a browser that is not light got %d nested parts, want the 4 agents and their 4 texts", nested)
	}

	res, body := c.do(t, c.client, "GET", "/api/chats/"+id+"/parts/"+wf, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the workflow, opened: %d %v", res.StatusCode, body)
	}
	raw, _ := json.Marshal(body)
	var detail struct {
		Seq      int64         `json:"seq"`
		Part     agent.Event   `json:"part"`
		Children []agent.Event `json:"children"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Part.PartID != wf || detail.Part.State != agent.PartDone || detail.Seq < journal[len(journal)-1].Seq {
		t.Fatalf("the workflow is %+v at seq %d", detail.Part, detail.Seq)
	}
	if len(detail.Children) != 4 {
		t.Fatalf("%d agents", len(detail.Children))
	}
	for _, child := range detail.Children {
		if child.ParentPartID != wf || child.Held != 1 || child.State != agent.PartDone {
			t.Fatalf("an agent is %+v", child)
		}
	}
	if res, _ := c.do(t, c.client, "GET", "/api/chats/"+id+"/parts/nothing", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a part the chat does not have: %d", res.StatusCode)
	}
}

// A browser that takes events in batches gets a replay in a frame or few,
// and the same events as one that takes them a frame each.
func TestAReplayComesInBatchesToABrowserThatTakesThem(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/tools")
	c.waitJournal(t, id, 15*time.Second, "the tools", turnsEnded(1))
	one := c.stream(t, id, 0)
	single := one.untilSynced(10 * time.Second)
	many := c.streamHello(t, id, map[string]any{"t": "hello", "since": 0, "batch": true})
	batched := many.untilSynced(10 * time.Second)
	a, _ := json.Marshal(single)
	b, _ := json.Marshal(batched)
	many.mu.Lock()
	frames := many.batches
	many.mu.Unlock()
	if string(a) != string(b) || frames != 1 || len(batched) < 5 {
		t.Fatalf("%d events in %d batches, the same as one at a time: %v", len(batched), frames, string(a) == string(b))
	}
}
