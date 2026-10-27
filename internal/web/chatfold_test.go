package web

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The chat view folds a chat's events itself (chatfold.js) and must end up
// holding what the server's projection holds (agent.Projection): a page that
// replays the journal from any point, with live deltas mixed in, draws the
// conversation the server would describe. The rules that are easy to get
// subtly different are all in this journal: deltas that overlap and that
// leave a gap, counted in UTF-16 (a character outside the BMP is two),
// nesting by parent_part_id with and without the parent, a part replaced
// whole that keeps its place and its children, a second answer that loses,
// an event that does not move the seq forward, and a turn that ends
// interrupted with things still streaming.
func TestTheBrowserFoldsAChatAsTheServerDoes(t *testing.T) {
	events := parityJournal()
	// A repeat of an event already applied - a replay that overlaps the live
	// tail - changes nothing.
	again := events[6]
	again.Text = "SHOULD NOT APPEAR"
	events = append(events, again)

	want := foldedView(agent.Fold(events))
	got := foldInBrowser(t, events)
	if got != want {
		t.Fatalf("the browser's fold differs from the server's:\nbrowser %s\nserver  %s", got, want)
	}
}

// parityJournal is the journal the two folds are held to: the rules that
// are easy to get subtly different, all in one chat.
func parityJournal() []agent.Event {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	events := []agent.Event{
		{Type: agent.TypeMessage, MessageID: "m1", Role: agent.RoleUser, State: agent.MessagePending, ClientID: "c1", Text: "hi"},
		{Type: agent.TypeMessageState, MessageID: "m1", State: agent.MessageDelivered},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "m1"},
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t1"},
		{Type: agent.TypePart, MessageID: "a1", PartID: "p1", Kind: agent.PartText, State: agent.PartStreaming},
		{Type: agent.TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 0, Text: "Hel"},
		{Type: agent.TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 1, Text: "ello wörld 😀"},
		{Type: agent.TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 99, Text: "a gap"},
		{Type: agent.TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 12, Text: "😀!"},
		{Type: agent.TypePart, MessageID: "a1", PartID: "g", Kind: agent.PartTool, State: agent.PartRunning, Content: raw(`{"tool":"Task"}`)},
		{Type: agent.TypePart, PartID: "g1", ParentPartID: "g", Kind: agent.PartText, State: agent.PartStreaming, Text: "inside"},
		{Type: agent.TypePartDelta, PartID: "g1", Offset: 6, Text: " and more"},
		{Type: agent.TypePart, PartID: "g2", ParentPartID: "g1", Kind: agent.PartTool, State: agent.PartRunning},
		{Type: agent.TypePart, MessageID: "a1", PartID: "lost", ParentPartID: "nowhere", Kind: agent.PartText, State: agent.PartDone, Text: "orphan"},
		{Type: agent.TypeAsk, AskID: "k1", TurnID: "t1", Payload: raw(`{"questions":[{"id":"q","question":"?"}]}`)},
		{Type: agent.TypeAskAnswered, AskID: "k1", Answer: raw(`{"answers":{"q":{"selected":["a"]}}}`), ClientID: "first", Device: "phone"},
		{Type: agent.TypeAskAnswered, AskID: "k1", Answer: raw(`{"answers":{"q":{"selected":["b"]}}}`), ClientID: "second", Device: "laptop"},
		{Type: agent.TypeAskDelivered, AskID: "k1", Via: agent.ViaTool},
		{Type: agent.TypePart, MessageID: "a1", PartID: "g", Kind: agent.PartTool, State: agent.PartDone, Text: "found it", Content: raw(`{"tool":"Task","summary":"x"}`)},
		{Type: agent.TypeAsk, AskID: "k2", TurnID: "t1", Payload: raw(`{"questions":[]}`)},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeInterrupted},
	}
	for i := range events {
		events[i].Seq = int64(i + 1)
		events[i].At = int64(1000 + i)
	}
	return events
}

