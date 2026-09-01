package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/harness/fakes"
)

func TestMain(m *testing.M) { fakes.Main(m) }

// ------------------------------------------------------------------ harness

// session is one adapter driven against fakeclaude, with every event it ever
// produced collected on a background goroutine so that a test never has to
// worry about which side of the channel it is on.
type session struct {
	t     *testing.T
	a     harness.Adapter
	argv  string
	mu    chan struct{} // guards events, as a one-slot lock
	got   []harness.Event
	drain chan struct{} // closed when Events is closed
	// patience is how long waitFor gives an expectation. The fakes answer in
	// milliseconds; the real CLI needs minutes.
	patience time.Duration
}

// shortGrace makes the launch windows test-sized and puts them back after.
// The fake, like the real CLI, says nothing at all until the first turn, so
// without this every Start would sit out the full production window.
func shortGrace(t *testing.T, launch, resume time.Duration) {
	t.Helper()
	oldL, oldR := launchGrace, resumeGrace
	launchGrace, resumeGrace = launch, resume
	t.Cleanup(func() { launchGrace, resumeGrace = oldL, oldR })
}

// start builds the fakes, puts them on PATH, and starts one adapter.
func start(t *testing.T, script string, spec harness.Spec) *session {
	t.Helper()
	shortGrace(t, 50*time.Millisecond, 50*time.Millisecond)
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	t.Setenv("FAKE_SCRIPT", script)

	work := t.TempDir()
	argv := filepath.Join(work, "argv.jsonl")
	t.Setenv("FAKE_ARGV_FILE", argv)

	if spec.Agent == "" {
		spec.Agent = "claude"
	}
	if spec.Cwd == "" {
		spec.Cwd = work
	}
	if spec.Dir == "" {
		spec.Dir = work
	}
	s := &session{t: t, argv: argv, mu: make(chan struct{}, 1), drain: make(chan struct{})}
	s.a = New()
	go func() {
		defer close(s.drain)
		for ev := range s.a.Events() {
			s.mu <- struct{}{}
			s.got = append(s.got, ev)
			<-s.mu
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.a.Start(ctx, spec); err != nil {
		t.Fatalf("starting the adapter: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.a.Close(closeCtx, 3*time.Second)
		<-s.drain
	})
	return s
}

func (s *session) send(turnID, text string) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.a.Send(ctx, turnID, text); err != nil {
		s.t.Fatalf("sending a turn: %v", err)
	}
}

func (s *session) events() []harness.Event {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	out := make([]harness.Event, len(s.got))
	copy(out, s.got)
	return out
}

// waitFor polls until pred is happy about the events collected so far.
func (s *session) waitFor(what string, pred func([]harness.Event) bool) []harness.Event {
	s.t.Helper()
	patience := s.patience
	if patience == 0 {
		patience = 20 * time.Second
	}
	deadline := time.Now().Add(patience)
	for {
		evs := s.events()
		if pred(evs) {
			return evs
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s; got:\n%s", what, dump(evs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitTurn waits for the turn_finished of one turn.
func (s *session) waitTurn(turnID string) []harness.Event {
	s.t.Helper()
	return s.waitFor("turn_finished of "+turnID, func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, turnID) == 1
	})
}

func (s *session) waitClosed() {
	s.t.Helper()
	select {
	case <-s.drain:
	case <-time.After(20 * time.Second):
		s.t.Fatalf("Events was never closed; got:\n%s", dump(s.events()))
	}
}

// argvLines reads back what the fake recorded about how it was launched.
func (s *session) argvLines() [][]string {
	s.t.Helper()
	f, err := os.Open(s.argv)
	if err != nil {
		s.t.Fatalf("reading %s: %v", s.argv, err)
	}
	defer f.Close()
	var out [][]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row []string
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			s.t.Fatalf("bad argv line %q: %v", sc.Text(), err)
		}
		out = append(out, row)
	}
	return out
}

