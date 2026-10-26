package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalAppendsAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat", JournalFile)
	j, events, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("a new journal has %d events", len(events))
	}
	out, err := j.Append(Event{Type: TypeNotice, Text: "one"}, Event{Type: TypeNotice, Text: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Seq != 1 || out[1].Seq != 2 || out[0].At == 0 {
		t.Fatalf("stamped %+v", out)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, events, err = OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(events) != 2 || events[1].Text != "two" {
		t.Fatalf("reopened %+v", events)
	}
	out, _ = j.Append(Event{Type: TypeNotice})
	if out[0].Seq != 3 {
		t.Fatalf("seq after reopen = %d", out[0].Seq)
	}
}

// A writer killed in the middle of a line, or a power loss, leaves a torn
// last line. The reader skips it, and the next open cuts it off so the next
// line starts on its own.
func TestJournalToleratesATornLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	j, _, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Type: TypeNotice, Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"seq":2,"at":1,"type":"part","text":"half of`)
	f.Close()

	events, err := ReadJournal(path)
	if err != nil || len(events) != 1 || events[0].Text != "kept" {
		t.Fatalf("read %+v %v", events, err)
	}
	j, events, err = OpenJournal(path)
	if err != nil || len(events) != 1 {
		t.Fatalf("open %+v %v", events, err)
	}
	out, _ := j.Append(Event{Type: TypeNotice, Text: "after"})
	j.Close()
	if out[0].Seq != 2 {
		t.Fatalf("the torn line's seq must be reused, got %d", out[0].Seq)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "half of") {
		t.Fatalf("the torn line is still there:\n%s", raw)
	}
	events, _ = ReadJournal(path)
	if len(events) != 2 || events[1].Text != "after" {
		t.Fatalf("after repair %+v", events)
	}
}

// Zeros at the end are what some file systems leave after a power loss.
func TestJournalSkipsZeroFilledTailAndDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	lines := []string{
		`{"seq":1,"at":1,"type":"notice"}`,
		`not json`,
		`{"seq":1,"at":1,"type":"notice","text":"a duplicate seq"}`,
		`{"seq":2,"at":1,"type":"notice","text":"two"}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n\x00\x00\x00\x00"), 0o600)
	events, err := ReadJournal(path)
	if err != nil || len(events) != 2 || events[1].Text != "two" {
		t.Fatalf("read %+v %v", events, err)
	}
	j, events, err := OpenJournal(path)
	if err != nil || len(events) != 2 {
		t.Fatalf("open %+v %v", events, err)
	}
	j.Close()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "\x00") {
		t.Fatal("the zero-filled tail was not cut off")
	}
}

// A whole last event that lost only its newline is kept, and the next line
// does not run on from it.
func TestJournalCompletesALastLineWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	os.WriteFile(path, []byte(`{"seq":1,"at":1,"type":"notice"}`), 0o600)
	j, events, err := OpenJournal(path)
	if err != nil || len(events) != 1 {
		t.Fatalf("open %+v %v", events, err)
	}
	j.Append(Event{Type: TypeNotice})
	j.Close()
	events, _ = ReadJournal(path)
	if len(events) != 2 {
		t.Fatalf("read %+v", events)
	}
}

func TestRecorderCoalescesDeltasPerPart(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	j, _, _ := OpenJournal(path)
	rec := NewRecorder(j, nil, 50*time.Millisecond)
	sub := rec.Feed().Subscribe()
	rec.Emit(Event{Type: TypePart, MessageID: "m", PartID: "p", Kind: PartText, State: PartStreaming})
	for i := 0; i < 100; i++ {
		rec.Delta("m", "p", "ab")
	}
	time.Sleep(150 * time.Millisecond)
	for i := 0; i < 10; i++ {
		rec.Delta("m", "p", "cd")
	}
	rec.Emit(Event{Type: TypePart, MessageID: "m", PartID: "q", Kind: PartTool, State: PartRunning})
	rec.Close()

	events, _ := ReadJournal(path)
	var deltas []Event
	for _, ev := range events {
		if ev.Type == TypePartDelta {
			deltas = append(deltas, ev)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("110 deltas in two bursts should be 2 lines, got %d: %+v", len(deltas), deltas)
	}
	if deltas[0].Offset != 0 || len(deltas[0].Text) != 200 || deltas[1].Offset != 200 || len(deltas[1].Text) != 20 {
		t.Fatalf("coalesced lines %+v", deltas)
	}
	// The pending deltas are written before the next event, so the journal
	// is in the order things happened.
	if events[len(events)-1].PartID != "q" {
		t.Fatalf("the last line is %+v", events[len(events)-1])
	}
	p := Fold(events)
	if got := p.Part("p").Text; got != strings.Repeat("ab", 100)+strings.Repeat("cd", 10) {
		t.Fatalf("folded text %q", got)
	}
	// Every delta also went out live, uncoalesced.
	_, live := sub.Pull(0)
	if len(live) != 110 || live[109].Offset != 218 {
		t.Fatalf("live deltas %d", len(live))
	}
}

// The final state is always written: a part that replaces its streaming text
// drops the deltas still waiting, and the fold is the final text.
func TestRecorderWritesTheFinalState(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	j, _, _ := OpenJournal(path)
	rec := NewRecorder(j, nil, time.Hour)
	rec.Emit(Event{Type: TypePart, MessageID: "m", PartID: "p", Kind: PartText, State: PartStreaming})
	rec.Delta("m", "p", "hel")
	rec.Delta("m", "p", "lo")
	rec.Emit(Event{Type: TypePart, MessageID: "m", PartID: "p", Kind: PartText, State: PartDone, Text: "hello"})
	rec.Delta("m", "p", "!")
	rec.Close()
	events, _ := ReadJournal(path)
	p := Fold(events)
	if part := p.Part("p"); part.Text != "hello!" || part.State != PartDone {
		t.Fatalf("part %+v from %+v", part, events)
	}
	last := events[len(events)-1]
	if last.Type != TypePartDelta || last.Offset != 5 {
		t.Fatalf("the delta after the final state %+v", last)
	}
}

func TestProjectionFoldsTurnsMessagesPartsAndAsks(t *testing.T) {
	answer := json.RawMessage(`{"q1":"a"}`)
	events := []Event{
		{Seq: 1, At: 10, Type: TypeConversation, ConversationID: "conv"},
		{Seq: 2, At: 11, Type: TypeMessage, MessageID: "u1", Role: RoleUser, State: MessagePending, ClientID: "c1", Text: "hi"},
		{Seq: 3, At: 12, Type: TypeMessageState, MessageID: "u1", State: MessageDelivered},
		{Seq: 4, At: 12, Type: TypeTurnStarted, TurnID: "t1", MessageID: "u1"},
		{Seq: 5, At: 13, Type: TypeMessage, MessageID: "a1", Role: RoleAssistant, State: MessageStreaming},
		{Seq: 6, At: 13, Type: TypePart, MessageID: "a1", PartID: "p1", Kind: PartText, State: PartStreaming},
		{Seq: 7, At: 14, Type: TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 0, Text: "Hel"},
		// A repeat of what is there, overlapping, and a gap: the first adds
		// only "lo", the gap is left for the journal.
		{Seq: 8, At: 14, Type: TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 1, Text: "ello"},
		{Seq: 9, At: 14, Type: TypePartDelta, MessageID: "a1", PartID: "p1", Offset: 9, Text: "gap"},
		{Seq: 10, At: 15, Type: TypePart, MessageID: "a1", PartID: "p2", Kind: PartTool, State: PartRunning},
		{Seq: 11, At: 16, Type: TypeAsk, AskID: "k1", TurnID: "t1", Payload: json.RawMessage(`{"title":"T"}`)},
	}
	p := Fold(events)
	if p.ConversationID != "conv" || p.Turn == nil || p.Turn.ID != "t1" {
		t.Fatalf("projection %+v", p)
	}
	if got := p.Part("p1").Text; got != "Hello" {
		t.Fatalf("delta folding gave %q", got)
	}
	if s := p.Summary(); s.Activity != ActivityWaiting || !s.Asked || s.OpenAsks != 1 {
		t.Fatalf("summary %+v", s)
	}
	if seq, ok := p.Seen("c1"); !ok || seq != 2 {
		t.Fatalf("client id c1 seen at %d %v", seq, ok)
	}
	// Part replacement keeps the part's position and takes the new state whole.
	p.Apply(Event{Seq: 12, At: 17, Type: TypePart, MessageID: "a1", PartID: "p1", Kind: PartText, State: PartDone, Text: "Hello world"})
	m := p.Message("a1")
	if len(m.Parts) != 2 || m.Parts[0].ID != "p1" || m.Parts[0].Text != "Hello world" || m.Parts[0].State != PartDone {
		t.Fatalf("parts after replacement %+v %+v", m.Parts[0], m.Parts[1])
	}
	p.Apply(Event{Seq: 13, At: 18, Type: TypeAskAnswered, AskID: "k1", ClientID: "c2", Answer: answer, Device: "phone"})
	p.Apply(Event{Seq: 14, At: 18, Type: TypeAskAnswered, AskID: "k1", ClientID: "c3", Answer: json.RawMessage(`1`), Device: "laptop"})
	if a := p.Ask("k1"); !a.Answered || a.ByDevice != "phone" || string(a.Answer) != string(answer) {
		t.Fatalf("the first answer must win: %+v", a)
	}
	if s := p.Summary(); s.Activity != ActivityBusy {
		t.Fatalf("answered, still in the turn: %+v", s)
	}
	// A turn that does not complete leaves nothing streaming.
	p.Apply(Event{Seq: 15, At: 19, Type: TypeTurnEnded, TurnID: "t1", Outcome: OutcomeInterrupted,
		Usage: &Usage{Input: 3, Output: 4}, CostUSD: 0.5})
	if p.Turn != nil || p.LastTurn.Outcome != OutcomeInterrupted || m.State != MessageInterrupted || m.Parts[1].State != PartInterrupted {
		t.Fatalf("after the interrupt: turn %+v message %+v tool %+v", p.LastTurn, m, m.Parts[1])
	}
	if p.Usage.Output != 4 || p.CostUSD != 0.5 {
		t.Fatalf("usage %+v %v", p.Usage, p.CostUSD)
	}
	if s := p.Summary(); s.Activity != ActivityIdle || s.LastOutcome != OutcomeInterrupted {
		t.Fatalf("summary %+v", s)
	}
	// A duplicate seq is not applied twice.
	p.Apply(Event{Seq: 15, Type: TypeTurnStarted, TurnID: "t2"})
	if p.Turn != nil {
		t.Fatal("an old seq was applied")
	}
	if len(p.UnsentAnswers()) != 1 {
		t.Fatalf("the answer was never delivered: %+v", p.UnsentAnswers())
	}
	p.Apply(Event{Seq: 16, Type: TypeAskDelivered, AskID: "k1", Via: ViaMessage})
	if len(p.UnsentAnswers()) != 0 || len(p.Undelivered()) != 0 {
		t.Fatalf("unsent %+v undelivered %+v", p.UnsentAnswers(), p.Undelivered())
	}
}

func TestApplyDeltaCountsUTF16(t *testing.T) {
	text, n := "", 0
	ApplyDelta(&text, &n, 0, "a😀")
	if n != 3 {
		t.Fatalf("a😀 is %d UTF-16 units", n)
	}
	if ApplyDelta(&text, &n, 0, "a😀") {
		t.Fatal("a repeat was applied")
	}
	ApplyDelta(&text, &n, 1, "😀b")
	if text != "a😀b" || n != 4 {
		t.Fatalf("%q %d", text, n)
	}
}

func TestFeedNeverHandsADeltaOutBeforeItsEvent(t *testing.T) {
	f := NewFeed(nil)
	sub := f.Subscribe()
	defer sub.Close()
	f.Append(Event{Seq: 1, Type: TypePart, PartID: "p"})
	f.Delta(Delta{PartID: "p", Text: "x"})
	events, deltas := sub.Pull(0)
	if len(events) != 1 || len(deltas) != 1 || deltas[0].After != 1 {
		t.Fatalf("events %+v deltas %+v", events, deltas)
	}
	select {
	case <-sub.Wake():
	default:
		t.Fatal("no wake")
	}
	if events, _ := sub.Pull(1); len(events) != 0 {
		t.Fatalf("after 1: %+v", events)
	}
}

// LastSeq reads the end of a journal the way readJournal reads all of it.
func TestLastSeqReadsTheEndOfAJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	if seq, err := LastSeq(path); err != nil || seq != 0 {
		t.Fatalf("a missing journal: %d %v", seq, err)
	}
	j, _, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 200<<10)
	if _, err := j.Append(Event{Type: TypeNotice, Text: "one"}, Event{Type: TypeNotice, Text: long},
		Event{Type: TypeNotice, Text: long}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	if seq, err := LastSeq(path); err != nil || seq != 3 {
		t.Fatalf("whole: %d %v", seq, err)
	}
	// A torn tail and zeros after it are skipped; a last event without its
	// newline counts.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"seq":4,"at":1,"type":"notice","text":"cut`)
	f.Write(make([]byte, 100))
	f.Close()
	if seq, _ := LastSeq(path); seq != 3 {
		t.Fatalf("torn: %d", seq)
	}
	os.WriteFile(path, []byte(`{"seq":1,"at":1,"type":"notice"}`+"\n"+`{"seq":2,"at":1,"type":"notice"}`), 0o600)
	if seq, _ := LastSeq(path); seq != 2 {
		t.Fatalf("no final newline: %d", seq)
	}
}

