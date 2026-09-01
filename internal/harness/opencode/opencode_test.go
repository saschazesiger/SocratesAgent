package opencode

import (
	"bufio"
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

// ------------------------------------------------------------------ harness

// run is one adapter under test plus everything its events were collected
// into. Every test drives the real adapter against the real fakeopencode
// binary over a real socket; nothing here is stubbed.
type run struct {
	t     *testing.T
	a     harness.Adapter
	argv  string
	dir   string
	mu    sync.Mutex
	evs   []harness.Event
	ended chan struct{}
}

// start builds the fakes, puts them on PATH, starts the adapter against the
// given script and collects its events until Events is closed.
func start(t *testing.T, script string, tweak ...func(*harness.Spec)) *run {
	t.Helper()
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	t.Setenv("FAKE_SCRIPT", script)

	work := t.TempDir()
	argv := filepath.Join(work, "argv.jsonl")
	t.Setenv("FAKE_ARGV_FILE", argv)

	spec := harness.Spec{
		Agent:  "opencode",
		Model:  "opencode|fake-thinker",
		Effort: "high",
		Cwd:    work,
		ChatID: "chat_1",
		Dir:    work,
	}
	for _, f := range tweak {
		f(&spec)
	}

	r := &run{t: t, a: New(), argv: argv, dir: work, ended: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := r.a.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.a.Close(ctx, 3*time.Second)
		select {
		case <-r.ended:
		case <-time.After(10 * time.Second):
			t.Error("Events was never closed")
		}
	})
	go func() {
		for ev := range r.a.Events() {
			r.mu.Lock()
			r.evs = append(r.evs, ev)
			r.mu.Unlock()
		}
		close(r.ended)
	}()
	return r
}

func (r *run) events() []harness.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]harness.Event, len(r.evs))
	copy(out, r.evs)
	return out
}

// wait polls the collected events until want is satisfied.
func (r *run) wait(what string, within time.Duration, want func([]harness.Event) bool) []harness.Event {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for {
		evs := r.events()
		if want(evs) {
			return evs
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s; saw:\n%s", what, dump(evs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// send sends one message and waits for the turn it opens to finish.
func (r *run) send(turnID, text string, within time.Duration) []harness.Event {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.a.Send(ctx, turnID, text); err != nil {
		r.t.Fatalf("Send: %v", err)
	}
	return r.wait("turn_finished of "+turnID, within, func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, turnID) == 1
	})
}

// argvLines is what the fake recorded about how it was launched and called.
func (r *run) argvLines() [][]string {
	r.t.Helper()
	f, err := os.Open(r.argv)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var line []string
		if err := json.Unmarshal(sc.Bytes(), &line); err == nil {
			out = append(out, line)
		}
	}
	return out
}

func dump(evs []harness.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		raw, _ := json.Marshal(ev)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

func count(evs []harness.Event, kind, turnID string) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == kind && (turnID == "" || ev.TurnID == turnID) {
			n++
		}
	}
	return n
}

