//go:build !windows

package codex

import (
	"context"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A message held behind a compaction, which cannot be steered, can be
// withdrawn and then never gets a turn; one steered into a running turn is
// codex's already: its queued state says so (ViaTurn) and Withdraw refuses it.
func TestWithdrawTakesAHeldMessageBackButNotASteeredOne(t *testing.T) {
	lab := newFakeLab(t)
	r := newRecorder()
	a := lab.start(lab.spec("a", ""), r)
	ctx := context.Background()

	n := len(r.all())
	if err := a.Command(ctx, "compact", ""); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, n, "the compaction turn", isType(agent.TypeTurnStarted), wait)
	held, _ := send(t, a, r, "never mind this")
	if ok, err := a.Withdraw(ctx, held.MessageID); err != nil || !ok {
		t.Fatalf("withdraw of a held message: %v, %v", ok, err)
	}
	if ok, err := a.Withdraw(ctx, held.MessageID); err != nil || ok {
		t.Fatalf("second withdraw: %v, %v", ok, err)
	}
	r.turnEnd(t, n, wait)
	time.Sleep(500 * time.Millisecond)
	for _, ev := range r.since(n) {
		if ev.Type == agent.TypeTurnStarted && ev.MessageID == held.MessageID {
			t.Fatalf("the withdrawn message got a turn\n%s", dump(r.since(n)))
		}
	}

	_, n = send(t, a, r, "/sleep 0.5")
	steer, _ := send(t, a, r, "and this")
	queued := r.waitFor(t, n, "the steered message queued", func(ev agent.Event) bool {
		return ev.Type == agent.TypeMessageState && ev.MessageID == steer.MessageID && ev.State == agent.MessageQueued
	}, wait)
	if queued.Via != agent.ViaTurn {
		t.Fatalf("queued via %q, want %q", queued.Via, agent.ViaTurn)
	}
	if ok, err := a.Withdraw(ctx, steer.MessageID); err != nil || ok {
		t.Fatalf("withdraw of a steered message: %v, %v", ok, err)
	}
	r.turnEnd(t, n, wait)
}
