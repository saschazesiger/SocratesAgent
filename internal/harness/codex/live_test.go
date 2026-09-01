package codex

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// The live tests talk to the real `codex` and cost real money, so they are
// never part of a normal run: they exist to prove the wire contract against
// the CLI that actually ships, which no fake can do.
//
//	SOCRATES_LIVE_AGENTS=1 go test -run TestLiveCodex ./internal/harness/codex/
func liveOrSkip(t *testing.T) string {
	t.Helper()
	if os.Getenv("SOCRATES_LIVE_AGENTS") != "1" {
		t.Skip("set SOCRATES_LIVE_AGENTS=1 to run the live agent tests")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not installed on this machine")
	}
	return path
}

// cheapestModel asks the real model/list for the model to spend money on.
// gpt-5.4-mini and gpt-5.6-luna are the two cheap ones the research found; if
// neither is offered any more, the account's default is used.
func cheapestModel(t *testing.T) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cat, err := Discover(ctx, "codex")
	if err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if len(cat.Models) == 0 {
		t.Fatalf("codex offered no models")
	}
	byID := map[string]harness.Model{}
	for _, m := range cat.Models {
		byID[m.ID] = m
	}
	for _, want := range []string{"gpt-5.4-mini", "gpt-5.6-luna"} {
		if m, ok := byID[want]; ok {
			return m.ID, effortFor(m)
		}
	}
	for _, m := range cat.Models {
		if m.Default {
			return m.ID, effortFor(m)
		}
	}
	return cat.Models[0].ID, effortFor(cat.Models[0])
}

func effortFor(m harness.Model) string {
	for _, e := range m.Efforts {
		if e == "low" {
			return "low"
		}
	}
	return m.DefaultEffort
}

// liveTurn sends one message and waits for the turn to end.
func liveTurn(t *testing.T, a *adapter, r *rec, turnID, text string) harness.Event {
	t.Helper()
	if err := a.Send(context.Background(), turnID, text); err != nil {
		t.Fatalf("Send %s: %v", turnID, err)
	}
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		for _, ev := range r.all() {
			if ev.Kind == harness.KindTurnFinished && ev.TurnID == turnID {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("turn %s never finished; got %s", turnID, kinds(r.all()))
	return harness.Event{}
}

func liveText(events []harness.Event, turnID string) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind == harness.KindText && ev.TurnID == turnID {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

// TestLiveCodexTwoTurnsAndResume runs two turns in one process and then a
// third in a second process resumed from the same thread id, which is the
// whole lifecycle the host depends on.
func TestLiveCodexTwoTurnsAndResume(t *testing.T) {
	liveOrSkip(t)
	model, effort := cheapestModel(t)
	t.Logf("live model %s effort %q", model, effort)

	cwd := t.TempDir()
	spec := harness.Spec{Agent: "codex", Model: model, Effort: effort, Cwd: cwd, ChatID: "live"}

	a := newAdapter()
	r := record(a)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := a.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sid := r.wait(t, harness.KindSessionID).Session
	if sid == "" {
		t.Fatalf("no thread id")
	}
	t.Logf("thread %s", sid)

	// F-5: turn/start carries the model and the effort. A rejected parameter
	// fails the RPC, so a turn that runs at all is a turn that was accepted
	// with both.
	end := liveTurn(t, a, r, "run_1", "Remember the number 42. Reply with exactly OK.")
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("first turn = %+v", end)
	}
	if got := liveText(r.all(), "run_1"); !strings.Contains(got, "OK") {
		t.Errorf("first turn said %q", got)
	}

	end = liveTurn(t, a, r, "run_2", "Reply with exactly SECOND.")
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("second turn = %+v", end)
	}
	if got := liveText(r.all(), "run_2"); !strings.Contains(got, "SECOND") {
		t.Errorf("second turn said %q", got)
	}
	for _, ev := range r.all() {
		if ev.Kind == harness.KindNotice && strings.Contains(ev.Error, "Model metadata") {
			t.Errorf("codex did not recognise the model: %s", ev.Error)
		}
	}
	usage := of(r.all(), harness.KindUsage)
	if len(usage) == 0 {
		t.Errorf("no usage was reported")
	} else {
		t.Logf("usage %+v", usage[len(usage)-1].Usage)
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = a.Close(closeCtx, 5*time.Second)
	closeCancel()
	r.waitClosed(t)
	checkInvariants(t, r.all())

	// A fresh process resuming the same thread must still remember the number.
	spec.SessionID = sid
	b := newAdapter()
	rb := record(b)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel2()
	if err := b.Start(ctx2, spec); err != nil {
		t.Fatalf("Start after resume: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = b.Close(c, 5*time.Second)
	})
	if got := rb.wait(t, harness.KindSessionID).Session; got != sid {
		t.Fatalf("resume reported thread %q, want %q", got, sid)
	}
	end = liveTurn(t, b, rb, "run_3", "What number did I ask you to remember? Reply with just the number.")
	if end.Outcome != harness.OutcomeOK {
		t.Fatalf("resumed turn = %+v", end)
	}
	got := liveText(rb.all(), "run_3")
	t.Logf("resumed turn said %q", got)
	if !strings.Contains(got, "42") {
		t.Errorf("the resumed thread did not remember the number: %q", got)
	}
	checkInvariants(t, rb.all())
}

// TestLiveCodexInterrupt cancels a turn that is in the middle of a long shell
// command, which is what the stop button does.
func TestLiveCodexInterrupt(t *testing.T) {
	liveOrSkip(t)
	model, effort := cheapestModel(t)

	a := newAdapter()
	r := record(a)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := a.Start(ctx, harness.Spec{
		Agent: "codex", Model: model, Effort: effort, Cwd: t.TempDir(), ChatID: "live-interrupt",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = a.Close(c, 5*time.Second)
	})
	r.wait(t, harness.KindSessionID)

	if err := a.Send(context.Background(), "run_1",
		"Run the shell command `for i in $(seq 1 60); do echo line-$i; sleep 1; done` and then say DONE."); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Interrupt once the command is actually running.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if len(of(r.all(), harness.KindToolStarted)) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	deadline = time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		for _, ev := range r.all() {
			if ev.Kind == harness.KindTurnFinished {
				if ev.Outcome != harness.OutcomeInterrupted {
					t.Fatalf("turn_finished = %+v, want interrupted", ev)
				}
				t.Logf("interrupted after %s", kinds(r.all()))
				checkInvariants(t, r.all())
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the interrupted turn never ended; got %s", kinds(r.all()))
}
