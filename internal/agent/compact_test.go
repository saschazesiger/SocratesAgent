package agent

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// A compacted run folds into what the whole run folds into, wherever it
// starts: Compact is what a replay sends from any seq a browser has, on top
// of what it holds. The journals are random - parts made by part events and
// by deltas, nested and orphaned, replaced whole and appended to (with
// overlaps and gaps), turns that end every way and with the wrong id, the
// values a later event sets again - and every split of every one is tried.
func TestACompactedRunFoldsAsTheWholeRun(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		events := randomJournal(seed, 220)
		want := described(Fold(events))
		for k := 0; k <= len(events); k++ {
			p := Fold(events[:k])
			rest := Compact(events[k:])
			for _, ev := range rest {
				p.Apply(ev)
			}
			if got := described(p); got != want {
				t.Fatalf("seed %d, compacted from event %d: the fold differs\ngot  %s\nwant %s", seed, k, got, want)
			}
		}
	}
}

// What a workflow journals over and over is sent once.
func TestCompactKeepsOneEventOfAPartReplacedOverAndOver(t *testing.T) {
	var events []Event
	add := func(ev Event) {
		ev.Seq, ev.At = int64(len(events)+1), int64(1000+len(events))
		events = append(events, ev)
	}
	add(Event{Type: TypeMessage, MessageID: "a", Role: RoleAssistant, State: MessageStreaming})
	for i := 0; i < 50; i++ {
		add(Event{Type: TypePart, MessageID: "a", PartID: "wf", Kind: PartTool, State: PartRunning,
			Content: json.RawMessage(fmt.Sprintf(`{"tool":"Workflow","step":%d}`, i))})
		add(Event{Type: TypeRateLimit, Window: "five_hour", Used: float64(i)})
	}
	add(Event{Type: TypePart, MessageID: "a", PartID: "wf", Kind: PartTool, State: PartDone, Content: json.RawMessage(`{"tool":"Workflow","step":50}`)})
	out := Compact(events)
	if len(out) != 3 {
		t.Fatalf("%d events kept, want the message, one part and one rate_limit: %+v", len(out), out)
	}
	if out[1].Seq != 2 || out[1].State != PartDone || out[1].UpdatedAt != events[len(events)-1].At || out[1].At != events[1].At {
		t.Fatalf("the part is %+v", out[1])
	}
}