// A reader that kept where it read up to reads on from there, and can tell
// that the journal is still the one it read: an event ends where it left off.
func TestAJournalIsReadOnFromWhereAReaderLeftOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, _, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", strings.Repeat("long ", 30000), "three"} {
		if _, err := j.Append(Event{Type: TypeNotice, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	all, good, err := ReadJournalFrom(path, 0, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("%d events, %v", len(all), err)
	}
	info, _ := os.Stat(path)
	if good != info.Size() {
		t.Fatalf("good %d, size %d", good, info.Size())
	}
	raw, _ := os.ReadFile(path)
	second := int64(bytes.IndexByte(raw, '\n') + 1)
	third := second + int64(bytes.IndexByte(raw[second:], '\n')+1)
	if seq, _ := SeqAt(path, third); seq != 2 {
		t.Fatalf("the event ending at %d is %d, want 2 (a line longer than a block)", third, seq)
	}
	if seq, _ := SeqAt(path, good); seq != 3 {
		t.Fatalf("the last event is %d", seq)
	}
	if seq, _ := SeqAt(path, third-5); seq != 0 {
		t.Fatalf("no event ends in the middle of a line, got %d", seq)
	}
	rest, end, _ := ReadJournalFrom(path, second, 1)
	if len(rest) != 2 || rest[0].Seq != 2 || end != good {
		t.Fatalf("read on: %d events from %d, end %d", len(rest), rest[0].Seq, end)
	}
	if again, _, _ := ReadJournalFrom(path, second, 2); len(again) != 1 {
		t.Fatalf("an event not past the one before is not read again: %d", len(again))
	}
}