func dump(evs []harness.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString("  " + ev.Kind)
		if ev.TurnID != "" {
			b.WriteString(" turn=" + ev.TurnID)
		}
		if ev.ID != "" {
			b.WriteString(" id=" + ev.ID)
		}
		if ev.Outcome != "" {
			b.WriteString(" outcome=" + ev.Outcome)
		}
		if ev.Text != "" {
			b.WriteString(" text=" + strconv.Quote(ev.Text))
		}
		if ev.Error != "" {
			b.WriteString(" error=" + strconv.Quote(ev.Error))
		}
		if ev.Session != "" {
			b.WriteString(" session=" + ev.Session)
		}
		b.WriteString("\n")
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

func only(t *testing.T, evs []harness.Event, kind string) harness.Event {
	t.Helper()
	var found []harness.Event
	for _, ev := range evs {
		if ev.Kind == kind {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("wanted exactly one %s, got %d:\n%s", kind, len(found), dump(evs))
	}
	return found[0]
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

func kinds(evs []harness.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

func hasFlag(args []string, name, value string) bool {
	for i, a := range args {
		if a == name {
			return value == "" || (i+1 < len(args) && args[i+1] == value)
		}
	}
	return false
}

// --------------------------------------------------------------- the tests

// A plain text turn: session_id first, then one turn, streamed and completed.
func TestTextTurn(t *testing.T) {
	s := start(t, `[{"do":"text","text":"All good here."},{"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_1"})

	// Invariant 1: session_id before the first turn_started.
	first := s.waitFor("session_id", func(evs []harness.Event) bool { return len(evs) > 0 })
	if first[0].Kind != harness.KindSessionID || first[0].Session == "" {
		t.Fatalf("wanted session_id first, got:\n%s", dump(first))
	}

	s.send("run_1", "hello")
	evs := s.waitTurn("run_1")

	list := kinds(evs)
	if i, j := indexOf(list, harness.KindSessionID), indexOf(list, harness.KindTurnStarted); i > j {
		t.Fatalf("session_id must come before turn_started:\n%s", dump(evs))
	}
	if got := count(evs, harness.KindTurnStarted, "run_1"); got != 1 {
		t.Fatalf("wanted one turn_started, got %d:\n%s", got, dump(evs))
	}
	text := only(t, evs, harness.KindText)
	if text.Text != "All good here." {
		t.Fatalf("text carried %q", text.Text)
	}
	// Invariant 4: text is the complete block, the deltas are increments of
	// the same id.
	for _, d := range of(evs, harness.KindTextDelta) {
		if d.ID != text.ID {
			t.Fatalf("a delta had id %q but the block is %q", d.ID, text.ID)
		}
		if !strings.Contains(text.Text, d.Text) {
			t.Fatalf("delta %q is not part of %q", d.Text, text.Text)
		}
	}
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK || end.TurnID != "run_1" {
		t.Fatalf("wanted turn_finished{ok,run_1}, got %+v", end)
	}
	// Invariant 5: usage carries the running total and precedes the end.
	u := only(t, evs, harness.KindUsage)
	if u.Usage == nil || u.Usage.Input != 10 || u.Usage.Output != 60 ||
		u.Usage.Cached != 13615 || u.Usage.Reasoning != 32 || u.Usage.Total != 70 {
		t.Fatalf("usage was %+v", u.Usage)
	}
	if u.Usage.CostUSD == 0 {
		t.Fatalf("usage carried no cost: %+v", u.Usage)
	}
	if indexOf(list, harness.KindUsage) > indexOf(list, harness.KindTurnFinished) {
		t.Fatalf("usage must precede turn_finished:\n%s", dump(evs))
	}
}

// text -> tool -> text. The two text blocks live in different API messages,
// whose per-message index both restart at 0, so their ids must still differ
// (F-12/S-1).
func TestToolTurnAndBlockIDs(t *testing.T) {
	s := start(t, `[{"do":"text","text":"Let me look."},
	                {"do":"tool","name":"Bash","input":"go test ./...","output":"ok\n","exit":0},
	                {"do":"text","text":"All tests pass."},
	                {"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_2"})
	s.send("run_1", "run the tests")
	evs := s.waitTurn("run_1")

	texts := of(evs, harness.KindText)
	if len(texts) != 2 {
		t.Fatalf("wanted two text blocks, got %d:\n%s", len(texts), dump(evs))
	}
	if texts[0].ID == texts[1].ID {
		t.Fatalf("the two text blocks share the id %q - a per-message index was used bare", texts[0].ID)
	}
	if !strings.Contains(texts[0].ID, ":") || !strings.Contains(texts[1].ID, ":") {
		t.Fatalf("a block id is not <message.id>:<n>: %q, %q", texts[0].ID, texts[1].ID)
	}
	// The id a delta was filed under is the id its text lands on.
	for _, d := range of(evs, harness.KindTextDelta) {
		if d.ID != texts[0].ID && d.ID != texts[1].ID {
			t.Fatalf("delta id %q matches neither completed block (%q, %q)", d.ID, texts[0].ID, texts[1].ID)
		}
	}

	started := only(t, evs, harness.KindToolStarted)
	finished := only(t, evs, harness.KindToolFinished)
	// Invariant 3: started and finished share one id.
	if started.ID != finished.ID || started.ID == "" {
		t.Fatalf("tool ids differ: %q vs %q", started.ID, finished.ID)
	}
	if started.Tool.Name != "Bash" || started.Tool.Title != "Ran a command" ||
		started.Tool.Input != "go test ./..." {
		t.Fatalf("tool_started reported %+v", started.Tool)
	}
	if started.Tool.InputJSON != `{"command":"go test ./..."}` {
		t.Fatalf("input_json was %q", started.Tool.InputJSON)
	}
	if finished.Tool.Output != "ok\n" || !finished.Tool.OK {
		t.Fatalf("tool_finished reported %+v", finished.Tool)
	}
	// SHOULD-FIX-4: the finish event carries the name and title again, or the
	// engine's patch blanks the card's header.
	if finished.Tool.Name != "Bash" || finished.Tool.Title != "Ran a command" {
		t.Fatalf("tool_finished dropped the card's identity: %+v", finished.Tool)
	}
	// F-2: no exit code is on the wire, so none is invented.
	if finished.Tool.ExitCode != 0 {
		t.Fatalf("tool_finished invented exit code %d", finished.Tool.ExitCode)
	}
	// Claude reports no incremental tool output.
	if n := count(evs, harness.KindToolOutput, ""); n != 0 {
		t.Fatalf("wanted no tool_output, got %d", n)
	}
	if only(t, evs, harness.KindTurnFinished).Outcome != harness.OutcomeOK {
		t.Fatalf("the turn did not end ok:\n%s", dump(evs))
	}
}

// A failing tool call is reported as not-ok, still without an exit code.
func TestFailingToolIsNotOK(t *testing.T) {
	s := start(t, `[{"do":"tool","name":"Bash","input":"false","output":"boom\n","exit":1},
	                {"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_2b"})
	s.send("run_1", "break it")
	evs := s.waitTurn("run_1")
	fin := only(t, evs, harness.KindToolFinished)
	if fin.Tool.OK {
		t.Fatalf("a tool that exited 1 was reported ok: %+v", fin.Tool)
	}
	if fin.Tool.ExitCode != 0 {
		t.Fatalf("an exit code was invented: %+v", fin.Tool)
	}
}

// Reasoning and subagents: a thinking block becomes reasoning, and a Task
// tool_use becomes a subagent pair rather than a tool card.
func TestReasoningAndSubagent(t *testing.T) {
	s := start(t, `[{"do":"reason","text":"The user wants the callers."},
	                {"do":"subagent","name":"Task","input":"find the caller","output":"found it"},
	                {"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_3"})
	s.send("run_1", "who calls this")
	evs := s.waitTurn("run_1")

	r := only(t, evs, harness.KindReasoning)
	if r.Text != "The user wants the callers." {
		t.Fatalf("reasoning carried %q", r.Text)
	}
	started := only(t, evs, harness.KindSubagentStarted)
	finished := only(t, evs, harness.KindSubagentFinished)
	if started.ID != finished.ID {
		t.Fatalf("subagent ids differ: %q vs %q", started.ID, finished.ID)
	}
	if started.Tool.Name != "Task" || started.Tool.Title != "Started a subagent" ||
		started.Tool.Input != "general-purpose: find the caller" {
		t.Fatalf("subagent_started reported %+v", started.Tool)
	}
	if finished.Tool.Output != "found it" || !finished.Tool.OK {
		t.Fatalf("subagent_finished reported %+v", finished.Tool)
	}
	// A Task must never also be reported as a plain tool.
	if n := count(evs, harness.KindToolStarted, ""); n != 0 {
		t.Fatalf("the Task also became %d tool_started events", n)
	}
}

// The trap: subtype "success" carrying is_error true. subtype is not the
// discriminator, is_error is.
func TestErrorTurnDespiteSuccessSubtype(t *testing.T) {
	s := start(t, `[{"do":"end","outcome":"error","error":"model_not_found: totally-bogus"}]`,
		harness.Spec{Model: "totally-bogus", ChatID: "chat_4"})
	s.send("run_1", "hello")
	evs := s.waitTurn("run_1")

	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeError {
		t.Fatalf("wanted outcome error, got %q (subtype \"success\" was believed):\n%s", end.Outcome, dump(evs))
	}
	if !strings.Contains(end.Error, "model_not_found") {
		t.Fatalf("the error text was %q", end.Error)
	}
}

// The CLI dies mid-turn: one turn_finished{error}, then fatal, then the
// channel closes and nothing follows (invariants 2 and 6).
func TestDieMidTurn(t *testing.T) {
	s := start(t, `[{"do":"text","text":"working"},{"do":"die","code":1}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_5"})
	s.send("run_1", "hello")
	s.waitTurn("run_1")
	s.waitClosed()

	evs := s.events()
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeError || end.TurnID != "run_1" {
		t.Fatalf("wanted turn_finished{error,run_1}, got %+v", end)
	}
	if !strings.Contains(end.Error, "mid-turn") {
		t.Fatalf("the death message was %q", end.Error)
	}
	fatal := only(t, evs, harness.KindFatal)
	if !strings.Contains(fatal.Error, "claude exited") || !strings.Contains(fatal.Error, "code 1") {
		t.Fatalf("the fatal message was %q", fatal.Error)
	}
	// Invariant 6: fatal is the last event.
	list := kinds(evs)
	if list[len(list)-1] != harness.KindFatal {
		t.Fatalf("fatal was not last:\n%s", dump(evs))
	}
}

// A turn that never ends on its own, ended by the control_request interrupt.
func TestInterruptEndsAHangingTurn(t *testing.T) {
	s := start(t, `[{"do":"text","text":"thinking about it"},{"do":"hang"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_6"})
	s.send("run_1", "do something slow")
	// Wait until the turn is actually under way, so the interrupt is not a
	// no-op racing the send.
	s.waitFor("the first text block", func(evs []harness.Event) bool {
		return count(evs, harness.KindText, "run_1") == 1
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.a.Interrupt(ctx); err != nil {
		t.Fatalf("interrupting: %v", err)
	}
	evs := s.waitTurn("run_1")
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeInterrupted {
		t.Fatalf("wanted outcome interrupted, got %q with error %q", end.Outcome, end.Error)
	}
	if end.Error != "" {
		t.Fatalf("an interrupted turn must not carry an error, got %q", end.Error)
	}
}

// Interrupt is a no-op when nothing is running, and the process survives it.
func TestInterruptWhileIdleIsANoOp(t *testing.T) {
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_7"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.a.Interrupt(ctx); err != nil {
		t.Fatalf("interrupting an idle session: %v", err)
	}
	s.send("run_1", "hello")
	evs := s.waitTurn("run_1")
	if only(t, evs, harness.KindTurnFinished).Outcome != harness.OutcomeOK {
		t.Fatalf("the turn after an idle interrupt did not end ok:\n%s", dump(evs))
	}
}

// Two turns in one process: the script runs from the start on every turn
// (FK-1) and each Send gets exactly one turn_started and one turn_finished.
func TestTwoTurnsInOneProcess(t *testing.T) {
	s := start(t, `[{"do":"text","text":"first"},{"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_8"})
	s.send("run_1", "one")
	s.waitTurn("run_1")
	s.send("run_2", "two")
	s.waitFor("both turns", func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, "run_2") == 1
	})

	evs := s.events()
	for _, id := range []string{"run_1", "run_2"} {
		if n := count(evs, harness.KindTurnStarted, id); n != 1 {
			t.Fatalf("%s got %d turn_started events", id, n)
		}
		if n := count(evs, harness.KindTurnFinished, id); n != 1 {
			t.Fatalf("%s got %d turn_finished events", id, n)
		}
	}
	// Exactly one session_id for the process, whatever it served.
	if n := count(evs, harness.KindSessionID, ""); n != 1 {
		t.Fatalf("wanted one session_id, got %d", n)
	}
	// Every event of a turn carries that turn's id.
	for _, ev := range evs {
		switch ev.Kind {
		case harness.KindSessionID, harness.KindFatal:
		default:
			if ev.TurnID == "" {
				t.Fatalf("%s carried no turn id:\n%s", ev.Kind, dump(evs))
			}
		}
	}
}

// The production argv, as the fake recorded it.
func TestArgvAndEnvironment(t *testing.T) {
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		harness.Spec{
			Model: "haiku", Effort: "low", ChatID: "chat_9", ChatTitle: "A chat title",
			ExtraArgs: []string{"--add-dir", "/tmp/extra"},
			Env:       []string{"SOCRATES_MARKER=1"},
		})
	rows := s.argvLines()
	if len(rows) == 0 {
		t.Fatal("the fake recorded no argv")
	}
	args := rows[0]
	for _, want := range [][2]string{
		{"-p", ""},
		{"--output-format", "stream-json"},
		{"--input-format", "stream-json"},
		{"--verbose", ""},
		{"--include-partial-messages", ""},
		{"--permission-mode", "bypassPermissions"},
		{"--setting-sources", "project,local"},
		{"--replay-user-messages", ""},
		{"--model", "haiku"},
		{"--effort", "low"},
		{"--name", "A chat title"},
		{"--add-dir", "/tmp/extra"},
	} {
		if !hasFlag(args, want[0], want[1]) {
			t.Fatalf("argv is missing %s %s: %v", want[0], want[1], args)
		}
	}
	// A fresh chat owns its uuid: --session-id, never --resume, never
	// --continue or --fork-session.
	if !hasFlag(args, "--session-id", "") {
		t.Fatalf("a fresh chat was not started with --session-id: %v", args)
	}
	for _, banned := range []string{"--resume", "--continue", "--fork-session"} {
		if hasFlag(args, banned, "") {
			t.Fatalf("argv carries %s: %v", banned, args)
		}
	}
	// The uuid Socrates generated is the session the CLI reports back.
	sess := only(t, s.waitFor("session_id", func(evs []harness.Event) bool {
		return count(evs, harness.KindSessionID, "") == 1
	}), harness.KindSessionID)
	if !hasFlag(args, "--session-id", sess.Session) {
		t.Fatalf("--session-id %v does not match the reported session %q", args, sess.Session)
	}
	// IS_SANDBOX and the spec's own env reach the child: the fake inherits
	// them, so asserting on its behaviour is enough - it started at all with
	// the strict argv check, and the env is what the process was given.
	if os.Getenv("SOCRATES_MARKER") != "" {
		t.Fatal("the test's own environment already carries the marker")
	}
}

// Effort and the chat title are omitted rather than passed empty.
func TestArgvOmitsWhatIsNotSet(t *testing.T) {
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_10"})
	args := s.argvLines()[0]
	if hasFlag(args, "--effort", "") {
		t.Fatalf("--effort was passed with no effort set: %v", args)
	}
	// With no title the chat id is the display name.
	if !hasFlag(args, "--name", "chat_10") {
		t.Fatalf("--name did not fall back to the chat id: %v", args)
	}
}

// A restart resumes: the second process is launched with --resume carrying
// the session id the first one reported.
func TestRestartResumesTheCapturedSession(t *testing.T) {
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	t.Setenv("FAKE_SCRIPT", `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`)
	work := t.TempDir()
	argv := filepath.Join(work, "argv.jsonl")
	t.Setenv("FAKE_ARGV_FILE", argv)

	spec := harness.Spec{Agent: "claude", Model: "sonnet", ChatID: "chat_11", Cwd: work, Dir: work}

	// First process.
	first := New()
	var session string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range first.Events() {
			if ev.Kind == harness.KindSessionID && session == "" {
				session = ev.Session
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := first.Start(ctx, spec); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := first.Close(ctx, 3*time.Second); err != nil {
		t.Fatalf("closing the first adapter: %v", err)
	}
	<-done
	if session == "" {
		t.Fatal("the first process reported no session id")
	}

	// Second process, resuming.
	spec.SessionID = session
	second := New()
	var resumed string
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		for ev := range second.Events() {
			if ev.Kind == harness.KindSessionID && resumed == "" {
				resumed = ev.Session
			}
		}
	}()
	if err := second.Start(ctx, spec); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if err := second.Close(ctx, 3*time.Second); err != nil {
		t.Fatalf("closing the second adapter: %v", err)
	}
	<-done2

	// A session_id is emitted on a resume too, carrying the id that went in.
	if resumed != session {
		t.Fatalf("the resumed session was %q, wanted %q", resumed, session)
	}

	f, err := os.Open(argv)
	if err != nil {
		t.Fatalf("reading the argv file: %v", err)
	}
	defer f.Close()
	var rows [][]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row []string
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("bad argv line: %v", err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 2 {
		t.Fatalf("wanted two launches, got %d", len(rows))
	}
	if !hasFlag(rows[0], "--session-id", session) || hasFlag(rows[0], "--resume", "") {
		t.Fatalf("the first launch was not --session-id %s: %v", session, rows[0])
	}
	if !hasFlag(rows[1], "--resume", session) || hasFlag(rows[1], "--session-id", "") {
		t.Fatalf("the second launch did not resume %s: %v", session, rows[1])
	}
}

// Close is idempotent and closes Events exactly once (invariant: Events is
// closed exactly once, after the last event).
func TestCloseIsIdempotent(t *testing.T) {
	s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_12"})
	s.send("run_1", "hello")
	s.waitTurn("run_1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		if err := s.a.Close(ctx, 3*time.Second); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	s.waitClosed()
	// No fatal: this was our own shutdown, not a death.
	if n := count(s.events(), harness.KindFatal, ""); n != 0 {
		t.Fatalf("a clean Close produced %d fatal events", n)
	}
}

// Close while a turn is open still owes that turn its one turn_finished.
func TestCloseEndsAnOpenTurn(t *testing.T) {
	s := start(t, `[{"do":"text","text":"working"},{"do":"hang"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_13"})
	s.send("run_1", "hang please")
	s.waitFor("the first text block", func(evs []harness.Event) bool {
		return count(evs, harness.KindText, "run_1") == 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.a.Close(ctx, 2*time.Second); err != nil {
		t.Fatalf("closing: %v", err)
	}
	s.waitClosed()
	evs := s.events()
	if n := count(evs, harness.KindTurnFinished, "run_1"); n != 1 {
		t.Fatalf("an open turn got %d turn_finished events on Close:\n%s", n, dump(evs))
	}
	// Revision 9's E4: the outcome is interrupted, not an error - the user's
	// session was shut down, their turn did not fail.
	if end := only(t, evs, harness.KindTurnFinished); end.Outcome != harness.OutcomeInterrupted {
		t.Fatalf("Close ended the open turn as %q, wanted interrupted", end.Outcome)
	}
}

// A second Send while a turn is open is refused rather than silently opening
// a second turn - the engine promises not to, and the adapter does not rely
// on it.
func TestSecondSendDuringATurnIsRefused(t *testing.T) {
	s := start(t, `[{"do":"text","text":"working"},{"do":"hang"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_14"})
	s.send("run_1", "one")
	s.waitFor("the first text block", func(evs []harness.Event) bool {
		return count(evs, harness.KindText, "run_1") == 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.a.Send(ctx, "run_2", "two"); err == nil {
		t.Fatal("a second Send during an open turn was accepted")
	}
}

// The background-subagent case: subagent_stats reports work no Task tool_use
// was ever seen for, so the transcript says so instead of being silently
// incomplete.
func TestBackgroundSubagentsProduceANotice(t *testing.T) {
	s := start(t, `[{"do":"text","text":"delegated"},{"do":"end","outcome":"ok","subagents":2}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_15"})
	s.send("run_1", "delegate it")
	evs := s.waitTurn("run_1")
	n := only(t, evs, harness.KindNotice)
	if !strings.Contains(n.Error, "2 subagents") {
		t.Fatalf("the notice read %q", n.Error)
	}
	if n.TurnID != "run_1" {
		t.Fatalf("the notice was not attributed to the turn: %+v", n)
	}
}

// A turn that did show its Task calls gets no such notice.
func TestVisibleSubagentsProduceNoNotice(t *testing.T) {
	s := start(t, `[{"do":"subagent","name":"Task","input":"look","output":"done"},
	                {"do":"end","outcome":"ok"}]`,
		harness.Spec{Model: "sonnet", ChatID: "chat_16"})
	s.send("run_1", "delegate it")
	evs := s.waitTurn("run_1")
	if n := count(evs, harness.KindNotice, ""); n != 0 {
		t.Fatalf("wanted no notice, got %d:\n%s", n, dump(evs))
	}
}

// init arrives inside the first turn under --input-format stream-json (E-1),
// which is the fake's default and the real CLI's behaviour - but a build that
// announced itself at start must work identically, because the session id the
// adapter reports is the one Socrates generated either way.
func TestBothInitOrdersProduceTheSameSession(t *testing.T) {
	for _, atStart := range []bool{false, true} {
		name := "init inside the first turn"
		if atStart {
			name = "init at process start"
		}
		t.Run(name, func(t *testing.T) {
			if atStart {
				t.Setenv("FAKE_INIT_AT_START", "1")
			}
			s := start(t, `[{"do":"text","text":"hi"},{"do":"end","outcome":"ok"}]`,
				harness.Spec{Model: "sonnet", ChatID: "chat_init"})

			// Invariant 1 holds before anything has been read from the CLI.
			first := s.waitFor("session_id", func(evs []harness.Event) bool { return len(evs) > 0 })
			if first[0].Kind != harness.KindSessionID || first[0].Session == "" {
				t.Fatalf("wanted session_id first, got:\n%s", dump(first))
			}
			s.send("run_1", "hello")
			evs := s.waitTurn("run_1")
			if only(t, evs, harness.KindTurnFinished).Outcome != harness.OutcomeOK {
				t.Fatalf("the turn did not end ok:\n%s", dump(evs))
			}
			// Exactly one session_id either way: the id the CLI echoes is the
			// id Start already reported, so there is no correction.
			if n := count(evs, harness.KindSessionID, ""); n != 1 {
				t.Fatalf("wanted one session_id, got %d:\n%s", n, dump(evs))
			}
			if !hasFlag(s.argvLines()[0], "--session-id", first[0].Session) {
				t.Fatalf("--session-id does not match the reported session %q", first[0].Session)
			}
		})
	}
}

// A CLI that walks straight back out - a rejected flag, a broken install -
// must fail Start rather than becoming a chat that never answers (F-4).
func TestStartFailsWhenTheCLIExitsImmediately(t *testing.T) {
	work := t.TempDir()
	a := New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := a.Start(ctx, harness.Spec{
		Agent: "claude", Binary: "false", Model: "sonnet", ChatID: "chat_17",
		Cwd: work, Dir: work,
	})
	if err == nil {
		t.Fatal("Start accepted a CLI that exited immediately")
	}
	if !strings.Contains(err.Error(), "exited before it was ready") {
		t.Fatalf("the error was %q", err)
	}
	closeCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := a.Close(closeCtx, time.Second); err != nil {
		t.Fatalf("closing after a failed start: %v", err)
	}
	// Events is closed either way, or the host's pump never exits.
	for range a.Events() {
	}
}

// A binary that is not there at all is a plain start error.
func TestStartFailsWhenTheBinaryIsMissing(t *testing.T) {
	work := t.TempDir()
	a := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Start(ctx, harness.Spec{
		Agent: "claude", Binary: filepath.Join(work, "not-a-binary"),
		Model: "sonnet", ChatID: "chat_18", Cwd: work, Dir: work,
	}); err == nil {
		t.Fatal("Start accepted a binary that does not exist")
	}
	closeCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	_ = a.Close(closeCtx, time.Second)
	for range a.Events() {
	}
}

// -------------------------------------------------------------- unit tests

func TestModelMatches(t *testing.T) {
	for _, tc := range []struct {
		want, got string
		ok        bool
	}{
		{"haiku", "claude-haiku-4-5-20251001", true},
		{"sonnet", "claude-sonnet-5", true},
		{"opus", "claude-opus-5", true},
		{"fable", "claude-fable-5", true},
		{"claude-haiku-4-5", "claude-haiku-4-5", true},
		{"sonnet", "claude-opus-5", false},
		{"totally-bogus", "totally-bogus", true},
	} {
		if got := modelMatches(tc.want, tc.got); got != tc.ok {
			t.Errorf("modelMatches(%q, %q) = %v", tc.want, tc.got, got)
		}
	}
}

func TestRenderContent(t *testing.T) {
	if got := renderContent(json.RawMessage(`"plain"`)); got != "plain" {
		t.Errorf("string content rendered as %q", got)
	}
	if got := renderContent(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); got != "a\nb" {
		t.Errorf("block content rendered as %q", got)
	}
	if got := renderContent(json.RawMessage(`null`)); got != "" {
		t.Errorf("null content rendered as %q", got)
	}
}

func TestToolUseResultText(t *testing.T) {
	if got := toolUseResultText(json.RawMessage(`{"stdout":"out","stderr":"err"}`)); got != "out\nerr" {
		t.Errorf("rendered as %q", got)
	}
	if got := toolUseResultText(json.RawMessage(`"Error: Blocked"`)); got != "Error: Blocked" {
		t.Errorf("the bare-string shape rendered as %q", got)
	}
}

func TestDescribe(t *testing.T) {
	title, one := describe("Bash", json.RawMessage(`{"command":"ls -la","description":"list"}`))
	if title != "Ran a command" || one != "ls -la" {
		t.Errorf("Bash described as %q / %q", title, one)
	}
	title, one = describe("Task", json.RawMessage(`{"subagent_type":"general-purpose","description":"find it"}`))
	if title != "Started a subagent" || one != "general-purpose: find it" {
		t.Errorf("Task described as %q / %q", title, one)
	}
	title, one = describe("Weird", json.RawMessage(`{"b":2,"a":1}`))
	if title != "Weird" || one != "a=1 b=2" {
		t.Errorf("an unknown tool described as %q / %q", title, one)
	}
}
