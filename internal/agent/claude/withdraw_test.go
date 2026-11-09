//go:build !windows

package claude

import (
	"context"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A message queued behind a running turn is taken back with
// cancel_async_message: the CLI's command_lifecycle cancelled for it is the
// withdrawal, not a drop, so neither a cancelled state nor an error is
// emitted, and it never gets a turn. The message the turn runs for, and one
// the CLI never had, are not taken back.
func TestWithdrawTakesAQueuedMessageOutOfTheCLIsQueue(t *testing.T) {
	e := newEnv(t)
	a, r := start(t, e, "")
	send(t, a, "m1", "/slow 1")
	r.waitFor(t, "the turn", func(evs []agent.Event) bool { return count(evs, agent.TypeTurnStarted) == 1 })
	send(t, a, "m2", "never read")
	r.waitFor(t, "m2 queued", func(evs []agent.Event) bool {
		for _, ev := range evs {
			if ev.Type == agent.TypeMessageState && ev.MessageID == "m2" && ev.State == agent.MessageQueued {
				return true
			}
		}
		return false
	})
	ctx := context.Background()
	if ok, err := a.Withdraw(ctx, "m2"); err != nil || !ok {
		t.Fatalf("withdraw m2 = %v, %v", ok, err)
	}
	if ok, err := a.Withdraw(ctx, "m1"); err != nil || ok {
		t.Fatalf("withdraw of the running message = %v, %v", ok, err)
	}
	if ok, err := a.Withdraw(ctx, "nope"); err != nil || ok {
		t.Fatalf("withdraw of an unknown message = %v, %v", ok, err)
	}
	send(t, a, "m3", "after")
	evs := r.turnsEnded(t, 2)
	for _, ev := range evs {
		if ev.MessageID != "m2" {
			continue
		}
		if ev.Type == agent.TypeError || (ev.Type == agent.TypeMessageState && ev.State != agent.MessageQueued) ||
			ev.Type == agent.TypeTurnStarted {
			t.Fatalf("the withdrawn message: %+v", ev)
		}
	}
	started := []string{}
	for _, ev := range evs {
		if ev.Type == agent.TypeTurnStarted {
			started = append(started, ev.MessageID)
		}
	}
	if len(started) != 2 || started[0] != "m1" || started[1] != "m3" {
		t.Fatalf("turns started for %v", started)
	}
}
