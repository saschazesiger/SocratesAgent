package opencode

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// The live test runs the real `opencode serve` against real credentials. It
// never runs in CI and never runs by accident: it costs money on a paid
// provider and needs a network.
//
//	SOCRATES_LIVE_AGENTS=1 go test ./internal/harness/opencode/ -run TestLive -v
//
// Two more variables shape it, both optional:
//
//	SOCRATES_LIVE_OPENCODE_MODEL  <provider>|<model>, e.g. opencode|big-pickle.
//	                              Default: the first model OpenCode reports as
//	                              connected on the free built-in provider.
//	SOCRATES_LIVE_OPENCODE_ENV    extra KEY=VALUE pairs, comma separated,
//	                              passed to the server. Point XDG_DATA_HOME and
//	                              XDG_CONFIG_HOME at a lab directory here to
//	                              keep a test run out of your own OpenCode
//	                              installation.
const (
	liveEnv      = "SOCRATES_LIVE_AGENTS"
	liveModelEnv = "SOCRATES_LIVE_OPENCODE_MODEL"
	liveExtraEnv = "SOCRATES_LIVE_OPENCODE_ENV"
)

func liveSpec(t *testing.T) harness.Spec {
	t.Helper()
	if os.Getenv(liveEnv) != "1" {
		t.Skip("set " + liveEnv + "=1 to run the live OpenCode test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode is not on PATH")
	}
	var env []string
	for _, kv := range strings.Split(os.Getenv(liveExtraEnv), ",") {
		if strings.Contains(kv, "=") {
			env = append(env, kv)
		}
	}
	return harness.Spec{
		Agent:  "opencode",
		Model:  os.Getenv(liveModelEnv),
		Cwd:    t.TempDir(),
		ChatID: "live",
		Env:    env,
	}
}

// liveRun collects an adapter's events the way the hermetic tests do.
type liveRun struct {
	t     *testing.T
	a     harness.Adapter
	mu    sync.Mutex
	evs   []harness.Event
	ended chan struct{}
}

func startLive(t *testing.T, spec harness.Spec) *liveRun {
	t.Helper()
	r := &liveRun{t: t, a: New(), ended: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := r.a.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Every server this test starts is stopped, whatever the test did.
		_ = r.a.Close(ctx, 5*time.Second)
		select {
		case <-r.ended:
		case <-time.After(20 * time.Second):
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

func (r *liveRun) events() []harness.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]harness.Event, len(r.evs))
	copy(out, r.evs)
	return out
}

func (r *liveRun) wait(what string, within time.Duration, want func([]harness.Event) bool) []harness.Event {
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
		time.Sleep(100 * time.Millisecond)
	}
}

func (r *liveRun) send(turnID, text string, within time.Duration) []harness.Event {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.a.Send(ctx, turnID, text); err != nil {
		r.t.Fatalf("Send: %v", err)
	}
	return r.wait("turn_finished of "+turnID, within, func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, turnID) == 1
	})
}

// TestLiveOpenCodeDiscover fills in the model the other live tests run on, and
// on its own proves the discovery path against a real server.
func TestLiveOpenCodeDiscover(t *testing.T) {
	liveSpec(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cat, err := Discover(ctx, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	t.Logf("opencode reports %d connected models", len(cat.Models))
	if len(cat.Models) == 0 {
		t.Skip("no provider is connected on this machine")
	}
	for i, m := range cat.Models {
		if i >= 5 {
			break
		}
		t.Logf("  %s (%s) efforts=%v", m.ID, m.Label, m.Efforts)
	}
	for _, m := range cat.Models {
		if !strings.Contains(m.ID, "|") {
			t.Errorf("model id %q is not <provider>|<model>", m.ID)
		}
	}
}

// TestLiveOpenCodeTwoTurnsAndAnInterrupt is the whole contract against the real
// server: a turn that answers, a turn that uses the bash tool with **zero**
// permission events (F-10, which is what proves OPENCODE_PERMISSION took), and
// a turn that is interrupted.
func TestLiveOpenCodeTwoTurnsAndAnInterrupt(t *testing.T) {
	spec := liveSpec(t)
	if spec.Model == "" {
		spec.Model = liveDefaultModel(t)
	}
	t.Logf("model: %s", spec.Model)
	r := startLive(t, spec)

	evs := r.wait("session_id", 30*time.Second, func(evs []harness.Event) bool {
		return len(of(evs, harness.KindSessionID)) == 1
	})
	session := first(t, evs, harness.KindSessionID).Session
	t.Logf("session: %s", session)

	// Turn one: plain text.
	evs = r.send("live_1", "Reply with exactly: OK", 3*time.Minute)
	checkInvariants(t, evs)
	if fin := first(t, evs, harness.KindTurnFinished); fin.Outcome != harness.OutcomeOK {
		t.Fatalf("turn one = %+v", fin)
	}
	if len(of(evs, harness.KindText)) == 0 {
		t.Errorf("turn one produced no text:\n%s", dump(evs))
	}
	t.Logf("turn 1: %d events, text=%q", len(evs), textOf(evs))

	// Turn two: the bash tool, unattended.
	before := len(r.events())
	evs = r.send("live_2", "Run the bash tool with the command: echo hello-from-socrates. Then reply with its output.", 5*time.Minute)
	checkInvariants(t, evs)
	turn2 := evs[before:]
	if fin := first(t, turn2, harness.KindTurnFinished); fin.Outcome != harness.OutcomeOK {
		t.Fatalf("turn two = %+v", fin)
	}
	if len(of(turn2, harness.KindToolStarted)) == 0 {
		t.Errorf("turn two used no tool; the F-10 check proves nothing:\n%s", dump(turn2))
	}
	// F-10: with OPENCODE_PERMISSION="allow" the auto-reply must never fire.
	for _, n := range of(turn2, harness.KindNotice) {
		if strings.Contains(n.Error, "permission") {
			t.Errorf("a permission event reached the transcript: %q", n.Error)
		}
	}
	t.Logf("turn 2: %d events, tools=%d, reasoning=%d, usage=%d",
		len(turn2), len(of(turn2, harness.KindToolStarted)),
		len(of(turn2, harness.KindReasoning)), len(of(turn2, harness.KindUsage)))
	for _, ev := range of(turn2, harness.KindReasoning) {
		t.Logf("  reasoning id=%q text=%q", ev.ID, truncateForLog(ev.Text))
	}
	for _, ev := range of(turn2, harness.KindToolFinished) {
		t.Logf("  tool %s ok=%v exit=%d output=%q", ev.Tool.Name, ev.Tool.OK, ev.Tool.ExitCode,
			truncateForLog(ev.Tool.Output))
	}

	// Turn three: interrupted.
	before = len(r.events())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.a.Send(ctx, "live_3", "Count slowly from 1 to 200, one number per line, and do not stop early."); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.wait("the interrupted turn to get going", 90*time.Second, func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnStarted, "live_3") == 1
	})
	time.Sleep(2 * time.Second)
	if err := r.a.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	evs = r.wait("turn_finished after the interrupt", 60*time.Second, func(evs []harness.Event) bool {
		return count(evs, harness.KindTurnFinished, "live_3") == 1
	})
	checkInvariants(t, evs)
	fin := first(t, evs[before:], harness.KindTurnFinished)
	if fin.Outcome != harness.OutcomeInterrupted {
		t.Errorf("turn three = %+v, want interrupted", fin)
	}
	t.Logf("turn 3: ended %s after the interrupt", fin.Outcome)

	// Deltas are the one thing that comes off the server-wide stream. If they
	// stop being published there this says so out loud rather than quietly
	// dropping the streaming half of the UI.
	if n := len(of(r.events(), harness.KindTextDelta)); n == 0 {
		t.Errorf("no text_delta in three turns: the increments are not reaching the adapter")
	} else {
		t.Logf("text_delta events across the run: %d", n)
	}
}

