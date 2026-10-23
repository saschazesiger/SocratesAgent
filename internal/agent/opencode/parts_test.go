//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// taskPart is a task tool part of the chat's session; child "" is one whose
// subagent session is not known yet, end 0 one still running.
func taskPart(id, msg, status, child string, start, end int64) string {
	meta := `{}`
	if child != "" {
		meta = `{"sessionId":"` + child + `"}`
	}
	return `{"id":"` + id + `","sessionID":"ses_root","messageID":"` + msg + `","type":"tool","tool":"task","callID":"c_` + id + `",
		"state":{"status":"` + status + `","input":{"description":"d","prompt":"p","subagent_type":"general"},"metadata":` + meta + `,
		"time":{"start":` + jsonInt(start) + `,"end":` + jsonInt(end) + `}}}`
}

func jsonInt(n int64) string { raw, _ := json.Marshal(n); return string(raw) }

func mustPart(t *testing.T, raw string) ocPart {
	t.Helper()
	var p ocPart
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// placeOf is where the events of a part put it: "<message> <parent>", or
// several of those when they disagree.
func placeOf(rec *recorder, part string) string {
	places := map[string]bool{}
	out := ""
	rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.PartID != part || ev.Type != agent.TypePart {
				continue
			}
			if place := ev.MessageID + " " + ev.ParentPartID; !places[place] {
				places[place] = true
				if out != "" {
					out += ", "
				}
				out += place
			}
		}
	})
	return out
}

// A task that resumes an earlier task's subagent (task_id) gets what the
// subagent does from then on.
func TestAResumedSubagentGoesInsideTheTaskThatResumedIt(t *testing.T) {
	rec := newRecorder()
	a := New()
	a.out, a.session = rec, "ses_root"
	a.handlePart(0, mustPart(t, taskPart("prt_task1", "msg_a1", "running", "ses_c", 100, 0)))
	a.handlePart(0, mustPart(t, `{"id":"prt_c1","sessionID":"ses_c","messageID":"msg_c1","type":"text","text":"first","time":{"start":101,"end":102}}`))
	a.handlePart(0, mustPart(t, taskPart("prt_task1", "msg_a1", "completed", "ses_c", 100, 150)))
	// OpenCode names the subagent's session once the task runs.
	a.handlePart(0, mustPart(t, taskPart("prt_task2", "msg_a2", "pending", "", 0, 0)))
	a.handlePart(0, mustPart(t, taskPart("prt_task2", "msg_a2", "running", "ses_c", 200, 0)))
	a.handlePart(0, mustPart(t, `{"id":"prt_c2","sessionID":"ses_c","messageID":"msg_c2","type":"text","text":"second","time":{"start":201,"end":202}}`))
	// A later update of the first task part leaves the subagent with the
	// second.
	a.handlePart(0, mustPart(t, taskPart("prt_task1", "msg_a1", "completed", "ses_c", 100, 150)))
	a.handlePart(0, mustPart(t, `{"id":"prt_c3","sessionID":"ses_c","messageID":"msg_c2","type":"text","text":"third","time":{"start":203,"end":204}}`))
	for part, want := range map[string]string{"prt_c1": "msg_a1 prt_task1", "prt_c2": "msg_a2 prt_task2", "prt_c3": "msg_a2 prt_task2"} {
		if got := placeOf(rec, part); got != want {
			t.Errorf("%s is in %q, want %q", part, got, want)
		}
	}
}

