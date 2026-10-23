//go:build linux

package opencode

import (
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// What a catch-up reads can be older than what /event said: a part never goes
// back from its end, a snapshot that lags the streamed text is not news, and
// a part ends with the part whole rather than a delta.
func TestAPartNeverGoesBackAndEndsWhole(t *testing.T) {
	rec := newRecorder()
	a := New()
	a.out = rec
	emit := func(id string, c converted) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.emitPartLocked(id, "msg_1", "", c)
	}
	emit("prt_text", converted{kind: agent.PartText, state: agent.PartStreaming})
	a.handleDelta(0, "prt_text", "abc")
	emit("prt_text", converted{kind: agent.PartText, state: agent.PartStreaming})
	emit("prt_text", converted{kind: agent.PartText, state: agent.PartStreaming, text: "abcd"})
	emit("prt_text", converted{kind: agent.PartText, state: agent.PartDone, text: "abcd"})
	emit("prt_text", converted{kind: agent.PartText, state: agent.PartDone, text: "abcdef"})
	emit("prt_text", converted{kind: agent.PartText, state: agent.PartStreaming, text: "abc"})
	emit("prt_tool", converted{kind: agent.PartTool, state: agent.PartRunning, content: []byte(`{"tool":"bash"}`)})
	emit("prt_tool", converted{kind: agent.PartTool, state: agent.PartDone, text: "out", content: []byte(`{"tool":"bash"}`)})
	emit("prt_tool", converted{kind: agent.PartTool, state: agent.PartRunning, content: []byte(`{"tool":"bash"}`)})
	var got []string
	rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			got = append(got, ev.Type+" "+ev.PartID+" "+ev.State+" "+ev.Text)
		}
	})
	want := []string{
		"part prt_text streaming ",
		"part.delta prt_text  abc",
		"part.delta prt_text  d",
		"part prt_text done abcd",
		"part prt_text done abcdef",
		"part prt_tool running ",
		"part prt_tool done out",
	}
	if len(got) != len(want) {
		t.Fatalf("events\n%q\nwant\n%q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events\n%q\nwant\n%q", got, want)
		}
	}
}
