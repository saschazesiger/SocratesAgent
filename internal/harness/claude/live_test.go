package claude

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// The live tests run the real CLI. They are never part of CI: they cost money
// and need a logged-in account.
//
//	SOCRATES_LIVE_AGENTS=1 go test -run Live ./internal/harness/claude/
//
// They exist for one reason (F-4): of the flags in Argv, only
// --replay-user-messages was live-tested by the research. --name,
// --setting-sources and --effort are help-text only, and a rejected flag
// kills the process at start - which would kill every chat. So these tests
// run the exact production argv, not a reduced one.
const liveEnv = "SOCRATES_LIVE_AGENTS"

// liveModel is the cheapest model, and liveEffort exercises the --effort flag
// that no hermetic test can validate.
const (
	liveModel  = "haiku"
	liveEffort = "low"
)

func liveOrSkip(t *testing.T) {
	t.Helper()
	if os.Getenv(liveEnv) != "1" {
		t.Skip("set " + liveEnv + "=1 to run the live tests")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not on PATH")
	}
	// The fakes are on PATH in every other test of this package; the live
	// tests must not accidentally pick one up.
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if strings.Contains(dir, "socrates-fakes-") {
			t.Fatal("a fake claude is on PATH; the live test needs the real CLI")
		}
	}
}

// liveSession is the same collector as the hermetic tests use, without the
// fake-binary plumbing.
func liveSession(t *testing.T, spec harness.Spec) *session {
	t.Helper()
	spec.Agent = "claude"
	if spec.Cwd == "" {
		spec.Cwd = t.TempDir()
	}
	if spec.Dir == "" {
		spec.Dir = spec.Cwd
	}
	s := &session{t: t, mu: make(chan struct{}, 1), drain: make(chan struct{}), patience: 5 * time.Minute}
	s.a = New()
	go func() {
		defer close(s.drain)
		for ev := range s.a.Events() {
			s.mu <- struct{}{}
			s.got = append(s.got, ev)
			<-s.mu
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.a.Start(ctx, spec); err != nil {
		t.Fatalf("starting the real CLI with the production argv: %v", err)
	}
	return s
}

func (s *session) closeLive() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.a.Close(ctx, 10*time.Second)
	<-s.drain
}

// text returns everything the agent said in one turn.
func said(evs []harness.Event, turnID string) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Kind == harness.KindText && ev.TurnID == turnID {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

// TestLiveClaudeTurn is the one that makes WP2 done: the exact production
// argv against the real CLI, two turns in one process, and a resumed third
// turn in a second process that must still remember the first.
func TestLiveClaudeTurn(t *testing.T) {
	liveOrSkip(t)

	cwd := t.TempDir()
	spec := harness.Spec{
		Model: liveModel, Effort: liveEffort, Cwd: cwd, Dir: cwd,
		ChatID: "chat_live", ChatTitle: "Socrates live test",
	}
	s := liveSession(t, spec)

	sess := only(t, s.waitFor("session_id", func(evs []harness.Event) bool {
		return count(evs, harness.KindSessionID, "") == 1
	}), harness.KindSessionID)
	if sess.Session == "" {
		t.Fatal("the CLI reported an empty session id")
	}
	t.Logf("session_id = %s", sess.Session)

	// Turn one: state a fact.
	s.send("run_1", "My favourite number is 42. Reply with exactly: OK")
	evs := s.waitTurn("run_1")
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("turn one ended %q: %q\n%s", end.Outcome, end.Error, dump(evs))
	}
	t.Logf("turn one said: %q", said(evs, "run_1"))

	// Turn two, same process: the answer must come back.
	s.send("run_2", "What number did I mention? Reply with just the number.")
	s.waitFor("turn two", func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, "run_2") == 1
	})
	evs = s.events()
	answer := said(evs, "run_2")
	t.Logf("turn two said: %q", answer)
	for _, ev := range of(evs, harness.KindTurnFinished) {
		if ev.TurnID == "run_2" && ev.Outcome != harness.OutcomeOK {
			t.Fatalf("turn two ended %q: %q", ev.Outcome, ev.Error)
		}
	}
	if !strings.Contains(answer, "42") {
		t.Fatalf("turn two did not remember the number: %q", answer)
	}

	// Usage is real, not a placeholder.
	for _, u := range of(evs, harness.KindUsage) {
		t.Logf("usage: %+v", *u.Usage)
	}
	if len(of(evs, harness.KindUsage)) != 2 {
		t.Fatalf("wanted one usage per turn, got %d", len(of(evs, harness.KindUsage)))
	}
	s.closeLive()

	// A second process resuming the same session must still remember.
	spec.SessionID = sess.Session
	s2 := liveSession(t, spec)
	defer s2.closeLive()
	resumed := only(t, s2.waitFor("session_id", func(evs []harness.Event) bool {
		return count(evs, harness.KindSessionID, "") == 1
	}), harness.KindSessionID)
	if resumed.Session != sess.Session {
		t.Fatalf("--resume gave back session %q, wanted %q", resumed.Session, sess.Session)
	}
	s2.send("run_3", "Once more: what number did I mention? Reply with just the number.")
	evs = s2.waitTurn("run_3")
	answer = said(evs, "run_3")
	t.Logf("resumed turn said: %q", answer)
	if !strings.Contains(answer, "42") {
		t.Fatalf("the resumed session lost the conversation: %q", answer)
	}
}