// Claude's word that it goes on without another organization's thinking is
// a hint on the switch that caused it, not a line of its own; any other
// notice stays one.
func TestTheForeignThinkingNoticeGoesOnTheSwitch(t *testing.T) {
	events := []agent.Event{
		{Type: agent.TypeAccountSwitched, From: "a-1", To: "a-2", Reason: "testing"},
		{Type: agent.TypeMessage, MessageID: "m1", Role: agent.RoleUser, State: agent.MessageDelivered, Text: "go on"},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "m1", Account: "a-2"},
		{Type: agent.TypeNotice, Code: agent.NoticeForeignThinking, Text: "set aside"},
		{Type: agent.TypeNotice, Code: "compacted", Text: "compacted"},
	}
	for i := range events {
		events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
	}
	got := runFold(t, events, `chat.timeline.filter((e) => e.type === 'line').map((e) => e.line.kind + ':' + (e.line.hint || e.line.text || ''))`)
	if got != `["account:set aside","notice:compacted"]` {
		t.Fatalf("lines %s", got)
	}
}

// The usage windows are the account's the chat is on: a switch, or a turn on
// another account, lets go of the readings of the one before - its used-up
// week is no limit of the next one - and a limited turn keeps when the
// agent said its limit renews. The browser folds it as the server does.
func TestTheBrowserKeepsTheLimitsOfTheAccountItIsOn(t *testing.T) {
	events := []agent.Event{
		{Type: agent.TypeTurnStarted, TurnID: "t1", Account: "a-1"},
		{Type: agent.TypeRateLimit, Window: "seven_day", Used: 1, ResetsAt: 9000},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeLimited, Window: "seven_day", ResetsAt: 9000},
		{Type: agent.TypeAccountSwitched, From: "a-1", To: "a-2"},
		{Type: agent.TypeTurnStarted, TurnID: "t2", Account: "a-2"},
		{Type: agent.TypeRateLimit, Window: "five_hour", Used: 1, ResetsAt: 5000},
		{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeLimited, Window: "five_hour", ResetsAt: 5000},
		{Type: agent.TypeTurnStarted, TurnID: "t3", Account: "a-3"},
	}
	for i := range events {
		events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
	}
	check := func(n int, windows string, renews int64, window string) {
		t.Helper()
		p := agent.Fold(events[:n])
		keys := make([]string, 0, len(p.RateLimits))
		for k := range p.RateLimits {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if server := strings.Join(keys, ","); server != windows || p.LastTurn.RenewsAt != renews || p.LastTurn.Window != window {
			t.Fatalf("after %d events the server has %q, %d, %q", n, server, p.LastTurn.RenewsAt, p.LastTurn.Window)
		}
		got := runFold(t, events[:n], `[Object.keys(chat.rateLimits).sort().join(','), chat.lastTurn.renewsAt, chat.lastTurn.window]`)
		if want := fmt.Sprintf("[%q,%d,%q]", windows, renews, window); got != want {
			t.Fatalf("after %d events the browser has %s, the server %s", n, got, want)
		}
	}
	check(3, "seven_day", 9000, "seven_day")
	check(7, "five_hour", 5000, "five_hour")
	check(8, "", 5000, "five_hour")
}

// Background tasks are the process's own: once it is parked, stopped or
// dead, or a new host starts, the browser counts none, as the server does,
// whatever the last count said.
func TestNoBackgroundTaskOutlivesItsProcess(t *testing.T) {
	events := []agent.Event{
		{Type: agent.TypeProcess, State: agent.ProcessReady},
		{Type: agent.TypeTasks, Count: 3, Agents: 1},
		{Type: agent.TypeProcess, State: agent.ProcessParked},
		{Type: agent.TypeProcess, State: agent.ProcessStarting},
		{Type: agent.TypeProcess, State: agent.ProcessReady},
		{Type: agent.TypeTasks, Count: 2},
		{Type: agent.TypeProcess, State: agent.ProcessFailed},
	}
	for i := range events {
		events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
	}
	for n, want := range map[int]int{2: 3, 3: 0, 5: 0, 6: 2, 7: 0} {
		if p := agent.Fold(events[:n]); p.Tasks != want || (want == 0 && p.Agents != 0) {
			t.Fatalf("after %d events the server counts %d tasks, %d agents; want %d", n, p.Tasks, p.Agents, want)
		}
		if got := runFold(t, events[:n], `chat.tasks`); got != strconv.Itoa(want) {
			t.Fatalf("after %d events the browser counts %s tasks; want %d", n, got, want)
		}
	}
}

