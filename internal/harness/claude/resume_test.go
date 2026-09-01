package claude

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/harness/fakes"
)

// R-3, hermetically.
//
// FAKE_GHOST_RESUME=1 makes fakeclaude reproduce the one behaviour of the real
// CLI that no script can express and that the first cut of this adapter got
// wrong: a --resume of a session that does not exist writes no system/init,
// then one unprompted
//
//	result{subtype:"error_during_execution", is_error:true, num_turns:0}
//
// and exits 1 with "No conversation found with session ID" on stderr. Live
// that lands 1.3-3.9 s after launch, which is before Start returns, between
// Start and Send, or inside the turn depending on nothing the adapter
// controls - so each of the three is a test.
//
// The flag is consumed per session id through FAKE_STATE_DIR, so the adapter's
// one relaunch finds a working CLI.

// ghostSession starts an adapter against a session id the fake will refuse.
// gap is how long Start is given to notice: with the production window the
// death resolves inside Start, with a short one it lands afterwards.
func ghostSession(t *testing.T, id string, window time.Duration) *session {
	t.Helper()
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	t.Setenv("FAKE_SCRIPT", `[{"do":"text","text":"back again"},{"do":"end","outcome":"ok"}]`)
	t.Setenv("FAKE_GHOST_RESUME", "1")

	work := t.TempDir()
	t.Setenv("FAKE_STATE_DIR", filepath.Join(work, "state"))
	argv := filepath.Join(work, "argv.jsonl")
	t.Setenv("FAKE_ARGV_FILE", argv)

	s := &session{t: t, argv: argv, mu: make(chan struct{}, 1), drain: make(chan struct{})}
	s.a = newAdapterWithGrace(window, window)
	go func() {
		defer close(s.drain)
		for ev := range s.a.Events() {
			s.mu <- struct{}{}
			s.got = append(s.got, ev)
			<-s.mu
		}
	}()
	// Registered before Start, so a Start that fails still joins the adapter's
	// goroutines instead of leaving them running into the next test.
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = s.a.Close(closeCtx, 3*time.Second)
		<-s.drain
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.a.Start(ctx, harness.Spec{
		Agent: "claude", Model: "sonnet", ChatID: "chat_ghost",
		SessionID: id, Cwd: work, Dir: work,
	}); err != nil {
		t.Fatalf("Start on a session the CLI cannot read must not fail the chat: %v", err)
	}
	return s
}

// assertRecovered is what every timing must produce: one turn that started and
// ended once, ok, with the notice that says why the history is gone, no fatal,
// and a surviving launch that dropped --resume for the id Socrates owns.
func assertRecovered(t *testing.T, s *session, ghost, turnID string) {
	t.Helper()
	evs := s.waitTurn(turnID)

	if n := count(evs, harness.KindTurnStarted, turnID); n != 1 {
		t.Fatalf("%s got %d turn_started events - the replay opened a second turn:\n%s",
			turnID, n, dump(evs))
	}
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("the fallback did not save the turn: %q %q\n%s", end.Outcome, end.Error, dump(evs))
	}
	if n := count(evs, harness.KindFatal, ""); n != 0 {
		t.Fatalf("the expected death was reported as fatal:\n%s", dump(evs))
	}
	notices := of(evs, harness.KindNotice)
	if len(notices) != 1 || !strings.Contains(notices[0].Error, "could not be resumed") {
		t.Fatalf("wanted one notice about the lost history, got %v", notices)
	}
	if notices[0].TurnID != turnID {
		t.Fatalf("the notice was not attributed to the turn: %+v", notices[0])
	}
	if txt := only(t, evs, harness.KindText); txt.Text != "back again" {
		t.Fatalf("the replayed turn produced %q", txt.Text)
	}
	// The session id stays the one Socrates owns across the relaunch.
	for _, ev := range of(evs, harness.KindSessionID) {
		if ev.Session != ghost {
			t.Fatalf("the relaunch changed the session id to %q", ev.Session)
		}
	}
	// Two launches: the ghost that carried --resume, and the one relaunch,
	// which must start a fresh session under the id Socrates already owns
	// rather than trying --resume again.
	rows := s.argvLines()
	if len(rows) != 2 {
		t.Fatalf("wanted the ghost and exactly one relaunch, got %d: %v", len(rows), rows)
	}
	if !hasFlag(rows[0], "--resume", ghost) {
		t.Fatalf("the first launch did not resume %s: %v", ghost, rows[0])
	}
	if !hasFlag(rows[1], "--session-id", ghost) || hasFlag(rows[1], "--resume", "") {
		t.Fatalf("the relaunch did not start a fresh session under %s: %v", ghost, rows[1])
	}
}

// The kind timing: the ghost dies while Start is still watching, so the whole
// thing resolves before the host is ever handed the session.
func TestResumeFallbackWhenTheDeathBeatsStart(t *testing.T) {
	const ghost = "99999999-9999-4999-8999-999999999991"
	s := ghostSession(t, ghost, 3*time.Second)

	if n := count(s.events(), harness.KindFatal, ""); n != 0 {
		t.Fatalf("Start reported a fatal for the ghost:\n%s", dump(s.events()))
	}
	s.send("run_1", "hello")
	assertRecovered(t, s, ghost, "run_1")
}

