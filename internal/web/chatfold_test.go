package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
for (const ev of JSON.parse(readFileSync(` + strconv.Quote(journal) + `, 'utf8'))) fold.apply(chat, ev);
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
