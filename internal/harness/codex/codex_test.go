package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/harness/fakes"
)

func TestMain(m *testing.M) { fakes.Main(m) }

// ---------------------------------------------------------------- harness

// rec drains an adapter's events into a slice, so a test can wait for the one
// it cares about without ever blocking the adapter.
type rec struct {
	mu     sync.Mutex
	events []harness.Event
	closed bool
}

func record(a harness.Adapter) *rec {
	r := &rec{}
	go func() {
		for ev := range a.Events() {
			r.mu.Lock()
			r.events = append(r.events, ev)
			r.mu.Unlock()
		}
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
	}()
	return r
}

func (r *rec) all() []harness.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]harness.Event, len(r.events))
	copy(out, r.events)
	return out
}

func (r *rec) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// wait blocks until an event of this kind has arrived, and returns the first.
func (r *rec) wait(t *testing.T, kind string) harness.Event {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range r.all() {
			if ev.Kind == kind {
				return ev
			}
		}
		if r.isClosed() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	for _, ev := range r.all() {
		if ev.Kind == kind {
			return ev
		}
	}
	t.Fatalf("no %s event arrived; got %s", kind, kinds(r.all()))
	return harness.Event{}
}

func (r *rec) waitClosed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if r.isClosed() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("Events was never closed; got %s", kinds(r.all()))
}

func kinds(events []harness.Event) string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return "[" + strings.Join(out, " ") + "]"
}

func count(events []harness.Event, kind string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func of(events []harness.Event, kind string) []harness.Event {
	var out []harness.Event
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// session sets PATH to the fakes, scripts one, starts an adapter and makes
// sure it is closed again.
type session struct {
	a        *adapter
	r        *rec
	argvFile string
}

func start(t *testing.T, script string, spec harness.Spec) *session {
	t.Helper()
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))

	argv := filepath.Join(t.TempDir(), "argv.jsonl")
	if spec.Cwd == "" {
		spec.Cwd = t.TempDir()
	}
	if spec.Agent == "" {
		spec.Agent = "codex"
	}
	if spec.Model == "" {
		spec.Model = "gpt-5.4-mini"
	}
	if spec.ChatID == "" {
		spec.ChatID = "chat_test"
	}
	spec.Env = append(spec.Env, "FAKE_SCRIPT="+script, "FAKE_ARGV_FILE="+argv)

	a := newAdapter()
	r := record(a)
	if err := a.Start(t.Context(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Close(ctx, 2*time.Second)
	})
	return &session{a: a, r: r, argvFile: argv}
}