// The timing BLOCKER-1 was about, and the one every production caller has:
// the host starts the adapter and the user types later, so the death lands
// between Start and Send with no turn open at all.
func TestResumeFallbackWhenTheDeathLandsBetweenStartAndSend(t *testing.T) {
	const ghost = "99999999-9999-4999-8999-999999999992"
	s := ghostSession(t, ghost, 20*time.Millisecond)

	// Let the ghost die and be replaced with nothing else going on.
	s.waitFor("the ghost to be replaced", func([]harness.Event) bool {
		b, err := os.ReadFile(s.argv)
		return err == nil && bytes.Count(b, []byte("\n")) == 2
	})
	if n := count(s.events(), harness.KindFatal, ""); n != 0 {
		t.Fatalf("the ghost death became a fatal:\n%s", dump(s.events()))
	}
	s.send("run_1", "hello")
	assertRecovered(t, s, ghost, "run_1")
}

// The third timing: the turn is already open when the ghost dies, so the
// replay has to carry that same turn id and must not open a second one.
func TestResumeFallbackWhenTheDeathLandsInsideTheTurn(t *testing.T) {
	const ghost = "99999999-9999-4999-8999-999999999993"
	s := ghostSession(t, ghost, 20*time.Millisecond)

	// Send at once: the ghost is still in its silent 200 ms, so this is the
	// turn the relaunch has to replay.
	s.send("run_1", "hello")
	assertRecovered(t, s, ghost, "run_1")
}

// The fallback is spent once. A second death is a real one: turn_finished with
// an error, a fatal, a closed stream - and a Send afterwards that says the
// session is gone instead of leaking a closed pipe (SHOULD-FIX-6).
func TestResumeFallbackIsUsedOnlyOnce(t *testing.T) {
	const ghost = "99999999-9999-4999-8999-999999999994"
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	// The relaunched process dies on its first turn, so the adapter meets a
	// second death with its one retry already spent.
	t.Setenv("FAKE_SCRIPT", `[{"do":"text","text":"back"},{"do":"die","code":1}]`)
	t.Setenv("FAKE_GHOST_RESUME", "1")

	work := t.TempDir()
	t.Setenv("FAKE_STATE_DIR", filepath.Join(work, "state"))
	argv := filepath.Join(work, "argv.jsonl")
	t.Setenv("FAKE_ARGV_FILE", argv)

	s := &session{t: t, argv: argv, mu: make(chan struct{}, 1), drain: make(chan struct{})}
	s.a = newAdapterWithGrace(testGrace, testGrace)
	go func() {
		defer close(s.drain)
		for ev := range s.a.Events() {
			s.mu <- struct{}{}
			s.got = append(s.got, ev)
			<-s.mu
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.a.Start(ctx, harness.Spec{
		Agent: "claude", Model: "sonnet", ChatID: "chat_ghost_twice",
		SessionID: ghost, Cwd: work, Dir: work,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.send("run_1", "hello")
	s.waitTurn("run_1")
	s.waitClosed()

	evs := s.events()
	// The fallback really did run: the history notice is on the turn.
	notices := of(evs, harness.KindNotice)
	if len(notices) != 1 || !strings.Contains(notices[0].Error, "could not be resumed") {
		t.Fatalf("the ghost path was not taken; notices were %v:\n%s", notices, dump(evs))
	}
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeError {
		t.Fatalf("the second death should end the turn with an error, got %q", end.Outcome)
	}
	if n := count(evs, harness.KindFatal, ""); n != 1 {
		t.Fatalf("wanted one fatal after the retry was spent, got %d:\n%s", n, dump(evs))
	}
	if err := s.a.Send(ctx, "run_2", "again"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("Send after the death said %v, wanted \"not running\"", err)
	}
}

// A resume that works is left alone: no relaunch, no notice, and the argv
// keeps --resume.
func TestAWorkingResumeIsNotSecondGuessed(t *testing.T) {
	const id = "99999999-9999-4999-8999-999999999995"
	dir := fakes.Build(t)
	t.Setenv("PATH", fakes.PathWith(dir))
	t.Setenv("FAKE_SCRIPT", `[{"do":"text","text":"still here"},{"do":"end","outcome":"ok"}]`)

	work := t.TempDir()
	argv := filepath.Join(work, "argv.jsonl")
	t.Setenv("FAKE_ARGV_FILE", argv)

	s := &session{t: t, argv: argv, mu: make(chan struct{}, 1), drain: make(chan struct{})}
	s.a = newAdapterWithGrace(testGrace, testGrace)
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
	if err := s.a.Start(ctx, harness.Spec{
		Agent: "claude", Model: "sonnet", ChatID: "chat_resume_ok",
		SessionID: id, Cwd: work, Dir: work,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.a.Close(closeCtx, 3*time.Second)
		<-s.drain
	})

	s.send("run_1", "hello")
	evs := s.waitTurn("run_1")
	if only(t, evs, harness.KindTurnFinished).Outcome != harness.OutcomeOK {
		t.Fatalf("a healthy resume did not end ok:\n%s", dump(evs))
	}
	if n := count(evs, harness.KindNotice, ""); n != 0 {
		t.Fatalf("a healthy resume produced %d notices:\n%s", n, dump(evs))
	}
	rows := s.argvLines()
	if len(rows) != 1 || !hasFlag(rows[0], "--resume", id) {
		t.Fatalf("wanted exactly one launch carrying --resume %s, got %v", id, rows)
	}
}