// A subagent of the history that a task of this process resumes is caught
// up after a dropped stream, from where the task resumed it: what it did
// before is the journal's already.
func TestACatchUpReadsAResumedSubagentFromItsResumption(t *testing.T) {
	stored := map[string]string{
		"/session/ses_root/message": `[
			{"info":{"id":"msg_u0","sessionID":"ses_root","role":"user","time":{"created":90}},"parts":[]},
			{"info":{"id":"msg_a0","sessionID":"ses_root","role":"assistant","parentID":"msg_u0","time":{"created":95,"completed":300}},
			 "parts":[` + taskPart("prt_task0", "msg_a0", "completed", "ses_c", 100, 290) + `]},
			{"info":{"id":"msg_u2","sessionID":"ses_root","role":"user","time":{"created":900}},"parts":[]},
			{"info":{"id":"msg_a2","sessionID":"ses_root","role":"assistant","parentID":"msg_u2","time":{"created":950}},
			 "parts":[` + taskPart("prt_task2", "msg_a2", "running", "ses_c", 1000, 0) + `]}]`,
		"/session/ses_c/message": `[
			{"info":{"id":"msg_c0u","sessionID":"ses_c","role":"user","time":{"created":150}},
			 "parts":[{"id":"prt_c0p","sessionID":"ses_c","messageID":"msg_c0u","type":"text","text":"old prompt"}]},
			{"info":{"id":"msg_c0","sessionID":"ses_c","role":"assistant","parentID":"msg_c0u","time":{"created":160,"completed":200}},
			 "parts":[{"id":"prt_old","sessionID":"ses_c","messageID":"msg_c0","type":"text","text":"old","time":{"start":161,"end":162}}]},
			{"info":{"id":"msg_c2u","sessionID":"ses_c","role":"user","time":{"created":1001}},
			 "parts":[{"id":"prt_c2p","sessionID":"ses_c","messageID":"msg_c2u","type":"text","text":"go on"}]},
			{"info":{"id":"msg_c2","sessionID":"ses_c","role":"assistant","parentID":"msg_c2u","time":{"created":1002,"completed":1100}},
			 "parts":[{"id":"prt_new","sessionID":"ses_c","messageID":"msg_c2","type":"text","text":"hello","time":{"start":1003,"end":1050}}]}]`,
		"/permission":     `[]`,
		"/session/status": `{"ses_root":{"type":"busy"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := stored[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	cl := newClient(srv.URL, "", t.TempDir())
	var history []ocMessageWithParts
	if err := cl.call(context.Background(), http.MethodGet, "/session/ses_root/message", nil, &history); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	a := New()
	a.out, a.session = rec, "ses_root"
	a.mu.Lock()
	a.seed(history[:2])
	a.mu.Unlock()
	a.handleMessage(0, history[2].Info)
	a.handleMessage(0, history[3].Info)
	a.handlePart(0, mustPart(t, taskPart("prt_task2", "msg_a2", "pending", "", 0, 0)))
	a.handlePart(0, mustPart(t, taskPart("prt_task2", "msg_a2", "running", "ses_c", 1000, 0)))
	a.handleMessage(0, ocMessage{ID: "msg_c2u", SessionID: "ses_c", Role: "user"})
	a.handleMessage(0, ocMessage{ID: "msg_c2", SessionID: "ses_c", Role: "assistant", ParentID: "msg_c2u"})
	a.handlePart(0, mustPart(t, `{"id":"prt_new","sessionID":"ses_c","messageID":"msg_c2","type":"text","text":"","time":{"start":1003}}`))
	a.handleDelta(0, "prt_new", "hel")
	// The stream drops here; the part ends while none is open.
	a.reconcile(context.Background(), 0, cl)

	if got := placeOf(rec, "prt_new"); got != "msg_a2 prt_task2" {
		t.Fatalf("the resumed subagent's part is in %q", got)
	}
	if n := rec.count(agent.TypePart, func(ev agent.Event) bool { return ev.PartID == "prt_old" || ev.PartID == "prt_c0p" }); n != 0 {
		t.Fatalf("the catch-up wrote %d parts of the subagent's history", n)
	}
	rec.view(func(p *agent.Projection, _ []agent.Event) {
		if part := p.Part("prt_new"); part == nil || part.State != agent.PartDone || part.Text != "hello" {
			t.Fatalf("the resumed subagent's part after the catch-up: %+v", part)
		}
	})
}

// A rebuilt history shows inside each task part what the subagent did for
// it, when a later task resumed the same subagent session.
func TestARebuildSplitsAResumedSubagentBetweenItsTasks(t *testing.T) {
	var root, sub []ocMessageWithParts
	decode := func(dst *[]ocMessageWithParts, raw string) {
		if err := json.Unmarshal([]byte(raw), dst); err != nil {
			t.Fatal(err)
		}
	}
	decode(&root, `[
		{"info":{"id":"msg_u1","sessionID":"ses_root","role":"user","time":{"created":90}},"parts":[]},
		{"info":{"id":"msg_a1","sessionID":"ses_root","role":"assistant","parentID":"msg_u1","time":{"created":95,"completed":250}},
		 "parts":[`+taskPart("prt_task1", "msg_a1", "completed", "ses_c", 100, 200)+`]},
		{"info":{"id":"msg_u2","sessionID":"ses_root","role":"user","time":{"created":290}},"parts":[]},
		{"info":{"id":"msg_a2","sessionID":"ses_root","role":"assistant","parentID":"msg_u2","time":{"created":295,"completed":450}},
		 "parts":[`+taskPart("prt_task2", "msg_a2", "completed", "ses_c", 300, 400)+`]}]`)
	decode(&sub, `[
		{"info":{"id":"msg_c1u","sessionID":"ses_c","role":"user","time":{"created":110}},"parts":[]},
		{"info":{"id":"msg_c1","sessionID":"ses_c","role":"assistant","time":{"created":120,"completed":190},"tokens":{"input":7}},
		 "parts":[{"id":"prt_c1","sessionID":"ses_c","messageID":"msg_c1","type":"text","text":"first","time":{"start":121,"end":122}}]},
		{"info":{"id":"msg_c2u","sessionID":"ses_c","role":"user","time":{"created":310}},"parts":[]},
		{"info":{"id":"msg_c2","sessionID":"ses_c","role":"assistant","time":{"created":320,"completed":390},"tokens":{"input":11}},
		 "parts":[{"id":"prt_c2","sessionID":"ses_c","messageID":"msg_c2","type":"text","text":"second","time":{"start":321,"end":322}}]}]`)
	events := historyEvents(root, func(session string) []ocMessageWithParts {
		if session == "ses_c" {
			return sub
		}
		return nil
	})
	places := map[string][]string{}
	var inputs []int64
	for _, ev := range events {
		switch ev.Type {
		case agent.TypePart:
			places[ev.PartID] = append(places[ev.PartID], ev.MessageID+" "+ev.ParentPartID)
		case agent.TypeTurnEnded:
			inputs = append(inputs, ev.Usage.Input)
		}
	}
	for part, want := range map[string]string{"prt_c1": "msg_a1 prt_task1", "prt_c2": "msg_a2 prt_task2"} {
		if got := places[part]; len(got) != 1 || got[0] != want {
			t.Errorf("%s is in %q, want %q", part, got, want)
		}
	}
	if len(inputs) != 2 || inputs[0] != 7 || inputs[1] != 11 {
		t.Errorf("the turns' input %v, want [7 11]", inputs)
	}
}
