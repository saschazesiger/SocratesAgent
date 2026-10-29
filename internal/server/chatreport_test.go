package server

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// A chat's record is its timeline with clock times and measured durations:
// two turns, one ended and one running, each call with how long it took or
// how long it has run, and the clock says the same in numbers.
func TestAChatsRecordIsItsTimeline(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	now := base.Add(30 * time.Minute)
	bash := func(cmd string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"tool": "Bash", "input": map[string]any{"command": cmd}})
		return raw
	}
	p := agent.NewProjection()
	for _, ev := range []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, Text: "Fix the build", At: at(0)},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1", At: at(time.Second)},
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", At: at(2 * time.Second)},
		{Type: agent.TypePart, MessageID: "a1", PartID: "p1", Kind: agent.PartTool, State: agent.PartRunning,
			Content: bash("go build ./..."), At: at(3 * time.Second)},
		{Type: agent.TypePart, MessageID: "a1", PartID: "p1", Kind: agent.PartTool, State: agent.PartDone,
			Content: bash("go build ./..."), At: at(63 * time.Second)},
		{Type: agent.TypePart, MessageID: "a1", PartID: "p2", Kind: agent.PartText, State: agent.PartDone,
			Text: "The build works again.", At: at(70 * time.Second)},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted, At: at(10 * time.Minute)},
		{Type: agent.TypeMessage, MessageID: "u2", Role: agent.RoleUser, Text: "Now run the tests", At: at(20 * time.Minute)},
		{Type: agent.TypeTurnStarted, TurnID: "t2", MessageID: "u2", At: at(20 * time.Minute)},
		{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, TurnID: "t2", At: at(21 * time.Minute)},
		{Type: agent.TypePart, MessageID: "a2", PartID: "p3", Kind: agent.PartTool, State: agent.PartRunning,
			Content: bash("go test ./..."), At: at(25 * time.Minute)},
	} {
		p.Apply(ev)
	}
	report := readChatReport(p, now)
	c := report.Clock
	if c.Turns != 2 || c.FirstMessage != at(0) || c.TurnStarted != at(20*time.Minute) ||
		c.CallStarted != at(25*time.Minute) || c.LastEnded != at(10*time.Minute) {
		t.Fatalf("the clock: %+v", c)
	}
	// The ended turn ran from its start to its end, the running one until now.
	if c.Worked != (9*time.Minute + 59*time.Second + 10*time.Minute).Milliseconds() {
		t.Fatalf("worked %d ms", c.Worked)
	}
	lines := strings.Split(report.Record, "\n")
	want := []string{
		"[2026-10-25 12:00:00] Person: Fix the build",
		"[2026-10-25 12:00:01] --- Turn 1 started",
		"[2026-10-25 12:00:03] Call: Running go build ./... - took 1m 00s",
		"[2026-10-25 12:01:10] Agent: The build works again.",
		"[2026-10-25 12:10:00] --- Turn 1 ended after 9m 59s, completed",
		"[2026-10-25 12:20:00] --- Turn 2 started",
		"[2026-10-25 12:20:00] Person: Now run the tests",
		"[2026-10-25 12:25:00] Call: Running go test ./... (still running) - running for 5m 00s",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the record:\n%s\nwant:\n%s", report.Record, strings.Join(want, "\n"))
	}
	facts := clockFacts(c, at(-time.Minute), now)
	for _, w := range []string{"The chat was opened: 2026-10-25 11:59:00 (31m 00s ago)", "Turns so far: 2, working 19m 59s in all",
		"The current turn started: 2026-10-25 12:20:00 (10m 00s ago)", "Running now: Running go test ./..., for 5m 00s"} {
		if !strings.Contains(facts, w) {
			t.Fatalf("the facts have no %q:\n%s", w, facts)
		}
	}
	if strings.Contains(facts, "The last turn ended") {
		t.Fatalf("a running turn is not said to rest after the last one:\n%s", facts)
	}
}