// How a turn ended is said right under that turn, above what the person sent
// while it ran: the journal has a message queued during the turn before the
// turn's end, and "Interrupted" drawn under it read as if that message had
// been cut short. It does not climb past the turn's own prompt (a turn
// stopped before a word), and a limit stays the newest word, which is when
// its card offers another account, until the message is taken up.
func TestAnEndedTurnsLineStandsAboveWhatWasQueuedDuringIt(t *testing.T) {
	order := `chat.timeline.map((e) => e.type === 'line' ? e.line.kind + ':' + (e.line.outcome || '') : e.id)`
	for _, c := range []struct {
		name    string
		answer  bool
		outcome string
		want    string
	}{
		{"interrupted with an answer", true, agent.OutcomeInterrupted, `["m1","a1","turn:interrupted","m2","a2"]`},
		{"interrupted before a word", false, agent.OutcomeInterrupted, `["m1","turn:interrupted","m2","a2"]`},
		{"failed", true, agent.OutcomeFailed, `["m1","a1","turn:failed","m2","a2"]`},
		{"limited", true, agent.OutcomeLimited, `["m1","a1","turn:limited","m2","a2"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			events := []agent.Event{
				{Type: agent.TypeMessage, MessageID: "m1", Role: agent.RoleUser, State: agent.MessageDelivered, Text: "/slow 20"},
				{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "m1"},
			}
			if c.answer {
				events = append(events,
					agent.Event{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t1"},
					agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: "p1", Kind: agent.PartText, State: agent.PartStreaming, Text: "s1 s2"})
			}
			events = append(events,
				agent.Event{Type: agent.TypeMessage, MessageID: "m2", Role: agent.RoleUser, State: agent.MessageQueued, Text: "queued while working"},
				agent.Event{Type: agent.TypeCommand, Op: agent.OpInterrupt, ClientID: "stop"},
				agent.Event{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: c.outcome, Text: "it ended"},
				agent.Event{Type: agent.TypeMessageState, MessageID: "m2", State: agent.MessageDelivered},
				agent.Event{Type: agent.TypeTurnStarted, TurnID: "t2", MessageID: "m2"},
				agent.Event{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, State: agent.MessageDone, TurnID: "t2"},
				agent.Event{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeCompleted})
			for i := range events {
				events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
			}
			if got := runFold(t, events, order); got != c.want {
				t.Fatalf("timeline %s, want %s", got, c.want)
			}
		})
	}
}

// What the person sends while a turn runs stays the newest word until the
// agent takes it up: what the agent says and does meanwhile goes above it.
// Taken up into the running turn - Claude reads it at its next step, Codex
// a steered one, an answer sent as a message as well - it is the turn's
// from then on, and what the agent says after it is drawn under it: the
// answer it goes on with is cut there. What still waits stays under all of
// it, and a message taken up out of order goes up to where it was read.
func TestAMessageTakenUpDuringATurnHasWhatFollowsUnderIt(t *testing.T) {
	order := `chat.timeline.map((e) => e.type === 'line' ? e.line.kind : e.type === 'more' ? e.id + '#' + (e.segment + 1) : e.id)
  .concat(['a1 cut at ' + JSON.stringify(chat.messages.get('a1').cuts)])`
	part := func(id string) agent.Event {
		return agent.Event{Type: agent.TypePart, MessageID: "a1", PartID: id, Kind: agent.PartText, State: agent.PartDone, Text: id}
	}
	events := []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, State: agent.MessageDelivered, Text: "go"},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1"},
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t1"},
		part("p1"),
		{Type: agent.TypeMessage, MessageID: "m2", Role: agent.RoleUser, State: agent.MessagePending, Text: "also this"},
		{Type: agent.TypeMessageState, MessageID: "m2", State: agent.MessageQueued},
		part("p2"),
		{Type: agent.TypeNotice, Code: "x", Text: "a word on the way"},
		{Type: agent.TypeMessageState, MessageID: "m2", State: agent.MessageDelivered},
		part("p3"),
		{Type: agent.TypeMessage, MessageID: "m3", Role: agent.RoleUser, State: agent.MessagePending, AnswerTo: "k1", Text: "an answer"},
		{Type: agent.TypeMessage, MessageID: "m4", Role: agent.RoleUser, State: agent.MessagePending, Text: "later"},
		{Type: agent.TypeMessageState, MessageID: "m4", State: agent.MessageQueued},
		{Type: agent.TypeMessage, MessageID: "m5", Role: agent.RoleUser, State: agent.MessagePending, Text: "last"},
		{Type: agent.TypeMessageState, MessageID: "m5", State: agent.MessageQueued},
		part("p4"),
		{Type: agent.TypeMessageState, MessageID: "m3", State: agent.MessageDelivered},
		part("p5"),
		{Type: agent.TypeMessageState, MessageID: "m5", State: agent.MessageDelivered},
		part("p6"),
		{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, State: agent.MessageDone, TurnID: "t1"},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted},
		{Type: agent.TypeMessageState, MessageID: "m4", State: agent.MessageDelivered},
		{Type: agent.TypeTurnStarted, TurnID: "t2", MessageID: "m4"},
		{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, State: agent.MessageDone, TurnID: "t2"},
		{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeCompleted},
	}
	for i := range events {
		events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
	}
	want := `["u1","a1","notice","m2","a1#2","m3","a1#3","m5","a1#4","m4","a2","a1 cut at [2,4,5]"]`
	if got := runFold(t, events, order); got != want {
		t.Fatalf("timeline %s, want %s", got, want)
	}
}

// What an agent says after a question is drawn under the question's card.
// The answer of a turn is made when the turn starts, so a question that
// names no part (the scripted agent's, OpenCode's ask_user) cuts the turn's
// answers where they were, and what comes after it is a piece of its own
// under the card. A question that names its tool part (Claude's, Codex's)
// is drawn at that part, inside the answer, which is not cut; neither is an
// answer of another turn.
func TestAnAnswerGoesOnUnderTheQuestion(t *testing.T) {
	order := `chat.timeline.map((e) => e.type === 'ask' ? 'ask:' + e.id : e.type === 'more' ? e.id + '#' + (e.segment + 1) : e.id)
  .concat([...chat.messages.values()].map((m) => m.id + ' cut at ' + JSON.stringify(m.cuts)))`
	for _, c := range []struct {
		name    string
		payload string
		want    string
	}{
		{"no part", `{"questions":[{"id":"q","question":"?"}]}`,
			`["u0","a0","u1","a1","ask:k1","a1#2","u0 cut at []","a0 cut at []","u1 cut at []","a1 cut at [1]"]`},
		{"its tool part", `{"part_id":"call","questions":[{"id":"q","question":"?"}]}`,
			`["u0","a0","u1","a1","ask:k1","u0 cut at []","a0 cut at []","u1 cut at []","a1 cut at []"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			events := []agent.Event{
				{Type: agent.TypeMessage, MessageID: "u0", Role: agent.RoleUser, State: agent.MessageDelivered, Text: "hi"},
				{Type: agent.TypeTurnStarted, TurnID: "t0", MessageID: "u0"},
				{Type: agent.TypeMessage, MessageID: "a0", Role: agent.RoleAssistant, State: agent.MessageDone, TurnID: "t0"},
				{Type: agent.TypeTurnEnded, TurnID: "t0", Outcome: agent.OutcomeCompleted},
				{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, State: agent.MessageDelivered, Text: "/ask"},
				{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1"},
				{Type: agent.TypeMessage, MessageID: "a1", Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t1"},
				{Type: agent.TypePart, MessageID: "a1", PartID: "call", Kind: agent.PartTool, State: agent.PartRunning,
					Content: json.RawMessage(`{"tool":"mcp__socrates__ask_user"}`)},
				{Type: agent.TypeAsk, AskID: "k1", TurnID: "t1", Payload: json.RawMessage(c.payload)},
				{Type: agent.TypeAskAnswered, AskID: "k1", Answer: json.RawMessage(`{"answers":{}}`)},
				{Type: agent.TypePart, MessageID: "a1", PartID: "reply", Kind: agent.PartText, State: agent.PartDone, Text: "you answered"},
				{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeCompleted},
			}
			for i := range events {
				events[i].Seq, events[i].At = int64(i+1), int64(1000+i)
			}
			if got := runFold(t, events, order); got != c.want {
				t.Fatalf("timeline %s, want %s", got, c.want)
			}
		})
	}
}

// foldedView is what both folds are compared on: every message with its
// parts (and theirs), and every question.
func foldedView(p *agent.Projection) string {
	type part struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		State    string `json:"state"`
		Text     string `json:"text"`
		Children []part `json:"children"`
	}
	var parts func(list []*agent.Part) []part
	parts = func(list []*agent.Part) []part {
		out := []part{}
		for _, one := range list {
			out = append(out, part{ID: one.ID, Kind: one.Kind, State: one.State, Text: one.Text, Children: parts(one.Children)})
		}
		return out
	}
	type message struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		State string `json:"state"`
		Text  string `json:"text"`
		Parts []part `json:"parts"`
	}
	type ask struct {
		ID        string          `json:"id"`
		Answered  bool            `json:"answered"`
		Answer    json.RawMessage `json:"answer"`
		ByDevice  string          `json:"by_device"`
		Delivered string          `json:"delivered"`
	}
	view := struct {
		Messages []message `json:"messages"`
		Asks     []ask     `json:"asks"`
		Open     []string  `json:"open"`
		Running  bool      `json:"running"`
		Outcome  string    `json:"outcome"`
	}{Messages: []message{}, Asks: []ask{}, Open: []string{}, Running: p.Turn != nil}
	for _, m := range p.Messages {
		view.Messages = append(view.Messages, message{ID: m.ID, Role: m.Role, State: m.State, Text: m.Text, Parts: parts(m.Parts)})
	}
	for _, a := range p.Asks {
		answer := a.Answer
		if answer == nil {
			answer = json.RawMessage("null")
		}
		view.Asks = append(view.Asks, ask{ID: a.ID, Answered: a.Answered, Answer: answer, ByDevice: a.ByDevice, Delivered: a.Delivered})
	}
	for _, a := range p.OpenAsks() {
		view.Open = append(view.Open, a.ID)
	}
	if p.LastTurn != nil {
		view.Outcome = p.LastTurn.Outcome
	}
	out, _ := json.Marshal(view)
	return string(out)
}

