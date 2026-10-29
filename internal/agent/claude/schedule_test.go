//go:build !windows

package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// openers is the notice parts the turns of the CLI's own opened with, as
// "code: text".
func openers(t *testing.T, p *agent.Projection) []string {
	t.Helper()
	var out []string
	for _, m := range p.Messages {
		if m.Role != agent.RoleAssistant {
			continue
		}
		for _, n := range partsOf(m, agent.PartNotice) {
			var c struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(n.Content, &c)
			out = append(out, c.Code+": "+n.Text)
		}
	}
	return out
}

// A wakeup the model schedules is pending while the agent rests - which
// keeps the host from parking it - and the turn it starts says it woke up
// as scheduled, with the prompt; the rebuilt history says the same.
func TestAWakeupIsPendingAndItsTurnSaysWhy(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/wakeup 1")
	r.turnsEnded(t, 1)
	if a.Scheduled() != 1 {
		t.Fatalf("scheduled = %d", a.Scheduled())
	}
	r.view(func(p *agent.Projection) {
		s := p.Summary()
		if len(p.Schedules) != 1 || p.Schedules[0].Kind != agent.ScheduleWakeup || p.Schedules[0].Prompt != "Check on the build" ||
			p.Schedules[0].Reason != "the build takes a while" || s.WakesAt == 0 || s.Schedules != 1 || s.Activity != agent.ActivityIdle {
			t.Fatalf("schedules %+v, summary %+v", p.Schedules, s)
		}
		call := content(t, partsOf(p.Messages[0], agent.PartTool)[0])
		if call.Tool != "ScheduleWakeup" || call.Summary != "the build takes a while" {
			t.Fatalf("the call = %+v", call)
		}
	})
	r.turnsEnded(t, 2)
	if a.Scheduled() != 0 {
		t.Fatalf("scheduled after the wakeup = %d", a.Scheduled())
	}
	var conv string
	r.view(func(p *agent.Projection) {
		conv = p.ConversationID
		if len(p.Schedules) != 0 {
			t.Fatalf("schedules after the wakeup: %+v", p.Schedules)
		}
		if got := openers(t, p); len(got) != 1 || got[0] != "wakeup: Woke up as scheduled: Check on the build" {
			t.Fatalf("openers = %q", got)
		}
		last := lastAssistant(p)
		if parts := last.Parts; len(parts) < 2 || parts[0].Kind != agent.PartNotice || !strings.Contains(parts[len(parts)-1].Text, "Woke up for") {
			t.Fatalf("the wakeup's turn: %+v", parts)
		}
	})
	events, err := a.Rebuild(agent.StartSpec{Env: e.vars, ResumeID: conv})
	if err != nil {
		t.Fatal(err)
	}
	p := agent.Fold(number(events))
	if got := openers(t, p); len(got) != 1 || got[0] != "wakeup: Woke up as scheduled: Check on the build" {
		t.Fatalf("rebuilt openers = %q:\n%s", got, dump(events))
	}
	if n := count(events, agent.TypeTurnStarted); n != 2 {
		t.Fatalf("rebuilt turns = %d", n)
	}
}

// A one-shot cron job leaves the list when it runs; a recurring one stays,
// without the time it no longer runs at. Each run opens its turn with the
// prompt.
func TestCronJobsRunAndSayWhy(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/cron 0.6 recurring")
	r.turnsEnded(t, 1)
	r.view(func(p *agent.Projection) {
		if len(p.Schedules) != 1 || p.Schedules[0].Kind != agent.ScheduleCron || !p.Schedules[0].Recurring ||
			p.Schedules[0].Cron != "Every minute" || p.Schedules[0].At == 0 {
			t.Fatalf("schedules = %+v", p.Schedules)
		}
	})
	r.turnsEnded(t, 2)
	r.view(func(p *agent.Projection) {
		if len(p.Schedules) != 1 || p.Schedules[0].At != 0 {
			t.Fatalf("a recurring job after its run = %+v", p.Schedules)
		}
		if got := openers(t, p); len(got) != 1 || got[0] != "cron: A scheduled task ran: Run the nightly check" {
			t.Fatalf("openers = %q", got)
		}
	})
	send(t, a, "m2", "/cron 0.3")
	r.turnsEnded(t, 3)
	r.view(func(p *agent.Projection) {
		if len(p.Schedules) != 2 {
			t.Fatalf("two jobs = %+v", p.Schedules)
		}
	})
	// The once job, and the recurring one's last two runs (in any order).
	r.turnsEnded(t, 6)
	r.view(func(p *agent.Projection) {
		if got := openers(t, p); len(got) != 4 {
			t.Fatalf("openers = %q", got)
		}
		for _, sc := range p.Schedules {
			if !sc.Recurring {
				t.Fatalf("the once job is still listed: %+v", p.Schedules)
			}
		}
	})
}

// A background command's report and a Monitor's event - which has no frame
// of its own in the stream, only its line in the transcript - say what they
// were at the head of the turn they start.
func TestATasksReportAndAMonitorEventSayWhat(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/bg 0.3")
	r.turnsEnded(t, 2)
	send(t, a, "m2", "/monitor 0.5")
	r.turnsEnded(t, 4)
	var conv string
	want := []string{
		`task: Background command "sleep then echo" completed (exit code 0)`,
		`monitor: Monitor "watch the build": build passed`,
	}
	r.view(func(p *agent.Projection) {
		conv = p.ConversationID
		if got := openers(t, p); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("openers = %q", got)
		}
	})
	events, err := a.Rebuild(agent.StartSpec{Env: e.vars, ResumeID: conv})
	if err != nil {
		t.Fatal(err)
	}
	if got := openers(t, agent.Fold(number(events))); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rebuilt openers = %q", got)
	}
}

func TestNotificationOpeners(t *testing.T) {
	for _, c := range []struct{ text, code, words, detail string }{
		{"<task-notification>\n<task-id>b1</task-id>\n<summary>Monitor event: \"CI runs\"</summary>\n<event>run 4 completed\tsuccess</event>\n</task-notification>",
			"monitor", `Monitor "CI runs": run 4 completed` + "\tsuccess", "run 4 completed\tsuccess"},
		{"<task-notification>\n<status>completed</status>\n<summary>Agent \"Audit\" finished</summary>\n<result>All good.\nMore.</result>\n</task-notification>",
			"task", `Agent "Audit" finished`, "All good.\nMore."},
		{"<task-notification>\n<task-id>x</task-id>\n</task-notification>", "task", "A background task reported back.", ""},
	} {
		o := notificationOpener("u", c.text)
		if o.code != c.code || o.text != c.words || o.detail != c.detail {
			t.Errorf("%q: %+v", c.text, o)
		}
	}
	line := transcriptLine{Type: "user", UUID: "u", IsMeta: true, Message: json.RawMessage(`{"role":"user","content":"Another Claude session sent a message"}`)}
	line.Origin = &struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		From string `json:"from"`
		Body string `json:"body"`
	}{Kind: "peer", Name: "general-purpose", Body: "[Subagent hand-back] Done.\nDetails"}
	if o, ok := openerOf(line); !ok || o.code != "peer" || o.text != "A message from the agent “general-purpose”: [Subagent hand-back] Done. …" {
		t.Fatalf("peer = %+v %v", o, ok)
	}
}
