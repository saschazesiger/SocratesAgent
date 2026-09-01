package harness

import (
	"encoding/json"
	"testing"
)

// The wire shape of an Event is the one thing the adapter, the journal, the
// engine and the browser all have to agree on, and three of those four are
// written by different people. So it is pinned here rather than described.
func TestEventJSONIsPinned(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  string
	}{
		{"turn started",
			Event{Kind: KindTurnStarted, Seq: 41, TS: 1788271900000, TurnID: "run_9f2c"},
			`{"kind":"turn_started","seq":41,"ts":1788271900000,"turn_id":"run_9f2c"}`},
		{"text delta",
			Event{Kind: KindTextDelta, Seq: 42, TS: 1788271901000, TurnID: "run_9f2c", ID: "text-0", Text: "I'll check the "},
			`{"kind":"text_delta","seq":42,"ts":1788271901000,"turn_id":"run_9f2c","id":"text-0","text":"I'll check the "}`},
		{"tool started",
			Event{Kind: KindToolStarted, Seq: 44, TS: 1788271902000, TurnID: "run_9f2c", ID: "toolu_015x",
				Tool: &Tool{Name: "Bash", Title: "Ran a command", Input: "go test ./...", InputJSON: `{"command":"go test ./..."}`}},
			`{"kind":"tool_started","seq":44,"ts":1788271902000,"turn_id":"run_9f2c","id":"toolu_015x",` +
				`"tool":{"name":"Bash","title":"Ran a command","input":"go test ./...","input_json":"{\"command\":\"go test ./...\"}"}}`},
		{"tool finished",
			Event{Kind: KindToolFinished, Seq: 46, TS: 1788271904000, TurnID: "run_9f2c", ID: "toolu_015x",
				Tool: &Tool{Name: "Bash", Title: "Ran a command", Output: "ok\n", OK: true}},
			`{"kind":"tool_finished","seq":46,"ts":1788271904000,"turn_id":"run_9f2c","id":"toolu_015x",` +
				`"tool":{"name":"Bash","title":"Ran a command","output":"ok\n","ok":true}}`},
		{"usage",
			Event{Kind: KindUsage, Seq: 56, TS: 1788271916000, TurnID: "run_9f2c",
				Usage: &Usage{Input: 11143, Output: 198, Cached: 4352, Reasoning: 21, Total: 11341, CostUSD: 0.017, Context: 258400}},
			`{"kind":"usage","seq":56,"ts":1788271916000,"turn_id":"run_9f2c",` +
				`"usage":{"input":11143,"output":198,"cached":4352,"reasoning":21,"total":11341,"cost_usd":0.017,"context_window":258400}}`},
		{"turn finished",
			Event{Kind: KindTurnFinished, Seq: 59, TS: 1788271917000, TurnID: "run_9f2c", Outcome: OutcomeOK},
			`{"kind":"turn_finished","seq":59,"ts":1788271917000,"turn_id":"run_9f2c","outcome":"ok"}`},
		{"session id",
			Event{Kind: KindSessionID, Seq: 2, TS: 1788271899000, Session: "953755b8-5bb5-4c26-b046-ef1a990c0154"},
			`{"kind":"session_id","seq":2,"ts":1788271899000,"session":"953755b8-5bb5-4c26-b046-ef1a990c0154"}`},
		{"fatal",
			Event{Kind: KindFatal, Seq: 60, TS: 1788271918000, Error: "claude exited with code 1 before the turn finished"},
			`{"kind":"fatal","seq":60,"ts":1788271918000,"error":"claude exited with code 1 before the turn finished"}`},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.event)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(raw) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, raw, c.want)
		}
		var back Event
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		again, _ := json.Marshal(back)
		if string(again) != c.want {
			t.Errorf("%s does not round trip: %s", c.name, again)
		}
	}
}

// An adapter leaves Seq and TS at zero and the host stamps them, so a zero
// value must not be omitted from the wire - the engine's floor test reads Seq
// off every frame.
func TestSeqAndTimestampAreAlwaysWritten(t *testing.T) {
	raw, err := json.Marshal(Event{Kind: KindNotice, Error: "hm"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"notice","seq":0,"ts":0,"error":"hm"}` {
		t.Fatalf("got %s", raw)
	}
}

func TestFilterEffortsKeepsTheIntersectionInOrder(t *testing.T) {
	got := FilterEfforts([]string{"xhigh", "high", "minimal", "low", "medium", "ultra"})
	want := []string{"low", "medium", "high"}
	if len(got) != len(want) {
		t.Fatalf("efforts = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("efforts = %#v", got)
		}
	}
	if len(FilterEfforts(nil)) != 0 {
		t.Fatal("an agent with no efforts gained some")
	}
}

func TestTruncateOutputSaysThatItDid(t *testing.T) {
	long := make([]byte, ToolOutputLimit+10)
	for i := range long {
		long[i] = 'x'
	}
	got := TruncateOutput(string(long))
	if len(got) <= ToolOutputLimit {
		t.Fatalf("truncated to %d", len(got))
	}
	if got[ToolOutputLimit:] != "\n… [output truncated]" {
		t.Fatalf("no marker: %q", got[ToolOutputLimit:])
	}
	if TruncateOutput("short") != "short" {
		t.Fatal("a short output was touched")
	}
}

// Two adapters registering under the same id would silently shadow one
// another, and IDs is what the picker is built from, so its order is fixed.
func TestRegistryIsSortedAndAddressable(t *testing.T) {
	Register(Descriptor{ID: "zzz-test", Label: "Z"})
	Register(Descriptor{ID: "aaa-test", Label: "A"})
	ids := IDs()
	last := ""
	for _, id := range ids {
		if id < last {
			t.Fatalf("IDs is not sorted: %#v", ids)
		}
		last = id
	}
	if d, ok := Get("aaa-test"); !ok || d.Label != "A" {
		t.Fatalf("Get = %#v %v", d, ok)
	}
	if _, ok := Get("nothing"); ok {
		t.Fatal("an unregistered id was found")
	}
}