// foldInBrowser folds the events with the real chatfold.js in node and
// describes the result the way foldedView does.
func foldInBrowser(t *testing.T, events []agent.Event) string {
	t.Helper()
	return runFold(t, events, `(() => {
  const parts = (list) => list.map((p) => ({ id: p.id, kind: p.kind, state: p.state, text: p.text, children: parts(p.children) }));
  const messages = chat.timeline.filter((e) => e.type === 'message').map((e) => chat.messages.get(e.id))
    .map((m) => ({ id: m.id, role: m.role, state: m.state, text: m.text, parts: parts(m.parts) }));
  const asks = [...chat.asks.values()].sort((a, b) => a.seq - b.seq)
    .map((a) => ({ id: a.id, answered: a.answered, answer: a.answer, by_device: a.byDevice, delivered: a.delivered }));
  return { messages, asks, open: fold.openAsks(chat).map((a) => a.id), running: fold.busy(chat),
    outcome: chat.lastTurn ? chat.lastTurn.outcome : '' };
})()`)
}

// runFold folds the events with the real chatfold.js in node and prints
// expr, a JavaScript expression over fold (the module) and chat, as JSON.
func runFold(t *testing.T, events []agent.Event, expr string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so the browser module cannot be run")
	}
	source, err := filepath.Abs(filepath.Join("static", "js", "chatfold.js"))
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "events.json")
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	module := filepath.ToSlash(source)
	if !strings.HasPrefix(module, "/") {
		module = "/" + module
	}
	script := `
import { readFileSync } from 'node:fs';
const fold = await import(` + strconv.Quote("file://"+module) + `);
const chat = fold.newChat();
const events = JSON.parse(readFileSync(` + strconv.Quote(journal) + `, 'utf8'));
for (const ev of events) fold.apply(chat, ev);
console.log(JSON.stringify(` + expr + `));
`
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	var complaints strings.Builder
	cmd.Stderr = &complaints
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, complaints.String())
	}
	return strings.TrimSpace(string(out))
}

