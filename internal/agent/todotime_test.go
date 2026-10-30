package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSplitEstimate(t *testing.T) {
	cases := []struct {
		in, title string
		secs      int64
	}{
		{"Run the tests (~3m)", "Run the tests", 180},
		{"Run the tests (~1h15m)", "Run the tests", 4500},
		{"Run the tests [~1h 30m]", "Run the tests", 5400},
		{"Read the file (~30s)", "Read the file", 30},
		{"Read the file (est. 90 sec)", "Read the file", 90},
		{"Build ~45 min", "Build", 2700},
		{"Build (~1.5h)", "Build", 5400},
		{"Build (2 mins)", "Build", 120},
		{"Fix bug (#123)", "Fix bug (#123)", 0},
		{"Update deps (go 1.22)", "Update deps (go 1.22)", 0},
		{"Plain title", "Plain title", 0},
		{"Check (~5 parsecs)", "Check (~5 parsecs)", 0},
	}
	for _, c := range cases {
		title, secs := splitEstimate(c.in)
		if title != c.title || secs != c.secs {
			t.Errorf("splitEstimate(%q) = %q, %d; want %q, %d", c.in, title, secs, c.title, c.secs)
		}
	}
}

// task is one task of a timed list, as the UI reads it.
type timedTask struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Step       string `json:"step"`
	ActiveForm string `json:"active_form"`
	Status     string `json:"status"`
	Estimate   int64  `json:"estimate_s"`
	Added      int64  `json:"added_at"`
	Started    int64  `json:"started_at"`
	Done       int64  `json:"done_at"`
}

func timed(t *testing.T, raw json.RawMessage, list string) []timedTask {
	t.Helper()
	var c map[string]json.RawMessage
	var tasks []timedTask
	if err := json.Unmarshal(raw, &c); err != nil || json.Unmarshal(c[list], &tasks) != nil {
		t.Fatalf("content %s: %v", raw, err)
	}
	return tasks
}

func TestTimeTodosClaude(t *testing.T) {
	t0 := time.UnixMilli(1_000_000)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	list := func(s1, s2 string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"items": []map[string]any{
			{"id": "1", "subject": "Read the code (~2m)", "active_form": "Reading the code (~2m)", "status": s1},
			{"id": "2", "subject": "Write the fix (~10m)", "status": s2},
		}})
		return raw
	}
	v1 := TimeTodos(list("pending", "pending"), nil, at(0))
	got := timed(t, v1, "items")
	if got[0].Subject != "Read the code" || got[0].ActiveForm != "Reading the code" || got[0].Estimate != 120 ||
		got[1].Estimate != 600 || got[0].Added != t0.UnixMilli() || got[0].Started != 0 {
		t.Fatalf("v1 = %+v", got)
	}
	v2 := TimeTodos(list("in_progress", "pending"), v1, at(5))
	v3 := TimeTodos(list("completed", "in_progress"), v2, at(65))
	got = timed(t, v3, "items")
	if got[0].Started != at(5).UnixMilli() || got[0].Done != at(65).UnixMilli() || got[1].Started != at(65).UnixMilli() ||
		got[0].Added != t0.UnixMilli() {
		t.Fatalf("v3 = %+v", got)
	}
	// A second emission with nothing new keeps every stamp.
	v4 := TimeTodos(list("completed", "in_progress"), v3, at(100))
	if string(v4) != string(v3) {
		t.Fatalf("v4 = %s, v3 = %s", v4, v3)
	}
	// Completed without ever being in progress: began when the one before
	// was done.
	v5 := TimeTodos(list("completed", "completed"), v2, at(200))
	got = timed(t, v5, "items")
	if got[1].Started != at(200).UnixMilli() || got[1].Done != at(200).UnixMilli() {
		t.Fatalf("v5 = %+v", got)
	}
	v6 := TimeTodos(list("completed", "completed"), v3, at(300))
	got = timed(t, v6, "items")
	if got[1].Started != at(65).UnixMilli() || got[1].Done != at(300).UnixMilli() {
		t.Fatalf("v6 = %+v", got)
	}
}

// Codex's steps have no ids: they are known by their titles, whatever the
// estimate says.
func TestTimeTodosCodexSteps(t *testing.T) {
	t0 := time.UnixMilli(5_000_000)
	steps := func(est, s1 string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"explanation": "why", "steps": []map[string]any{
			{"step": "Inspect " + est, "status": s1},
		}})
		return raw
	}
	v1 := TimeTodos(steps("(~1m)", "inProgress"), nil, t0)
	v2 := TimeTodos(steps("(~5m)", "inProgress"), v1, t0.Add(time.Minute))
	got := timed(t, v2, "steps")
	if got[0].Step != "Inspect" || got[0].Estimate != 300 || got[0].Started != t0.UnixMilli() {
		t.Fatalf("v2 = %+v (%s)", got, v2)
	}
	var top map[string]any
	_ = json.Unmarshal(v2, &top)
	if top["explanation"] != "why" {
		t.Fatalf("explanation lost: %s", v2)
	}
}

func TestTimeTodosLeavesOtherContent(t *testing.T) {
	for _, raw := range []string{`{"tool":"x"}`, `not json`, `{"items":"nope"}`} {
		if got := TimeTodos(json.RawMessage(raw), nil, time.Now()); string(got) != raw {
			t.Errorf("TimeTodos(%s) = %s", raw, got)
		}
	}
}

// The recorder stamps a todo part against the list before it, also across
// parts and a restart (the stamps are in the journal).
func TestRecorderTimesTodos(t *testing.T) {
	dir := t.TempDir()
	j, events, err := OpenJournal(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(j, events, time.Millisecond)
	content := func(status string) json.RawMessage {
		return json.RawMessage(`{"items":[{"id":"1","subject":"Do it (~1m)","status":"` + status + `"}]}`)
	}
	if _, err := r.Emit(Event{Type: TypePart, MessageID: "m1", PartID: "todo_m1", Kind: PartTodo, Content: content("in_progress")}); err != nil {
		t.Fatal(err)
	}
	var started int64
	r.View(func(p *Projection) {
		got := timed(t, p.parts["todo_m1"].Content, "items")
		started = got[0].Started
		if got[0].Subject != "Do it" || got[0].Estimate != 60 || started == 0 {
			t.Fatalf("first = %+v", got)
		}
	})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	j, events, err = OpenJournal(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r = NewRecorder(j, events, time.Millisecond)
	defer r.Close()
	if _, err := r.Emit(Event{Type: TypePart, MessageID: "m2", PartID: "todo_m2", Kind: PartTodo, Content: content("completed")}); err != nil {
		t.Fatal(err)
	}
	r.View(func(p *Projection) {
		got := timed(t, p.parts["todo_m2"].Content, "items")
		if got[0].Started != started || got[0].Done < started {
			t.Fatalf("second = %+v, started %d", got, started)
		}
	})
}