// A record too long for the analysis keeps the first message and the recent
// end, and says how much it left out.
func TestALongRecordKeepsItsStartAndItsEnd(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	entries := []reportEntry{{at: now.UnixMilli(), text: "Person: the task", keep: true}}
	long := strings.Repeat("x", 1000)
	for i := 0; i < 400; i++ {
		entries = append(entries, reportEntry{at: now.UnixMilli() + int64(i), text: "Agent: " + long})
	}
	entries = append(entries, reportEntry{at: now.UnixMilli() + 1000, text: "Agent: the newest"})
	got := renderReport(entries, now, 100000)
	if n := len([]rune(got)); n > 100000 {
		t.Fatalf("the record is %d runes", n)
	}
	lines := strings.Split(got, "\n")
	if !strings.HasSuffix(lines[0], "Person: the task") || !strings.Contains(lines[1], "earlier entries left out") ||
		!strings.HasSuffix(lines[len(lines)-1], "Agent: the newest") {
		t.Fatalf("the cut record starts %q, %q and ends %q", lines[0], lines[1], lines[len(lines)-1])
	}
}

func TestSpanWords(t *testing.T) {
	for ms, want := range map[int64]string{-5: "0s", 40_000: "40s", 725_000: "12m 05s",
		12_000_000: "3h 20m", 187_200_000: "2d 4h"} {
		if got := span2words(ms); got != want {
			t.Errorf("span2words(%d) = %q, want %q", ms, got, want)
		}
	}
}

// A request is the person's message that found the agent resting, and all
// the work it took: a message sent while a turn ran is more of the same
// request, and a turn the agent started by itself (a background task that
// reported back) belongs to the request before it. Time spent waiting for
// the person's answer is not work.
func TestARequestIsAllTheWorkItTook(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	p := agent.NewProjection()
	for _, ev := range []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, Text: "Fix the build", At: at(0)},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1", At: at(time.Second)},
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", At: at(2 * time.Second)},
		{Type: agent.TypePart, MessageID: "a1", PartID: "p1", Kind: agent.PartTool, State: agent.PartDone,
			Content: json.RawMessage(`{"tool":"Bash","input":{"command":"make"}}`), At: at(3 * time.Second)},
		{Type: agent.TypePart, MessageID: "a1", PartID: "p0", Kind: agent.PartText, State: agent.PartDone,
			Text: "Fixed.", At: at(10 * time.Minute)},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted, At: at(10 * time.Minute)},
		{Type: agent.TypeMessage, MessageID: "u2", Role: agent.RoleUser, Text: "Now add the docs", At: at(20 * time.Minute)},
		{Type: agent.TypeTurnStarted, TurnID: "t2", MessageID: "u2", At: at(20 * time.Minute)},
		{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, TurnID: "t2", At: at(21 * time.Minute)},
		// Said while the turn runs: the same request.
		{Type: agent.TypeMessage, MessageID: "u3", Role: agent.RoleUser, Text: "and a test", At: at(22 * time.Minute)},
		{Type: agent.TypeAsk, AskID: "q1", TurnID: "t2", At: at(23 * time.Minute),
			Content: json.RawMessage(`{"questions":[{"question":"Which file?"}]}`)},
		{Type: agent.TypeAskAnswered, AskID: "q1", At: at(28 * time.Minute), Content: json.RawMessage(`"README"`)},
		{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeCompleted, At: at(30 * time.Minute)},
		// Woken by a background task: still the docs.
		{Type: agent.TypeTurnStarted, TurnID: "t3", At: at(35 * time.Minute)},
		{Type: agent.TypeMessage, MessageID: "a3", Role: agent.RoleAssistant, TurnID: "t3", At: at(35 * time.Minute)},
		{Type: agent.TypeMessage, MessageID: "u4", Role: agent.RoleUser, Text: "and push it", State: agent.MessageQueued,
			At: at(36 * time.Minute)},
	} {
		p.Apply(ev)
	}
	now := base.Add(40 * time.Minute)
	report := readChatReport(p, now)
	c := report.Clock
	w := c.Work
	if w.Request != "Now add the docs" || w.Start != at(20*time.Minute) || !w.Running || w.Turns != 2 ||
		w.Waited != (5*time.Minute).Milliseconds() || w.Working(c.Now) != (15*time.Minute).Milliseconds() {
		t.Fatalf("the current request: %+v (working %d)", w, w.Working(c.Now))
	}
	if len(c.Earlier) != 1 || c.Earlier[0].Request != "Fix the build" || c.Earlier[0].Calls != 1 ||
		c.Earlier[0].Working(c.Now) != (10*time.Minute).Milliseconds() {
		t.Fatalf("the earlier requests: %+v", c.Earlier)
	}
	if c.Queued != 1 || !strings.Contains(report.Record, "it has not read this yet): and push it") {
		t.Fatalf("a message the agent has not taken up is work still to come: %d\n%s", c.Queued, report.Record)
	}
	facts := clockFacts(c, 0, now)
	for _, want := range []string{"sent 2026-10-25 12:20:00 (20m 00s ago): Now add the docs",
		"worked on it so far: 15m 00s (not counting 5m 00s waiting for the person's answers), in 2 turns with 0 calls",
		"- 10m 00s, 1 turn, 1 call: Fix the build", "Messages from the person the agent has not taken up yet: 1"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("the facts have no %q:\n%s", want, facts)
		}
	}
}