// compactJournal is parityJournal and a second chat's worth of what a
// replay compacts: a workflow and its agents replaced whole over and over,
// across a turn that ends interrupted; a part that a delta makes and a part
// event replaces; and the values reported again and again.
func compactJournal() []agent.Event {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	events := parityJournal()
	more := []agent.Event{
		{Type: agent.TypeMessage, MessageID: "m2", Role: agent.RoleUser, State: agent.MessageDelivered, ClientID: "c2", Text: "again"},
		{Type: agent.TypeTurnStarted, TurnID: "t2", MessageID: "m2"},
		{Type: agent.TypeMessage, MessageID: "a2", Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: "t2"},
		{Type: agent.TypeAgentInfo, Content: raw(`{"v":1}`)},
		{Type: agent.TypePartDelta, MessageID: "a2", PartID: "d", Offset: 0, Text: "made by a delta"},
		{Type: agent.TypeRateLimit, Window: "five_hour", Used: 10},
	}
	for i := 0; i < 4; i++ {
		more = append(more,
			agent.Event{Type: agent.TypePart, MessageID: "a2", PartID: "wf", Kind: agent.PartTool, State: agent.PartRunning,
				Content: raw(fmt.Sprintf(`{"tool":"Workflow","workflow":{"status":"running","agents_done":%d}}`, i))},
			agent.Event{Type: agent.TypePart, PartID: fmt.Sprintf("ag%d", i%2), ParentPartID: "wf", Kind: agent.PartTool, State: agent.PartRunning,
				Content: raw(fmt.Sprintf(`{"tool":"WorkflowAgent","n":%d}`, i))},
			agent.Event{Type: agent.TypeRateLimit, Window: "weekly", Used: float64(i)},
			agent.Event{Type: agent.TypeTasks, Count: i},
			agent.Event{Type: agent.TypeUsage, Usage: &agent.Usage{Context: int64(i), Window: 9}},
		)
	}
	more = append(more,
		agent.Event{Type: agent.TypePart, MessageID: "a2", PartID: "d", Kind: agent.PartText, State: agent.PartDone, Text: "replaced"},
		agent.Event{Type: agent.TypePartDelta, MessageID: "a2", PartID: "d", Offset: 8, Text: " and added to"},
		agent.Event{Type: agent.TypePart, MessageID: "a2", PartID: "todo", Kind: agent.PartTodo, State: agent.PartDone, Content: raw(`{"items":[]}`)},
		agent.Event{Type: agent.TypeUsage},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t2", Outcome: agent.OutcomeInterrupted},
		agent.Event{Type: agent.TypeRateLimit, Window: "five_hour", Used: 20},
		agent.Event{Type: agent.TypePart, MessageID: "a2", PartID: "wf", Kind: agent.PartTool, State: agent.PartRunning,
			Content: raw(`{"tool":"Workflow","workflow":{"status":"running","agents_done":9}}`)},
		agent.Event{Type: agent.TypePart, PartID: "ag1", ParentPartID: "wf", Kind: agent.PartTool, State: agent.PartDone, Text: "done"},
		agent.Event{Type: agent.TypePart, MessageID: "a2", PartID: "todo", Kind: agent.PartTodo, State: agent.PartDone, Content: raw(`{"items":[1]}`)},
		agent.Event{Type: agent.TypeAgentInfo, Content: raw(`{"v":2}`)},
		agent.Event{Type: agent.TypeMessage, MessageID: "m3", Role: agent.RoleUser, State: agent.MessageDelivered, ClientID: "c3", Text: "last"},
		agent.Event{Type: agent.TypeTurnStarted, TurnID: "t3", MessageID: "m3"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t3", Outcome: agent.OutcomeCompleted, Usage: &agent.Usage{Input: 3}},
		agent.Event{Type: agent.TypePart, MessageID: "a2", PartID: "wf", Kind: agent.PartTool, State: agent.PartDone,
			Content: raw(`{"tool":"Workflow","workflow":{"status":"completed","agents_done":10}}`)},
	)
	events = append(events, more...)
	for i := range events {
		events[i].Seq = int64(i + 1)
		events[i].At = int64(1000 + 3*i)
	}
	return events
}

// The browser keeps what it was sent compacted on the device, by the
// server's rules: from any seq, its compaction is the server's.
func TestTheBrowserCompactsAsTheServerDoes(t *testing.T) {
	events := compactJournal()
	normal := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var generic any
		if err := json.Unmarshal(data, &generic); err != nil {
			t.Fatal(err)
		}
		data, _ = json.Marshal(generic)
		return string(data)
	}
	var want [][]agent.Event
	for k := range events {
		want = append(want, agent.Compact(events[k:]))
	}
	got := runFold(t, events, `events.map((_, k) => fold.compact(events.slice(k)))`)
	var gotList any
	if err := json.Unmarshal([]byte(got), &gotList); err != nil {
		t.Fatal(err)
	}
	if normal(gotList) != normal(want) {
		t.Fatalf("the browser compacts differently:\nbrowser %s\nserver  %s", normal(gotList), normal(want))
	}
}