// argv reads back everything the fake recorded: its own argv first, then one
// line per turn/start.
func (s *session) argv(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(s.argvFile)
	if err != nil {
		t.Fatalf("reading the argv file: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var one []string
		if err := json.Unmarshal([]byte(line), &one); err != nil {
			t.Fatalf("argv line %q: %v", line, err)
		}
		out = append(out, one)
	}
	return out
}

// shortWatchdogs makes the three turn-end timers testable in milliseconds.
func shortWatchdogs(t *testing.T, errGrace, quiet, silence time.Duration) {
	t.Helper()
	oldErr, oldQuiet, oldSilence := errorGrace, quietAfter, silenceAfter
	errorGrace, quietAfter, silenceAfter = errGrace, quiet, silence
	t.Cleanup(func() { errorGrace, quietAfter, silenceAfter = oldErr, oldQuiet, oldSilence })
}

// checkInvariants asserts the parts of §2.3 that hold for every turn of every
// adapter.
func checkInvariants(t *testing.T, events []harness.Event) {
	t.Helper()
	seenSession := false
	open := map[string]bool{}
	finished := map[string]int{}
	started := map[string]int{}
	fatal := -1
	for i, ev := range events {
		if fatal >= 0 && i > fatal {
			t.Errorf("event %d (%s) arrived after the fatal", i, ev.Kind)
		}
		switch ev.Kind {
		case harness.KindSessionID:
			if ev.Session == "" {
				t.Errorf("a session_id without a session id")
			}
			seenSession = true
		case harness.KindTurnStarted:
			if !seenSession {
				t.Errorf("turn_started before session_id (invariant 1)")
			}
			started[ev.TurnID]++
			open[ev.TurnID] = true
		case harness.KindTurnFinished:
			finished[ev.TurnID]++
			if !open[ev.TurnID] {
				t.Errorf("turn_finished for %q without an open turn", ev.TurnID)
			}
			open[ev.TurnID] = false
			switch ev.Outcome {
			case harness.OutcomeOK, harness.OutcomeError, harness.OutcomeInterrupted:
			default:
				t.Errorf("turn_finished with outcome %q", ev.Outcome)
			}
		case harness.KindFatal:
			fatal = i
		}
		if ev.Kind != harness.KindSessionID && ev.Kind != harness.KindFatal &&
			ev.Kind != harness.KindNotice && ev.TurnID == "" {
			t.Errorf("event %d (%s) has no turn id", i, ev.Kind)
		}
	}
	for id, n := range started {
		if n != 1 {
			t.Errorf("turn %s started %d times (invariant 2)", id, n)
		}
		if finished[id] != 1 {
			t.Errorf("turn %s finished %d times (invariant 2 / 7)", id, finished[id])
		}
	}
	// Invariant 3: a tool's started/output/finished share one id.
	live := map[string]bool{}
	for _, ev := range events {
		switch ev.Kind {
		case harness.KindToolStarted, harness.KindSubagentStarted:
			live[ev.ID] = true
		case harness.KindToolOutput:
			if !live[ev.ID] {
				t.Errorf("tool_output for %q with no open tool call", ev.ID)
			}
		case harness.KindToolFinished, harness.KindSubagentFinished:
			if !live[ev.ID] {
				t.Errorf("tool_finished for %q with no open tool call", ev.ID)
			}
			delete(live, ev.ID)
		}
	}
}

// texts joins the text_delta events of one block, so a test can assert the
// stream rebuilds the completed text whatever the coalescing did.
func deltasOf(events []harness.Event, id string) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind == harness.KindTextDelta && ev.ID == id {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

// --------------------------------------------------------------- the tests

func TestATextTurnStreamsAndCompletes(t *testing.T) {
	s := start(t, `[{"do":"text","text":"All good here."},{"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "gpt-5.4-mini", Effort: "low"})

	sid := s.r.wait(t, harness.KindSessionID)
	if sid.Session == "" {
		t.Fatalf("no thread id")
	}
	if err := s.a.Send(t.Context(), "run_1", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK || end.TurnID != "run_1" {
		t.Fatalf("turn_finished = %+v", end)
	}
	events := s.r.all()
	checkInvariants(t, events)

	text := of(events, harness.KindText)
	if len(text) != 1 || text[0].Text != "All good here." {
		t.Fatalf("text events = %+v", text)
	}
	if got := deltasOf(events, text[0].ID); got != "All good here." {
		t.Errorf("the deltas rebuild %q, want the whole text", got)
	}
	// E5: the userMessage item the turn opens with must never become a card.
	if n := count(events, harness.KindToolStarted); n != 0 {
		t.Errorf("a text-only turn produced %d tool cards; the userMessage item must be dropped", n)
	}
	usage := of(events, harness.KindUsage)
	if len(usage) == 0 {
		t.Fatalf("no usage event")
	}
	u := usage[len(usage)-1].Usage
	if u.Total != 11341 || u.Input != 11143 || u.Cached != 4352 || u.Reasoning != 21 || u.Context != 258400 {
		t.Errorf("usage = %+v", u)
	}

	// The unattended argv and F-5: model and effort on every turn.
	lines := s.argv(t)
	if len(lines) < 2 {
		t.Fatalf("the fake recorded %v", lines)
	}
	if lines[0][1] != "app-server" || !strings.Contains(strings.Join(lines[0], " "), "stdio://") {
		t.Errorf("argv = %v", lines[0])
	}
	want := []string{"turn/start", "model=gpt-5.4-mini", "effort=low"}
	if strings.Join(lines[1], " ") != strings.Join(want, " ") {
		t.Errorf("turn/start recorded %v, want %v", lines[1], want)
	}
}

func TestATurnWithATool(t *testing.T) {
	s := start(t, `[{"do":"text","text":"Running it."},
	  {"do":"tool","name":"Bash","input":"go test ./...","output":"ok\ndone\n","exit":0},
	  {"do":"text","text":"All tests pass."},
	  {"do":"end","outcome":"ok"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "run the tests"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.r.wait(t, harness.KindTurnFinished)
	events := s.r.all()
	checkInvariants(t, events)

	if n := count(events, harness.KindText); n != 2 {
		t.Errorf("%d text blocks, want 2 (commentary and final_answer)", n)
	}
	// F-12: two blocks of one turn must not share an id.
	texts := of(events, harness.KindText)
	if texts[0].ID == texts[1].ID {
		t.Errorf("both text blocks have id %q", texts[0].ID)
	}
	starts := of(events, harness.KindToolStarted)
	if len(starts) != 1 {
		t.Fatalf("%d tool_started, want 1: %s", len(starts), kinds(events))
	}
	if starts[0].Tool.Name != "shell" || starts[0].Tool.Input != "go test ./..." {
		t.Errorf("tool_started = %+v", starts[0].Tool)
	}
	if !strings.Contains(starts[0].Tool.InputJSON, "commandExecution") {
		t.Errorf("input_json = %q", starts[0].Tool.InputJSON)
	}
	// FK-12: aggregatedOutput is null on both ends, so an adapter that
	// ignores the deltas has nothing to report.
	out := of(events, harness.KindToolOutput)
	if len(out) == 0 {
		t.Fatalf("no tool_output")
	}
	fin := of(events, harness.KindToolFinished)
	if len(fin) != 1 || fin[0].ID != starts[0].ID {
		t.Fatalf("tool_finished = %+v", fin)
	}
	if fin[0].Tool.Output != "ok\ndone\n" || !fin[0].Tool.OK || fin[0].Tool.ExitCode != 0 {
		t.Errorf("tool_finished tool = %+v", fin[0].Tool)
	}
}

func TestAFailingToolIsNotOK(t *testing.T) {
	s := start(t, `[{"do":"tool","name":"Bash","input":"false","output":"boom\n","exit":2},
	  {"do":"end","outcome":"ok"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.r.wait(t, harness.KindTurnFinished)
	fin := of(s.r.all(), harness.KindToolFinished)
	if len(fin) != 1 || fin[0].Tool.OK || fin[0].Tool.ExitCode != 2 {
		t.Fatalf("tool_finished = %+v", fin)
	}
}

func TestReasoningIsOneEventAndNeverATextDelta(t *testing.T) {
	s := start(t, `[{"do":"reason","text":"Thinking about the tests."},
	  {"do":"text","text":"Done."},{"do":"end","outcome":"ok"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "think"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.r.wait(t, harness.KindTurnFinished)
	events := s.r.all()
	checkInvariants(t, events)
	reason := of(events, harness.KindReasoning)
	if len(reason) != 1 || reason[0].Text != "Thinking about the tests." {
		t.Fatalf("reasoning = %+v", reason)
	}
	for _, ev := range of(events, harness.KindTextDelta) {
		if ev.ID == reason[0].ID {
			t.Errorf("reasoning was streamed as a text_delta")
		}
	}
}

func TestASubagentItemBecomesSubagentEvents(t *testing.T) {
	s := start(t, `[{"do":"subagent","name":"Task","input":"agents/reviewer.md","output":"looks fine"},
	  {"do":"end","outcome":"ok"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "delegate"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.r.wait(t, harness.KindTurnFinished)
	events := s.r.all()
	checkInvariants(t, events)
	up := of(events, harness.KindSubagentStarted)
	down := of(events, harness.KindSubagentFinished)
	if len(up) != 1 || len(down) != 1 || up[0].ID != down[0].ID {
		t.Fatalf("subagent events = %s", kinds(events))
	}
	if up[0].Tool.Title != "Subagent reviewer.md" {
		t.Errorf("title = %q", up[0].Tool.Title)
	}
	if up[0].Tool.Input == "" {
		t.Errorf("the agent thread id should be the input, got %+v", up[0].Tool)
	}
	if !down[0].Tool.OK {
		t.Errorf("a completed subagent should be ok")
	}
}

// FK-13: warning, then systemError, then error{willRetry:false}, and no
// turn/completed ever. The turn must end exactly once, on the grace timer.
func TestAnErrorEndsTheTurnWithoutTurnCompleted(t *testing.T) {
	shortWatchdogs(t, 150*time.Millisecond, time.Hour, time.Hour)
	s := start(t, `[{"do":"end","outcome":"error","error":"the model is not supported"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeError || end.Error != "the model is not supported" {
		t.Fatalf("turn_finished = %+v", end)
	}
	// Nothing else may end it a second time.
	time.Sleep(300 * time.Millisecond)
	events := s.r.all()
	checkInvariants(t, events)
	if n := count(events, harness.KindTurnFinished); n != 1 {
		t.Fatalf("%d turn_finished events", n)
	}
	if n := count(events, harness.KindNotice); n != 1 {
		t.Errorf("%d notices, want the one warning: %s", n, kinds(events))
	}
}

// FK-13's third form: willRetry:true is remembered but must not end the turn.
func TestAnErrorThatWillRetryDoesNotEndTheTurn(t *testing.T) {
	shortWatchdogs(t, 30*time.Second, time.Hour, time.Hour)
	s := start(t, `[{"do":"end","outcome":"retry","error":"a hiccup"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("turn_finished = %+v, want the retried turn to complete normally", end)
	}
	checkInvariants(t, s.r.all())
}

// C-4: two turn/completed frames for one turn, and only one turn_finished.
func TestASecondTurnCompletedIsSwallowed(t *testing.T) {
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok","twice":true}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.r.wait(t, harness.KindTurnFinished)
	time.Sleep(200 * time.Millisecond)
	events := s.r.all()
	checkInvariants(t, events)
	if n := count(events, harness.KindTurnFinished); n != 1 {
		t.Fatalf("%d turn_finished events for one turn", n)
	}
}

func TestADeathMidTurnEndsTheTurnAndIsFatal(t *testing.T) {
	s := start(t, `[{"do":"text","text":"about to fall over"},{"do":"die","code":3}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeError || !strings.Contains(end.Error, "codex exited") {
		t.Fatalf("turn_finished = %+v", end)
	}
	fatal := s.r.wait(t, harness.KindFatal)
	if fatal.Error == "" {
		t.Errorf("a fatal without a reason")
	}
	s.r.waitClosed(t)
	checkInvariants(t, s.r.all())
}

func TestAHangIsInterrupted(t *testing.T) {
	s := start(t, `[{"do":"text","text":"working"},{"do":"hang"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.r.wait(t, harness.KindText)
	if err := s.a.Interrupt(t.Context()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeInterrupted {
		t.Fatalf("turn_finished = %+v", end)
	}
	checkInvariants(t, s.r.all())

	// Interrupting again, with nothing running, is a no-op.
	if err := s.a.Interrupt(t.Context()); err != nil {
		t.Fatalf("a second Interrupt: %v", err)
	}
}

// The 60 minute silence watchdog: one notice at ten, then the turn ends, the
// process is killed and the adapter is finished.
func TestTheSilenceWatchdogEndsTheTurnAndKillsTheProcess(t *testing.T) {
	shortWatchdogs(t, 30*time.Second, 50*time.Millisecond, 250*time.Millisecond)
	s := start(t, `[{"do":"hang"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeError || end.Error != "the agent stopped reporting" {
		t.Fatalf("turn_finished = %+v", end)
	}
	s.r.wait(t, harness.KindFatal)
	s.r.waitClosed(t)
	events := s.r.all()
	checkInvariants(t, events)
	if count(events, harness.KindNotice) == 0 {
		t.Errorf("the quiet notice never arrived: %s", kinds(events))
	}
	// The process is killed, not left to deliver a late turn/completed.
	select {
	case <-s.a.exited:
	case <-time.After(5 * time.Second):
		t.Errorf("the process outlived the watchdog")
	}
}

func TestTwoTurnsInOneProcess(t *testing.T) {
	s := start(t, `[{"do":"text","text":"first"},{"do":"end","outcome":"ok"}]`, harness.Spec{Effort: "medium"})
	s.r.wait(t, harness.KindSessionID)
	for _, id := range []string{"run_1", "run_2"} {
		if err := s.a.Send(t.Context(), id, "go"); err != nil {
			t.Fatalf("Send %s: %v", id, err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			done := false
			for _, ev := range s.r.all() {
				if ev.Kind == harness.KindTurnFinished && ev.TurnID == id {
					done = true
				}
			}
			if done || time.Now().After(deadline) {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	events := s.r.all()
	checkInvariants(t, events)
	if n := count(events, harness.KindTurnFinished); n != 2 {
		t.Fatalf("%d turns finished, want 2: %s", n, kinds(events))
	}
	// F-5 again: the model and the effort go on *every* turn.
	lines := s.argv(t)
	turns := 0
	for _, l := range lines {
		if l[0] == "turn/start" {
			turns++
			if l[1] != "model=gpt-5.4-mini" || l[2] != "effort=medium" {
				t.Errorf("turn/start recorded %v", l)
			}
		}
	}
	if turns != 2 {
		t.Errorf("%d turn/start calls recorded, want 2", turns)
	}
}

// The restart path: a CLI that died is replaced by a fresh process resuming
// the thread it left behind.
func TestARestartResumesTheThread(t *testing.T) {
	first := start(t, `[{"do":"text","text":"gone"},{"do":"die","code":1}]`, harness.Spec{})
	sid := first.r.wait(t, harness.KindSessionID).Session
	if err := first.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	first.r.waitClosed(t)

	second := start(t, `[{"do":"text","text":"back"},{"do":"end","outcome":"ok"}]`,
		harness.Spec{SessionID: sid})
	got := second.r.wait(t, harness.KindSessionID)
	if got.Session != sid {
		t.Fatalf("thread/resume reported %q, want the id it was given (%q)", got.Session, sid)
	}
	if err := second.a.Send(t.Context(), "run_2", "carry on"); err != nil {
		t.Fatalf("Send after resume: %v", err)
	}
	end := second.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("turn_finished = %+v", end)
	}
	checkInvariants(t, second.r.all())
}

// A resume of an id the fake has never seen must still come back as that id -
// which is what proves thread/resume, not thread/start, was used.
func TestResumeUsesTheGivenThreadID(t *testing.T) {
	const id = "01a05d4f-9999-7101-b75a-900101783ce4"
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`, harness.Spec{SessionID: id})
	got := s.r.wait(t, harness.KindSessionID)
	if got.Session != id {
		t.Fatalf("session_id = %q, want %q", got.Session, id)
	}
}

// F-8: a ServerRequest is answered with a JSON-RPC error, never a decision.
// The fake blocks the script until it is answered, so a turn that completes at
// all is a turn whose request was answered acceptably.
func TestAServerRequestIsAnsweredWithAnError(t *testing.T) {
	s := start(t, `[{"do":"ask","input":"whoami"},{"do":"text","text":"carried on"},
	  {"do":"end","outcome":"ok"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := s.r.wait(t, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("turn_finished = %+v", end)
	}
	events := s.r.all()
	checkInvariants(t, events)
	notices := of(events, harness.KindNotice)
	if len(notices) != 1 || !strings.Contains(notices[0].Error, "unattended") {
		t.Fatalf("notices = %+v", notices)
	}
	if count(events, harness.KindText) != 1 {
		t.Errorf("the script did not carry on after the request: %s", kinds(events))
	}
}

func TestSendWhileATurnIsRunningIsRefused(t *testing.T) {
	s := start(t, `[{"do":"hang"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	if err := s.a.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.a.Send(t.Context(), "run_2", "again"); err == nil {
		t.Fatalf("a second Send while a turn is open was accepted")
	}
}

func TestCloseIsIdempotentAndClosesEvents(t *testing.T) {
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`, harness.Spec{})
	s.r.wait(t, harness.KindSessionID)
	ctx := t.Context()
	if err := s.a.Close(ctx, time.Second); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.a.Close(ctx, time.Second); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	s.r.waitClosed(t)
	// A deliberate shutdown is not a crash.
	if n := count(s.r.all(), harness.KindFatal); n != 0 {
		t.Errorf("Close produced %d fatal events", n)
	}
}

// FK-16: the effort list arrives in its object form and xhigh is filtered out.
func TestDiscoverReadsTheModelList(t *testing.T) {
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	cat, err := Discover(t.Context(), "codex")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cat.Static {
		t.Errorf("the codex catalogue is discovered, not curated")
	}
	if len(cat.Models) != 2 {
		t.Fatalf("models = %+v", cat.Models)
	}
	first := cat.Models[0]
	if first.ID != "gpt-5.4-mini" || first.Label != "GPT-5.4-Mini" || !first.Default {
		t.Errorf("first model = %+v", first)
	}
	if strings.Join(first.Efforts, ",") != "low,medium,high" {
		t.Errorf("efforts = %v, want the low/medium/high intersection", first.Efforts)
	}
	if first.DefaultEffort != "medium" {
		t.Errorf("default effort = %q", first.DefaultEffort)
	}
	if first.Hint == "" {
		t.Errorf("the description should become the hint")
	}
}

func TestTheDescriptorIsRegistered(t *testing.T) {
	d, ok := harness.Get("codex")
	if !ok {
		t.Fatalf("codex is not registered")
	}
	if d.Binary != "codex" || !d.HasEffort || d.New == nil || d.Discover == nil {
		t.Fatalf("descriptor = %+v", d)
	}
	if a := d.New(); a == nil {
		t.Fatalf("New returned nothing")
	}
}

func TestHumanisedTitles(t *testing.T) {
	for in, want := range map[string]string{
		"mcpToolCall": "Mcp tool call",
		"webSearch":   "Web search",
		"plan":        "Plan",
		"":            "Ran a tool",
	} {
		if got := humanise(in); got != want {
			t.Errorf("humanise(%q) = %q, want %q", in, got, want)
		}
	}
}

// Both reasoning array shapes - bare strings (the ThreadItem schema) and
// objects carrying a text field (summary_text, which the fake emits) - are
// accepted, because both are in the generated schema for this binary.
func TestReasoningPartsDecodeInBothShapes(t *testing.T) {
	objects := []json.RawMessage{json.RawMessage(`{"type":"summary_text","text":"one"}`)}
	strs := []json.RawMessage{json.RawMessage(`"two"`)}
	if got := joinTexts(objects); got != "one" {
		t.Errorf("summary_text objects = %q", got)
	}
	if got := joinTexts(strs); got != "two" {
		t.Errorf("bare strings = %q", got)
	}
}

func TestOutputIsTruncated(t *testing.T) {
	long := strings.Repeat("x", harness.ToolOutputLimit+100)
	if got := harness.TruncateOutput(long); len(got) <= harness.ToolOutputLimit ||
		!strings.HasSuffix(got, "truncated]") {
		t.Errorf("TruncateOutput kept %d bytes", len(got))
	}
}