func of(evs []harness.Event, kind string) []harness.Event {
	var out []harness.Event
	for _, ev := range evs {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func first(t *testing.T, evs []harness.Event, kind string) harness.Event {
	t.Helper()
	got := of(evs, kind)
	if len(got) == 0 {
		t.Fatalf("no %s event; saw:\n%s", kind, dump(evs))
	}
	return got[0]
}

// checkInvariants asserts the seven rules in §2.3 over a finished transcript.
// Every test that produces a transcript runs it, so a rule cannot be broken by
// one path and hidden by another.
func checkInvariants(t *testing.T, evs []harness.Event) {
	t.Helper()

	// 1: session_id before the first turn_started.
	sessionAt, startedAt := -1, -1
	for i, ev := range evs {
		if ev.Kind == harness.KindSessionID && sessionAt < 0 {
			sessionAt = i
		}
		if ev.Kind == harness.KindTurnStarted && startedAt < 0 {
			startedAt = i
		}
	}
	if sessionAt < 0 {
		t.Errorf("no session_id event")
	}
	if startedAt >= 0 && sessionAt > startedAt {
		t.Errorf("session_id arrived after the first turn_started")
	}

	// 2: exactly one turn_started and one turn_finished per turn.
	started, finished := map[string]int{}, map[string]int{}
	open := ""
	for _, ev := range evs {
		switch ev.Kind {
		case harness.KindTurnStarted:
			started[ev.TurnID]++
			if open != "" {
				t.Errorf("turn %s started while %s was still open", ev.TurnID, open)
			}
			open = ev.TurnID
		case harness.KindTurnFinished:
			finished[ev.TurnID]++
			if open != ev.TurnID {
				t.Errorf("turn_finished for %s but %q was open", ev.TurnID, open)
			}
			open = ""
			switch ev.Outcome {
			case harness.OutcomeOK, harness.OutcomeError, harness.OutcomeInterrupted:
			default:
				t.Errorf("turn_finished with an unknown outcome %q", ev.Outcome)
			}
		}
	}
	for id, n := range finished {
		if n != 1 {
			t.Errorf("turn %s finished %d times, want exactly 1", id, n)
		}
		if started[id] != 1 {
			t.Errorf("turn %s started %d times, want exactly 1", id, started[id])
		}
	}

	// 3: a tool call's three events share one id, in order.
	toolOpen := map[string]bool{}
	for _, ev := range evs {
		switch ev.Kind {
		case harness.KindToolStarted:
			if ev.ID == "" {
				t.Errorf("tool_started without an id")
			}
			toolOpen[ev.ID] = true
		case harness.KindToolOutput, harness.KindToolFinished:
			if !toolOpen[ev.ID] {
				t.Errorf("%s for %q which never started", ev.Kind, ev.ID)
			}
			if ev.Kind == harness.KindToolFinished {
				delete(toolOpen, ev.ID)
			}
		}
	}

	// 4: the deltas of a block add up to its text.
	deltas := map[string]string{}
	for _, ev := range evs {
		switch ev.Kind {
		case harness.KindTextDelta:
			deltas[ev.ID] += ev.Text
		case harness.KindText:
			if got, ok := deltas[ev.ID]; ok && got != ev.Text {
				t.Errorf("block %s: deltas spelled %q but the text was %q", ev.ID, got, ev.Text)
			}
			delete(deltas, ev.ID)
		}
	}

	// 5: usage carries a running total, so it never goes backwards.
	var last harness.Usage
	for _, ev := range evs {
		if ev.Kind != harness.KindUsage || ev.Usage == nil {
			continue
		}
		if ev.Usage.Input < last.Input || ev.Usage.Output < last.Output || ev.Usage.CostUSD < last.CostUSD {
			t.Errorf("usage went backwards: %+v then %+v", last, *ev.Usage)
		}
		last = *ev.Usage
	}

	// 6: nothing after a fatal.
	for i, ev := range evs {
		if ev.Kind == harness.KindFatal && i != len(evs)-1 {
			t.Errorf("%d events arrived after the fatal", len(evs)-1-i)
		}
	}

	// Every event that belongs to a turn names it.
	for _, ev := range evs {
		switch ev.Kind {
		case harness.KindSessionID, harness.KindFatal:
		default:
			if ev.TurnID == "" {
				t.Errorf("%s carries no turn id", ev.Kind)
			}
		}
	}
}

// ------------------------------------------------------------------- tests

func TestTextTurn(t *testing.T) {
	r := start(t, `[{"do":"text","text":"All good here."},{"do":"end","outcome":"ok"}]`)

	evs := r.send("run_1", "hello", 20*time.Second)
	checkInvariants(t, evs)

	if got := first(t, evs, harness.KindSessionID).Session; !strings.HasPrefix(got, "ses_") {
		t.Errorf("session id %q does not look like one", got)
	}
	text := of(evs, harness.KindText)
	if len(text) != 1 || text[0].Text != "All good here." {
		t.Fatalf("want one complete text block, got:\n%s", dump(evs))
	}
	if len(of(evs, harness.KindTextDelta)) == 0 {
		t.Errorf("no text_delta at all; the stream was not followed")
	}
	fin := first(t, evs, harness.KindTurnFinished)
	if fin.Outcome != harness.OutcomeOK || fin.Error != "" {
		t.Errorf("turn_finished = %+v, want a clean ok", fin)
	}
	u := first(t, evs, harness.KindUsage)
	if u.Usage == nil || u.Usage.Input == 0 || u.Usage.CostUSD == 0 {
		t.Errorf("usage = %+v, want the step's tokens and cost", u.Usage)
	}
}

func TestToolTurnKeepsStepsApart(t *testing.T) {
	r := start(t, `[{"do":"text","text":"Let me look."},
	                {"do":"tool","name":"bash","input":"go test ./...","output":"ok\n","exit":0},
	                {"do":"text","text":"All tests pass."},
	                {"do":"end","outcome":"ok"}]`)

	evs := r.send("run_1", "run the tests", 20*time.Second)
	checkInvariants(t, evs)

	texts := of(evs, harness.KindText)
	if len(texts) != 2 {
		t.Fatalf("want two text blocks, got %d:\n%s", len(texts), dump(evs))
	}
	// F-12: textID restarts at text-0 in every step, so the assistant message
	// id is what keeps the two blocks apart.
	if texts[0].ID == texts[1].ID {
		t.Errorf("both text blocks landed on the id %q", texts[0].ID)
	}
	for _, ev := range texts {
		if !strings.Contains(ev.ID, ":") {
			t.Errorf("block id %q is not <assistantMessageID>:<textID>", ev.ID)
		}
	}

	started := of(evs, harness.KindToolStarted)
	finished := of(evs, harness.KindToolFinished)
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("want one tool call, got %d/%d:\n%s", len(started), len(finished), dump(evs))
	}
	if started[0].ID != finished[0].ID {
		t.Errorf("tool ids differ: %q then %q", started[0].ID, finished[0].ID)
	}
	if started[0].Tool.Name != "bash" || started[0].Tool.Title != "Ran a command" {
		t.Errorf("tool card = %+v", started[0].Tool)
	}
	if started[0].Tool.Input != "go test ./..." {
		t.Errorf("tool input summary = %q", started[0].Tool.Input)
	}
	if !strings.Contains(started[0].Tool.InputJSON, `"command"`) {
		t.Errorf("tool input_json = %q", started[0].Tool.InputJSON)
	}
	if !finished[0].Tool.OK || finished[0].Tool.ExitCode != 0 {
		t.Errorf("finished tool = %+v, want ok with exit 0", finished[0].Tool)
	}
	if !strings.Contains(finished[0].Tool.Output, "ok") {
		t.Errorf("tool output = %q", finished[0].Tool.Output)
	}

	// One usage per step.ended, each carrying the running total.
	usage := of(evs, harness.KindUsage)
	if len(usage) < 2 {
		t.Fatalf("want a usage per step, got %d:\n%s", len(usage), dump(evs))
	}
	last := usage[len(usage)-1].Usage
	if last.Input <= usage[0].Usage.Input {
		t.Errorf("usage did not accumulate across steps: %+v then %+v", usage[0].Usage, last)
	}
}

func TestReasoningBecomesAReasoningEvent(t *testing.T) {
	r := start(t, `[{"do":"reason","text":"The user wants the tests run."},
	                {"do":"text","text":"Running them."},
	                {"do":"end","outcome":"ok"}]`)

	evs := r.send("run_1", "think", 20*time.Second)
	checkInvariants(t, evs)

	reason := of(evs, harness.KindReasoning)
	if len(reason) != 1 {
		t.Fatalf("want one reasoning block, got %d:\n%s", len(reason), dump(evs))
	}
	if reason[0].Text != "The user wants the tests run." {
		t.Errorf("reasoning text = %q", reason[0].Text)
	}
	if !strings.Contains(reason[0].ID, ":reasoning-0") {
		t.Errorf("reasoning id = %q, want <assistantMessageID>:<reasoningID>", reason[0].ID)
	}
}

func TestToolFailedCarriesTheErrorMessage(t *testing.T) {
	r := start(t, `[{"do":"tool","name":"bash","input":"false","output":"exit status 1","exit":1},
	                {"do":"text","text":"That failed."},
	                {"do":"end","outcome":"ok"}]`)

	evs := r.send("run_1", "break it", 20*time.Second)
	checkInvariants(t, evs)

	fin := of(evs, harness.KindToolFinished)
	if len(fin) != 1 {
		t.Fatalf("want one finished tool, got %d:\n%s", len(fin), dump(evs))
	}
	if fin[0].Tool.OK {
		t.Errorf("a failed tool reported ok")
	}
	if fin[0].Tool.Output != "exit status 1" {
		t.Errorf("failed tool output = %q, want error.message verbatim", fin[0].Tool.Output)
	}
	if fin[0].Tool.Name != "bash" || fin[0].Tool.Title != "Ran a command" {
		t.Errorf("a failed tool lost its heading: %+v", fin[0].Tool)
	}
}

func TestSessionErrorEndsTheTurnWithItsMessage(t *testing.T) {
	const msg = "Unsupported API for openrouter/anthropic/claude-haiku-4.5: aisdk:@openrouter/ai-sdk-provider"
	r := start(t, `[{"do":"text","text":"Trying."},{"do":"end","outcome":"error","error":`+
		mustJSON(msg)+`}]`)

	// No step.ended follows a session.error, so this turn can only end through
	// the backstop poll: three absent polls, two seconds apart.
	evs := r.send("run_1", "use a bad model", 30*time.Second)
	checkInvariants(t, evs)

	fin := first(t, evs, harness.KindTurnFinished)
	if fin.Outcome != harness.OutcomeError {
		t.Fatalf("turn_finished = %+v, want error", fin)
	}
	if fin.Error != msg {
		t.Errorf("run error = %q, want the provider's message verbatim", fin.Error)
	}
	notices := of(evs, harness.KindNotice)
	if len(notices) == 0 {
		t.Fatalf("a session.error produced no notice:\n%s", dump(evs))
	}
	var sawMessage, sawBackstop bool
	for _, n := range notices {
		if n.Error == msg {
			sawMessage = true
		}
		if strings.Contains(n.Error, "idle check") {
			sawBackstop = true
		}
	}
	if !sawMessage {
		t.Errorf("no notice carried the error message; saw %v", notices)
	}
	if !sawBackstop {
		t.Errorf("a turn closed by the backstop did not say so in the transcript")
	}
}

func TestServerDeathEndsTheTurnAndClosesEvents(t *testing.T) {
	r := start(t, `[{"do":"text","text":"Half an ans"},{"do":"die","code":3}]`)

	evs := r.send("run_1", "crash", 30*time.Second)
	// The channel closes on its own after a fatal; nothing else may follow.
	select {
	case <-r.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("Events was not closed after the fatal")
	}
	evs = r.events()
	checkInvariants(t, evs)

	fin := first(t, evs, harness.KindTurnFinished)
	if fin.Outcome != harness.OutcomeError {
		t.Errorf("turn_finished = %+v, want error", fin)
	}
	fatal := of(evs, harness.KindFatal)
	if len(fatal) != 1 {
		t.Fatalf("want exactly one fatal, got %d:\n%s", len(fatal), dump(evs))
	}
	if !strings.Contains(fatal[0].Error, "exited") {
		t.Errorf("fatal = %q, want it to say the server exited", fatal[0].Error)
	}
}

func TestHangIsEndedByAnInterrupt(t *testing.T) {
	r := start(t, `[{"do":"text","text":"Working on it."},{"do":"hang"}]`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.a.Send(ctx, "run_1", "sleep forever"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.wait("the text of the hanging turn", 20*time.Second, func(evs []harness.Event) bool {
		return len(of(evs, harness.KindText)) == 1
	})
	if n := count(r.events(), harness.KindTurnFinished, ""); n != 0 {
		t.Fatalf("the turn ended by itself before the interrupt")
	}
	if err := r.a.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// The interrupt arms the fast confirmation poll, so this lands in about a
	// second rather than waiting out the backstop.
	evs := r.wait("turn_finished after the interrupt", 5*time.Second, func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, "run_1") == 1
	})
	checkInvariants(t, evs)
	if fin := first(t, evs, harness.KindTurnFinished); fin.Outcome != harness.OutcomeInterrupted {
		t.Errorf("turn_finished = %+v, want interrupted", fin)
	}
}

// TestDoubleStepEndedProducesOneTurnFinished is FK-22: the session leaves
// /api/session/active first and two step.ended{finish:"stop"} frames follow it,
// so the backstop poll and both frames each try to end the same turn.
func TestDoubleStepEndedProducesOneTurnFinished(t *testing.T) {
	r := start(t, `[{"do":"text","text":"Done."},{"do":"end","outcome":"ok","twice":true}]`)

	evs := r.send("run_1", "hello", 30*time.Second)
	// Give the second step.ended, 200 ms behind the first, time to arrive and
	// be swallowed.
	time.Sleep(1500 * time.Millisecond)
	evs = r.events()
	checkInvariants(t, evs)

	if n := count(evs, harness.KindTurnFinished, "run_1"); n != 1 {
		t.Fatalf("turn_finished %d times, want exactly 1:\n%s", n, dump(evs))
	}
}

// TestAskIsAnsweredAutomatically covers the fallback for an install where
// OPENCODE_PERMISSION does not take: an unanswered ask blocks the turn forever.
func TestAskIsAnsweredAutomatically(t *testing.T) {
	r := start(t, `[{"do":"ask","input":"echo hi"},
	                {"do":"text","text":"Allowed."},
	                {"do":"end","outcome":"ok"}]`)

	evs := r.send("run_1", "write a file", 30*time.Second)
	checkInvariants(t, evs)

	if fin := first(t, evs, harness.KindTurnFinished); fin.Outcome != harness.OutcomeOK {
		t.Errorf("turn_finished = %+v, want ok", fin)
	}
	var saw bool
	for _, n := range of(evs, harness.KindNotice) {
		if strings.Contains(n.Error, "permission") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("the auto-reply left no notice in the transcript:\n%s", dump(evs))
	}

	var replied bool
	for _, line := range r.argvLines() {
		if len(line) == 2 && strings.Contains(line[0], "/permission/") && strings.HasSuffix(line[0], "/reply") {
			if line[1] != `{"reply":"always"}` {
				t.Errorf("permission reply body = %s, want a plain always", line[1])
			}
			replied = true
		}
	}
	if !replied {
		t.Errorf("the permission reply route was never called")
	}
}

func TestResumeUsesTheSessionItWasGiven(t *testing.T) {
	const id = "ses_resumed0001"
	r := start(t, `[{"do":"text","text":"Still here."},{"do":"end","outcome":"ok"}]`,
		func(s *harness.Spec) { s.SessionID = id })

	// Invariant 1 wants session_id even when it is the id that was passed in;
	// a created session would have been called ses_fake0001.
	evs := r.wait("session_id", 10*time.Second, func(evs []harness.Event) bool {
		return len(of(evs, harness.KindSessionID)) == 1
	})
	if got := first(t, evs, harness.KindSessionID).Session; got != id {
		t.Fatalf("session id = %q, want the one that was passed in", got)
	}
	evs = r.send("run_1", "are you there", 20*time.Second)
	checkInvariants(t, evs)
	if fin := first(t, evs, harness.KindTurnFinished); fin.Outcome != harness.OutcomeOK {
		t.Errorf("a resumed session could not take a turn: %+v", fin)
	}
}

func TestTwoTurnsRunTheSameWay(t *testing.T) {
	r := start(t, `[{"do":"text","text":"First."},{"do":"end","outcome":"ok"}]`)

	r.send("run_1", "one", 20*time.Second)
	evs := r.send("run_2", "two", 20*time.Second)
	checkInvariants(t, evs)

	if n := count(evs, harness.KindTurnFinished, ""); n != 2 {
		t.Fatalf("want two finished turns, got %d:\n%s", n, dump(evs))
	}
	// F-9's baseline is what keeps the first turn's replayed text out of the
	// second turn: without it the second turn would carry two text blocks.
	var second int
	for _, ev := range evs {
		if ev.Kind == harness.KindText && ev.TurnID == "run_2" {
			second++
		}
	}
	if second != 1 {
		t.Errorf("the second turn carried %d text blocks, want 1:\n%s", second, dump(evs))
	}
}

func TestLaunchModelAndPermission(t *testing.T) {
	r := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		func(s *harness.Spec) { s.ExtraArgs = []string{"--from-settings"} })
	r.send("run_1", "hello", 20*time.Second)

	lines := r.argvLines()
	if len(lines) == 0 {
		t.Fatal("the fake recorded nothing")
	}
	argv := lines[0]
	joined := strings.Join(argv, " ")
	for _, want := range []string{"serve", "--port 0", "--hostname 127.0.0.1", "--from-settings"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %v is missing %q", argv, want)
		}
	}
	// F-10: the value of OPENCODE_PERMISSION is JSON, so "allow" carries its
	// quotes. Without it every tool call would wait on a permission event.
	if argv[len(argv)-1] != `OPENCODE_PERMISSION="allow"` {
		t.Errorf("OPENCODE_PERMISSION was recorded as %q", argv[len(argv)-1])
	}

	var body string
	for _, line := range lines {
		if len(line) == 2 && strings.HasSuffix(line[0], "/model") {
			body = line[1]
		}
	}
	if body == "" {
		t.Fatal("the model was never set on the session")
	}
	var got struct {
		Model modelRef `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the model body is not JSON: %v", err)
	}
	want := modelRef{ID: "fake-thinker", ProviderID: "opencode", Variant: "high"}
	if got.Model != want {
		t.Errorf("model body = %+v, want %+v", got.Model, want)
	}
	if !strings.Contains(body, `"id"`) || strings.Contains(body, "modelID") {
		t.Errorf("model body = %s, the key must be id, not modelID", body)
	}
}

// TestEveryRequestIsAuthenticated is the other half of the Basic-auth pin: the
// fake answers 401 to anything unauthenticated, /api/health included, so a
// start that gets through has authenticated - and this checks the credentials
// are where they belong rather than in a Bearer header.
func TestEveryRequestIsAuthenticated(t *testing.T) {
	c := newClient("http://127.0.0.1:1", "s3cret")
	req, err := c.request(context.Background(), "GET", "/api/health", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != serverUsername || pass != "s3cret" {
		t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
	}
	if req.Header.Get("Authorization") == "Bearer s3cret" {
		t.Errorf("the password was sent as a bearer token, which this server does not accept")
	}
}

func TestDiscoverListsTheConnectedModels(t *testing.T) {
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	t.Setenv("FAKE_SCRIPT", "")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cat, err := Discover(ctx, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cat.Static {
		t.Errorf("the OpenCode catalogue is discovered, not curated")
	}
	if len(cat.Models) != 2 {
		t.Fatalf("want two models, got %+v", cat.Models)
	}
	byID := map[string]harness.Model{}
	for _, m := range cat.Models {
		byID[m.ID] = m
	}
	thinker, ok := byID["opencode|fake-thinker"]
	if !ok {
		t.Fatalf("no opencode|fake-thinker in %+v", cat.Models)
	}
	if thinker.Label != "Fake Thinker" || thinker.Group != "opencode" {
		t.Errorf("thinker = %+v", thinker)
	}
	// F-13: the variants map form.
	if strings.Join(thinker.Efforts, ",") != "low,medium,high" {
		t.Errorf("efforts = %v", thinker.Efforts)
	}
	plain, ok := byID["opencode|fake-plain"]
	if !ok {
		t.Fatalf("no opencode|fake-plain in %+v", cat.Models)
	}
	// F-13: and the empty array form, which a map-only decoder chokes on.
	if len(plain.Efforts) != 0 {
		t.Errorf("a model with no variants reported efforts %v", plain.Efforts)
	}
}

func TestParseModel(t *testing.T) {
	for _, tc := range []struct {
		in, effort string
		want       *modelRef
		wantErr    bool
	}{
		{in: "", want: nil},
		{in: "opencode|big-pickle", want: &modelRef{ID: "big-pickle", ProviderID: "opencode"}},
		{in: "openrouter|anthropic/claude-haiku-4.5", effort: "high",
			want: &modelRef{ID: "anthropic/claude-haiku-4.5", ProviderID: "openrouter", Variant: "high"}},
		{in: "big-pickle", wantErr: true},
		{in: "|big-pickle", wantErr: true},
		{in: "opencode|", wantErr: true},
	} {
		got, err := parseModel(tc.in, tc.effort)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseModel(%q) = %+v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseModel(%q): %v", tc.in, err)
			continue
		}
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseModel(%q) = %+v, want nil", tc.in, got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("parseModel(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestSessionErrorMessagePrefersTheProvidersSentence(t *testing.T) {
	// Every error variant in EventSessionError - ProviderAuthError,
	// UnknownError, APIError and the rest - is {"name":…,"data":{"message":…}}.
	var d sessionErrorData
	raw := json.RawMessage(`{"sessionID":"ses_1","error":{"name":"UnknownError","data":{"message":"Unsupported API"}}}`)
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d.Raw = raw
	if got := d.message(); got != "Unsupported API" {
		t.Errorf("message = %q", got)
	}

	var named sessionErrorData
	_ = json.Unmarshal([]byte(`{"error":{"name":"MessageOutputLengthError","data":{}}}`), &named)
	if got := named.message(); got != "MessageOutputLengthError" {
		t.Errorf("message = %q, want the error's name when it carries no sentence", got)
	}

	var empty sessionErrorData
	empty.Raw = json.RawMessage(`{"error":{}}`)
	if got := empty.message(); got == "" {
		t.Errorf("an error with nothing in it still needs a message")
	}
}

func mustJSON(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// ------------------------------------------------------- the two hard rules

// openTurn puts an adapter into the state Send leaves it in, without a server,
// so the two rules that are hard to provoke over a socket - the replay
// baseline and the single turn end - can be tested directly.
func openTurn(t *testing.T, turnID string, lastSeq int64) *adapter {
	t.Helper()
	a := newAdapter()
	t.Cleanup(a.cancel)
	a.session = "ses_test"
	a.lastSeq = lastSeq
	a.turnID = turnID
	a.turnOnce = &sync.Once{}
	a.startOnce = &sync.Once{}
	a.turnDone = make(chan struct{})
	a.open = true
	return a
}

func durable(typ string, seq int64, data string) frame {
	f := frame{ID: "evt_test", Type: typ, Data: json.RawMessage(data)}
	f.Durable = new(struct {
		AggregateID string `json:"aggregateID"`
		Seq         int64  `json:"seq"`
		Version     int    `json:"version"`
	})
	f.Durable.AggregateID = "ses_test"
	f.Durable.Seq = seq
	f.Durable.Version = 1
	return f
}

func ephemeral(typ, data string) frame {
	return frame{ID: "evt_test", Type: typ, Data: json.RawMessage(data)}
}

func drain(a *adapter) []harness.Event {
	var out []harness.Event
	for {
		select {
		case ev := <-a.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestReplayBaselineIgnoresHistory is F-9. The session-scoped stream replays
// the whole durable history on every connect, so on a resumed session every
// previous turn's text - and every previous step.ended{finish:"stop"} - arrives
// again. Only the seq baseline keeps them out of the turn in flight.
func TestReplayBaselineIgnoresHistory(t *testing.T) {
	a := openTurn(t, "run_2", 10)

	// History: at or below the baseline, whatever it says.
	a.handle(durable(evTextEnded, 4, `{"assistantMessageID":"msg_old","textID":"text-0","text":"an answer from last week"}`))
	a.handle(durable(evPrompted, 9, `{"messageID":"msg_old"}`))
	a.handle(durable(evStepEnded, 10, `{"assistantMessageID":"msg_old","finish":"stop","cost":1,"tokens":{"input":9,"output":9}}`))
	if got := drain(a); len(got) != 0 {
		t.Fatalf("replayed history produced %d events:\n%s", len(got), dump(got))
	}

	// This turn: strictly after it.
	a.handle(durable(evPrompted, 11, `{"messageID":"msg_new"}`))
	a.handle(ephemeral(evTextDelta, `{"assistantMessageID":"msg_new","textID":"text-0","delta":"hi"}`))
	a.handle(durable(evTextEnded, 12, `{"assistantMessageID":"msg_new","textID":"text-0","text":"hi"}`))
	got := drain(a)
	if len(of(got, harness.KindTurnStarted)) != 1 {
		t.Errorf("want one turn_started, got:\n%s", dump(got))
	}
	if txt := of(got, harness.KindText); len(txt) != 1 || txt[0].Text != "hi" {
		t.Errorf("want the new turn's text only, got:\n%s", dump(got))
	}

	// And nothing at all while no turn is open - which is the state a resumed
	// session's first connect happens in.
	a.mu.Lock()
	a.open = false
	a.mu.Unlock()
	a.handle(durable(evTextEnded, 13, `{"assistantMessageID":"msg_new","textID":"text-1","text":"after the turn"}`))
	a.handle(ephemeral(evTextDelta, `{"assistantMessageID":"msg_new","textID":"text-1","delta":"x"}`))
	if got := drain(a); len(got) != 0 {
		t.Errorf("events arrived with no turn open:\n%s", dump(got))
	}
}

// TestCloseTurnLetsOneTriggerThrough is invariant 7. This adapter has five
// things that can end a turn - the confirmation poll, the backstop, an
// interrupt, a server that stops answering and a server that dies - and two of
// them landing together must still be one turn_finished.
func TestCloseTurnLetsOneTriggerThrough(t *testing.T) {
	// Two triggers land microseconds apart, not on a schedule anyone can plan
	// for, so this runs the collision many times over rather than once.
	for round := 0; round < 500; round++ {
		a := openTurn(t, "run_1", 0)

		var wg sync.WaitGroup
		begin := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-begin
				switch i % 3 {
				case 0:
					a.closeTurn("", "")
				case 1:
					a.closeTurn(harness.OutcomeError, "the server stopped answering")
				default:
					a.closeTurn(harness.OutcomeInterrupted, "")
				}
			}(i)
		}
		close(begin)
		wg.Wait()

		got := drain(a)
		if n := count(got, harness.KindTurnFinished, "run_1"); n != 1 {
			t.Fatalf("round %d: turn_finished %d times, want exactly 1:\n%s", round, n, dump(got))
		}
		// Invariant 2 all the same: the turn is started before it is finished,
		// even when nothing ever sent a prompted.
		if got[0].Kind != harness.KindTurnStarted {
			t.Fatalf("round %d: the first event was %s, want turn_started:\n%s", round, got[0].Kind, dump(got))
		}
		a.cancel()
	}
}

// TestServerWideStreamIsOnlyReadForIncrements pins the split between the two
// SSE connections. Measured against opencode 1.17.13: the session-scoped
// stream carries only durable events, and the ephemeral *.delta frames are
// published on the server-wide one alone - so the adapter follows both, and
// takes nothing but the increments from the wide one.
func TestServerWideStreamIsOnlyReadForIncrements(t *testing.T) {
	a := openTurn(t, "run_1", 10)

	// An increment for this session: taken.
	mine := ephemeral(evTextDelta, `{"sessionID":"ses_test","assistantMessageID":"msg_1","textID":"text-0","delta":"hello"}`)
	mine.global = true
	a.handle(mine)
	if got := drain(a); len(got) != 1 || got[0].Kind != harness.KindTextDelta || got[0].Text != "hello" {
		t.Fatalf("the increment was not taken:\n%s", dump(got))
	}

	// Another session on the same server - OpenCode opens one of its own to
	// title a chat - is not this chat.
	other := ephemeral(evTextDelta, `{"sessionID":"ses_someone_else","assistantMessageID":"msg_2","textID":"text-0","delta":"not mine"}`)
	other.global = true
	a.handle(other)

	// And anything durable would arrive twice, once per stream.
	dup := durable(evTextEnded, 11, `{"sessionID":"ses_test","assistantMessageID":"msg_1","textID":"text-0","text":"hello"}`)
	dup.global = true
	a.handle(dup)

	if got := drain(a); len(got) != 0 {
		t.Fatalf("the server-wide stream leaked %d events:\n%s", len(got), dump(got))
	}

	// The same durable event off the session's own stream is the one that
	// counts.
	a.handle(durable(evTextEnded, 11, `{"sessionID":"ses_test","assistantMessageID":"msg_1","textID":"text-0","text":"hello"}`))
	if got := drain(a); len(got) != 1 || got[0].Kind != harness.KindText {
		t.Fatalf("the session stream's own event was lost:\n%s", dump(got))
	}
}

// TestCloseEndsAnOpenTurnAsInterrupted is the Adapter contract's rule for a
// host that is shut down mid-turn: the turn ends before the channel does.
func TestCloseEndsAnOpenTurnAsInterrupted(t *testing.T) {
	r := start(t, `[{"do":"text","text":"Working on it."},{"do":"hang"}]`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.a.Send(ctx, "run_1", "sleep forever"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.wait("the text of the hanging turn", 20*time.Second, func(evs []harness.Event) bool {
		return len(of(evs, harness.KindText)) == 1
	})

	if err := r.a.Close(ctx, 3*time.Second); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-r.ended:
	case <-time.After(15 * time.Second):
		t.Fatal("Events was not closed")
	}
	evs := r.events()
	checkInvariants(t, evs)
	fin := first(t, evs, harness.KindTurnFinished)
	if fin.Outcome != harness.OutcomeInterrupted {
		t.Errorf("turn_finished = %+v, want interrupted", fin)
	}
	if n := count(evs, harness.KindTurnFinished, "run_1"); n != 1 {
		t.Errorf("turn_finished %d times, want exactly 1", n)
	}
}