// A browser that folds a compacted run ends up where the whole run takes
// it, wherever the run starts: everything it draws from included - the
// timeline and its cuts, when each part was made and last changed, the
// values reported again and again.
func TestTheBrowserFoldsACompactedRunAsTheWholeRun(t *testing.T) {
	got := runFold(t, compactJournal(), `(() => {
  const describe = (one) => {
    const parts = (list) => list.map((p) => [p.id, p.kind, p.state, p.text, p.content, p.at, p.updatedAt, p.messageId, parts(p.children)]);
    return JSON.stringify({
      timeline: one.timeline.map((e) => [e.key, e.type, e.line ? e.line : null]),
      messages: [...one.messages.values()].map((m) => [m.id, m.role, m.state, m.text, m.cuts, parts(m.parts), m.turn ? m.turn.outcome : '']),
      parts: [...one.parts.keys()].sort(),
      asks: [...one.asks.values()].map((a) => [a.id, a.answered, a.closed, a.delivered]),
      turn: one.turn, lastTurn: one.lastTurn, lastAt: one.lastAt, usage: one.usage, cost: one.cost,
      rateLimits: Object.entries(one.rateLimits).sort(), tasks: one.tasks, info: one.info, model: one.model, account: one.account,
      todo: fold.currentTodo(one) ? fold.currentTodo(one).id : '',
    });
  };
  const want = describe(chat);
  const bad = [];
  for (let k = 0; k <= events.length; k += 1) {
    const one = fold.newChat();
    for (const ev of events.slice(0, k)) fold.apply(one, ev);
    for (const ev of fold.compact(events.slice(k))) fold.apply(one, ev);
    if (describe(one) !== want) bad.push(k);
  }
  return bad;
})()`)
	if got != "[]" {
		t.Fatalf("compacted from these events on, the fold differs: %s", got)
	}
}