// TestLiveOpenCodeResume proves a second process picks the conversation up:
// same session id, new server, new password.
func TestLiveOpenCodeResume(t *testing.T) {
	spec := liveSpec(t)
	if spec.Model == "" {
		spec.Model = liveDefaultModel(t)
	}
	first1 := startLive(t, spec)
	evs := first1.wait("session_id", 30*time.Second, func(evs []harness.Event) bool {
		return len(of(evs, harness.KindSessionID)) == 1
	})
	session := first(t, evs, harness.KindSessionID).Session
	first1.send("live_1", "Remember the word bicycle. Reply with exactly: OK", 3*time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = first1.a.Close(ctx, 5*time.Second)

	resumed := spec
	resumed.SessionID = session
	second := startLive(t, resumed)
	evs = second.wait("session_id", 30*time.Second, func(evs []harness.Event) bool {
		return len(of(evs, harness.KindSessionID)) == 1
	})
	if got := first(t, evs, harness.KindSessionID).Session; got != session {
		t.Fatalf("resumed session id = %q, want %q", got, session)
	}
	evs = second.send("live_2", "Which word did I ask you to remember? Answer with the word alone.", 3*time.Minute)
	checkInvariants(t, evs)
	if !strings.Contains(strings.ToLower(textOf(evs)), "bicycle") {
		t.Errorf("the resumed session lost the conversation; it said %q", textOf(evs))
	}
	t.Logf("resumed answer: %q", textOf(evs))
}

// TestLiveOpenCodeAModelThatCannotRun is the silent-failure path, live. The
// server accepts the model switch, accepts the prompt, and then stops: nothing
// on either stream, no step.ended, the session leaves the active map. Only the
// backstop notices, and the turn must not be reported as a success.
func TestLiveOpenCodeAModelThatCannotRun(t *testing.T) {
	spec := liveSpec(t)
	spec.Model = "opencode|socrates-does-not-exist"
	r := startLive(t, spec)

	evs := r.send("live_bogus", "say hi", 60*time.Second)
	checkInvariants(t, evs)

	fin := first(t, evs, harness.KindTurnFinished)
	if fin.Outcome != harness.OutcomeError || fin.Error != "the agent produced no answer" {
		t.Errorf("turn_finished = %+v, want error \"the agent produced no answer\"", fin)
	}
	if len(of(evs, harness.KindText)) != 0 || len(of(evs, harness.KindToolStarted)) != 0 {
		t.Errorf("this turn was not supposed to produce anything:\n%s", dump(evs))
	}
	var backstop string
	for _, n := range of(evs, harness.KindNotice) {
		if strings.Contains(n.Error, "idle check") {
			backstop = n.Error
		}
	}
	if backstop == "" {
		t.Fatalf("the backstop did not say it closed the turn:\n%s", dump(evs))
	}
	t.Logf("notice: %s", backstop)
	// The server prints the reason on its own output even though nothing
	// reaches the wire; that line is the only "why" a user can be given.
	if !strings.Contains(backstop, "opencode said:") {
		t.Errorf("the notice does not carry the server's own error line")
	}
}

// liveDefaultModel picks something that is actually connected, preferring the
// free built-in provider so a test run does not cost anything.
func liveDefaultModel(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cat, err := Discover(ctx, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cat.Models) == 0 {
		t.Skip("no provider is connected on this machine")
	}
	for _, m := range cat.Models {
		if m.Group == "opencode" {
			return m.ID
		}
	}
	return cat.Models[0].ID
}

func textOf(evs []harness.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Kind == harness.KindText {
			b.WriteString(ev.Text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func truncateForLog(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