// The estimate the page shows is the geometric mean of the model's number,
// the time the request has run and that time carried forward by the share
// done, with a factor of two either way; none when the model says nothing
// is left to wait for.
func TestTheEstimateIsCalibratedByTheMeasuredTime(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	clock := chatClock{Now: 60 * 60000, State: termux.StateBusy, Work: workSpan{Start: 0, Running: true}}
	// 7.5 min (model), 60 min (run so far), 60 min (half done): cbrt(7.5*60*60) = 30.
	got, ok := statusETA(chatAnalysis{ETAMinutes: f(7.5), Progress: f(50)}, clock)
	if !ok || got.minutes != 30 || got.low != 15 || got.high != 60 {
		t.Fatalf("estimate %+v %v", got, ok)
	}
	// Without a share, the model and the time so far: sqrt(15*60) = 30.
	if got, _ := statusETA(chatAnalysis{ETAMinutes: f(15)}, clock); got.minutes != 30 {
		t.Fatalf("without progress %+v", got)
	}
	if _, ok := statusETA(chatAnalysis{Progress: f(100)}, clock); ok {
		t.Fatal("no estimate when the model has none")
	}
	if _, ok := statusETA(chatAnalysis{ETAMinutes: f(math.NaN())}, clock); ok {
		t.Fatal("no estimate from NaN")
	}
	// Resting with nothing behind it is done, whatever the model says.
	if _, ok := statusETA(chatAnalysis{ETAMinutes: f(0), Progress: f(100)},
		chatClock{Now: clock.Now, State: termux.StateIdle, Work: workSpan{End: clock.Now}}); ok {
		t.Fatal("an estimate for a chat that is done")
	}
	// Just started: never less than a minute.
	if got, _ := statusETA(chatAnalysis{ETAMinutes: f(0), Progress: f(99)}, chatClock{State: termux.StateBusy}); got.minutes != 1 || got.low != 1 || got.high != 2 {
		t.Fatalf("at the start %+v", got)
	}
	for _, c := range []struct {
		minutes float64
		lang    string
		want    string
	}{
		{1, "en", "about a minute more"},
		{25, "en", "about 25 minutes more"},
		{60, "en", "about an hour more"},
		{80, "en", "about an hour and 20 minutes more"},
		{150, "en", "about 2 hours and 30 minutes more"},
		{25, "de", "noch etwa 25 Minuten."},
		{120, "de-CH", "noch etwa 2 Stunden."},
		{61, "de", "noch etwa eine Stunde und eine Minute."},
		{1, "de", "noch etwa eine Minute."},
		{61, "en", "about an hour and 1 minute more"},
	} {
		if got := etaWords(statusEstimate{minutes: c.minutes}, c.lang); !strings.Contains(got, c.want) {
			t.Errorf("etaWords(%v, %s) = %q, want %q", c.minutes, c.lang, got, c.want)
		}
	}
}