// What a light replay held back is put in where it belongs when it is asked
// for: a workflow sent without its agents gets them, in the server's order
// and as the server has them, and a part the stream changed after the
// answer was read keeps what the stream said.
func TestTheBrowserFillsInWhatALightReplayHeldBack(t *testing.T) {
	events := compactJournal()
	p := agent.Fold(events)
	light := agent.Lighten(agent.Compact(events), 0, p)
	part, children, ok := p.Detail("wf")
	if !ok {
		t.Fatal("no workflow")
	}
	last := events[len(events)-1].Seq
	detail, _ := json.Marshal(map[string]any{"seq": last, "part": part, "children": children})
	// The answer was read before the last event: a newer ag1 came by the
	// stream meanwhile, and stays.
	stale, _ := json.Marshal(map[string]any{"seq": last - 1, "part": part, "children": children})
	got := runFold(t, light, `(() => {
  const wf = chat.parts.get('wf');
  const before = { held: wf.held, children: wf.children.map((c) => c.id) };
  fold.fill(chat, `+string(detail)+`);
  const after = { held: wf.held, lite: wf.lite, children: wf.children.map((c) => [c.id, c.state, c.parentId, chat.parts.get(c.id) === c]) };
  const again = fold.newChat();
  for (const ev of events) fold.apply(again, ev);
  fold.apply(again, { seq: 1e6, at: 1, type: 'part', part_id: 'ag1', parent_part_id: 'wf', kind: 'tool', state: 'error' });
  fold.fill(again, `+string(stale)+`);
  return { before, after, kept: again.parts.get('ag1').state, unknown: fold.fill(chat, { seq: 1, part: { part_id: 'nothing' } }) };
})()`)
	var want []string
	for _, c := range children {
		want = append(want, fmt.Sprintf(`[%q,%q,"wf",true]`, c.PartID, c.State))
	}
	expect := `{"before":{"held":2,"children":[]},"after":{"held":0,"lite":false,"children":[` + strings.Join(want, ",") + `]},"kept":"error","unknown":false}`
	if got != expect {
		t.Fatalf("filled in:\n%s\nwant\n%s", got, expect)
	}
}