// described is a projection as a string, everything it holds included.
func described(p *Projection) string {
	type partFacts struct {
		Seq     int64
		TextLen int
	}
	parts := map[string]partFacts{}
	for id, part := range p.parts {
		parts[id] = partFacts{part.seq, part.textLen}
	}
	after := map[string]string{}
	for id, m := range p.messages {
		after[id] = m.AfterTurn
	}
	copyOf := *p
	copyOf.LastSeq = 0
	raw, err := json.Marshal(struct {
		P       Projection
		Parts   map[string]partFacts
		Clients map[string]int64
		After   map[string]string
		PID     int
	}{copyOf, parts, p.clients, after, p.HostPID})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// randomJournal is a chat's journal of n events, made up from seed.
func randomJournal(seed int64, n int) []Event {
	rng := rand.New(rand.NewSource(seed))
	mirror := NewProjection()
	var events []Event
	var messages, parts, asks []string
	turn := ""
	turns := 0
	add := func(ev Event) {
		ev.Seq, ev.At = int64(len(events)+1), int64(1000+7*len(events))
		events = append(events, ev)
		mirror.Apply(ev)
	}
	pick := func(list []string) string {
		if len(list) == 0 {
			return ""
		}
		return list[rng.Intn(len(list))]
	}
	states := []string{PartStreaming, PartRunning, PartDone, PartError, PartInterrupted}
	kinds := []string{PartText, PartTool, PartReasoning, PartTodo}
	words := []string{"", "a", "hello", "wörld 😀", "more text here"}
	for len(events) < n {
		switch r := rng.Intn(100); {
		case r < 6 && turn == "":
			turns++
			turn = fmt.Sprintf("t%d", turns)
			m := fmt.Sprintf("u%d", turns)
			messages = append(messages, m)
			add(Event{Type: TypeMessage, MessageID: m, Role: RoleUser, State: MessagePending, ClientID: "c" + m, Text: "go"})
			add(Event{Type: TypeTurnStarted, TurnID: turn, MessageID: m, Account: pick([]string{"", "acct"})})
		case r < 12:
			m := fmt.Sprintf("a%d", len(messages))
			messages = append(messages, m)
			add(Event{Type: TypeMessage, MessageID: m, Role: RoleAssistant, State: MessageStreaming, TurnID: turn})
		case r < 26:
			id := fmt.Sprintf("p%d", len(parts))
			ev := Event{Type: TypePart, MessageID: pick(messages), PartID: id, Kind: pick(kinds), State: pick(states),
				Text: pick(words), Content: json.RawMessage(fmt.Sprintf(`{"tool":"T","n":%d}`, rng.Intn(5)))}
			if len(parts) > 0 && rng.Intn(3) == 0 {
				ev.ParentPartID = pick(parts)
				if rng.Intn(2) == 0 {
					ev.MessageID = ""
				}
			}
			if rng.Intn(15) == 0 {
				ev.ParentPartID = "nowhere"
			}
			parts = append(parts, id)
			add(ev)
		case r < 50 && len(parts) > 0:
			// A part replaced whole, now and then with another kind.
			id := pick(parts)
			ev := Event{Type: TypePart, MessageID: pick(messages), PartID: id, Kind: PartTool, State: pick(states),
				Text: pick(words), Content: json.RawMessage(fmt.Sprintf(`{"tool":"T","n":%d}`, rng.Intn(50)))}
			if part := mirror.Part(id); part != nil && rng.Intn(4) > 0 {
				ev.Kind = part.Kind
			}
			add(ev)
		case r < 66:
			id := pick(parts)
			if id == "" || rng.Intn(8) == 0 {
				id = fmt.Sprintf("p%d", len(parts))
				parts = append(parts, id)
			}
			have := 0
			if part := mirror.Part(id); part != nil {
				have = part.textLen
			}
			offset := have + rng.Intn(3) - 1
			if rng.Intn(10) == 0 {
				offset = have + 5
			}
			add(Event{Type: TypePartDelta, MessageID: pick(messages), PartID: id, Offset: max(offset, 0), Text: pick(words[1:])})
		case r < 72:
			id := turn
			if rng.Intn(6) == 0 {
				id = "other"
			}
			outcome := pick([]string{OutcomeCompleted, OutcomeCompleted, OutcomeInterrupted, OutcomeFailed, OutcomeLimited})
			usage := &Usage{Input: int64(rng.Intn(9)), Context: int64(rng.Intn(3)), Window: 100}
			add(Event{Type: TypeTurnEnded, TurnID: id, Outcome: outcome, Usage: usage, CostUSD: 0.5})
			if id == turn {
				turn = ""
			}
		case r < 78:
			add(Event{Type: TypeRateLimit, Window: pick([]string{"five_hour", "weekly"}), Used: float64(rng.Intn(100)), ResetsAt: int64(rng.Intn(9))})
		case r < 82:
			add(Event{Type: TypeTasks, Count: rng.Intn(4)})
		case r < 86:
			ev := Event{Type: TypeUsage}
			if rng.Intn(3) > 0 {
				ev.Usage = &Usage{Context: int64(rng.Intn(50)), Window: 200}
			}
			add(ev)
		case r < 89:
			add(Event{Type: TypeAgentInfo, Content: json.RawMessage(fmt.Sprintf(`{"v":%d}`, rng.Intn(9)))})
		case r < 93:
			id := fmt.Sprintf("k%d", len(asks))
			asks = append(asks, id)
			payload := `{"questions":[]}`
			if part := pick(parts); part != "" && rng.Intn(2) == 0 {
				payload = fmt.Sprintf(`{"part_id":%q,"questions":[]}`, part)
			}
			add(Event{Type: TypeAsk, AskID: id, TurnID: turn, Payload: json.RawMessage(payload)})
		case r < 96 && len(asks) > 0:
			add(Event{Type: TypeAskAnswered, AskID: pick(asks), Answer: json.RawMessage(`{}`), ClientID: fmt.Sprintf("ans%d", rng.Intn(4))})
		case r < 98:
			add(Event{Type: TypeCommand, Op: pick([]string{OpInterrupt, OpCommand}), ClientID: fmt.Sprintf("cmd%d", rng.Intn(5))})
		default:
			if m := pick(messages); m != "" {
				add(Event{Type: TypeMessageState, MessageID: m, State: pick([]string{MessageDelivered, MessageDone, MessageStreaming})})
			}
		}
	}
	return events
}