// A question that stays open after the turn that asked it ended is not
// counted past the end of the work, two questions open at once count once,
// and a request whose turn left no trace ends where the next one starts.
func TestWaitsStayInsideTheirRequest(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	ask := json.RawMessage(`{"questions":[{"question":"Which?"}]}`)
	p := agent.NewProjection()
	for _, ev := range []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u0", Role: agent.RoleUser, Text: "Fails at once", At: at(-time.Hour)},
		{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, Text: "Do it", At: at(0)},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1", At: at(time.Second)},
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", At: at(time.Second)},
		{Type: agent.TypeAsk, AskID: "q1", TurnID: "t1", At: at(4 * time.Minute), Content: ask},
		{Type: agent.TypeAsk, AskID: "q2", TurnID: "t1", At: at(5 * time.Minute), Content: ask},
		{Type: agent.TypeAskAnswered, AskID: "q2", At: at(6 * time.Minute), Content: json.RawMessage(`"A"`)},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted, At: at(10 * time.Minute)},
	} {
		p.Apply(ev)
	}
	c := readChatReport(p, base.Add(40*time.Minute)).Clock
	// q1 is open from 4m on and q2 inside it: 6 minutes of waiting, until
	// the turn ended at 10m - not until now.
	if w := c.Work; w.Waited != (6*time.Minute).Milliseconds() || w.Working(c.Now) != (4*time.Minute).Milliseconds() {
		t.Fatalf("the request: %+v, working %d", w, w.Working(c.Now))
	}
	if len(c.Earlier) != 1 || c.Earlier[0].Working(c.Now) != time.Hour.Milliseconds() {
		t.Fatalf("the request with no turn: %+v", c.Earlier)
	}
}

// Only the person starts a request: a message from another chat or Socrates'
// own "go on" after an account switch is more of the work that runs, and
// while a subagent or a background task runs between turns, that work goes
// on until now.
func TestOnlyThePersonStartsARequest(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	p := agent.NewProjection()
	for _, ev := range []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, Text: "Migrate it", At: at(0)},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1", At: at(time.Second)},
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, TurnID: "t1", At: at(time.Second)},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted, At: at(time.Hour)},
		{Type: agent.TypeMessage, MessageID: "u2", Role: agent.RoleUser, ClientID: "x:continue",
			Text: "Switching the account restarted you", At: at(2 * time.Hour)},
		{Type: agent.TypeTurnStarted, TurnID: "t2", MessageID: "u2", At: at(2 * time.Hour)},
		{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, TurnID: "t2", At: at(2 * time.Hour)},
		{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeCompleted, At: at(3 * time.Hour)},
		{Type: agent.TypeMessage, MessageID: "u3", Role: agent.RoleUser, ClientID: "chat-tools-ab12",
			Text: "[A message from the chat “Other”] hi", At: at(4 * time.Hour)},
		{Type: agent.TypeTurnStarted, TurnID: "t3", MessageID: "u3", At: at(4 * time.Hour)},
		{Type: agent.TypeMessage, MessageID: "a3", Role: agent.RoleAssistant, TurnID: "t3", At: at(4 * time.Hour)},
		{Type: agent.TypeTurnEnded, TurnID: "t3", Outcome: agent.OutcomeCompleted, At: at(4*time.Hour + time.Minute)},
		{Type: agent.TypeTasks, Count: 1, Agents: 1, At: at(4*time.Hour + time.Minute)},
	} {
		p.Apply(ev)
	}
	report := readChatReport(p, base.Add(5*time.Hour))
	c := report.Clock
	if c.Work.Request != "Migrate it" || len(c.Earlier) != 0 || c.Work.Turns != 3 || !c.Work.Running ||
		c.Work.Working(c.Now) != (5*time.Hour).Milliseconds() {
		t.Fatalf("the request: %+v, earlier %+v", c.Work, c.Earlier)
	}
	for _, want := range []string{"Socrates, on the person's behalf: Switching the account", "Another chat: [A message"} {
		if !strings.Contains(report.Record, want) {
			t.Fatalf("the record has no %q:\n%s", want, report.Record)
		}
	}
}
