package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
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
	got := renderReport(entries, now)
	if n := len([]rune(got)); n > reportMaxRunes {
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