// TestLiveClaudeResumeFallback is R-3 against the real CLI: a --resume of a
// session that does not exist. The CLI accepts the flag, stays alive and
// silent, and only fails on the first turn - so the adapter relaunches under
// the same id and replays that turn, and the user gets an answer plus one
// notice instead of a dead chat.
func TestLiveClaudeResumeFallback(t *testing.T) {
	liveOrSkip(t)

	cwd := t.TempDir()
	// A well-formed uuid the CLI has never seen.
	const ghost = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	s := liveSession(t, harness.Spec{
		Model: liveModel, Effort: liveEffort, Cwd: cwd, Dir: cwd,
		ChatID: "chat_live_resume", ChatTitle: "Socrates live resume", SessionID: ghost,
	})
	defer s.closeLive()

	s.send("run_1", "Reply with exactly: OK")
	evs := s.waitTurn("run_1")
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("the fallback did not save the turn: %q %q\n%s", end.Outcome, end.Error, dump(evs))
	}
	notices := of(evs, harness.KindNotice)
	if len(notices) == 0 || !strings.Contains(notices[0].Error, "could not be resumed") {
		t.Fatalf("wanted a notice about the lost history, got %v", notices)
	}
	t.Logf("notice: %s", notices[0].Error)
	t.Logf("the replayed turn said: %q", said(evs, "run_1"))
	if n := count(evs, harness.KindTurnStarted, "run_1"); n != 1 {
		t.Fatalf("the replay produced %d turn_started events", n)
	}
	if n := count(evs, harness.KindFatal, ""); n != 0 {
		t.Fatalf("the expected death was reported as fatal")
	}
}

// TestLiveClaudeInterrupt cancels a turn that is running a slow command, and
// asserts the user's own cancel comes back as "interrupted" rather than as a
// failure (F-3).
func TestLiveClaudeInterrupt(t *testing.T) {
	liveOrSkip(t)

	cwd := t.TempDir()
	s := liveSession(t, harness.Spec{
		Model: liveModel, Effort: liveEffort, Cwd: cwd, Dir: cwd,
		ChatID: "chat_live_interrupt", ChatTitle: "Socrates live interrupt",
	})
	defer s.closeLive()
	s.waitFor("session_id", func(evs []harness.Event) bool {
		return count(evs, harness.KindSessionID, "") == 1
	})

	// A literal `sleep 30` is blocked by a built-in Bash guardrail (research
	// Gotchas), so the slow command is a python one.
	s.send("run_1", `Run exactly this bash command and nothing else, then tell me it finished: python3 -c "import time; time.sleep(60)"`)
	s.waitFor("the tool call to start", func(evs []harness.Event) bool {
		return count(evs, harness.KindToolStarted, "run_1") >= 1
	})
	t.Log("tool started; interrupting")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.a.Interrupt(ctx); err != nil {
		t.Fatalf("interrupting: %v", err)
	}
	evs := s.waitTurn("run_1")
	end := only(t, evs, harness.KindTurnFinished)
	if end.Outcome != harness.OutcomeInterrupted {
		t.Fatalf("wanted outcome interrupted, got %q with error %q\n%s", end.Outcome, end.Error, dump(evs))
	}
	if end.Error != "" {
		t.Fatalf("an interrupted turn carried an error: %q", end.Error)
	}
	t.Log("the turn ended interrupted")

	// The process survives an interrupt and serves the next turn.
	s.send("run_2", "Reply with exactly: OK")
	s.waitFor("the turn after the interrupt", func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, "run_2") == 1
	})
	for _, ev := range of(s.events(), harness.KindTurnFinished) {
		if ev.TurnID == "run_2" && ev.Outcome != harness.OutcomeOK {
			t.Fatalf("the turn after the interrupt ended %q: %q", ev.Outcome, ev.Error)
		}
	}
	t.Logf("the turn after the interrupt said: %q", said(s.events(), "run_2"))
}
