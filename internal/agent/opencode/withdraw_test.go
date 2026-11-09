//go:build linux

package opencode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A message waiting in the queue behind a running turn can be withdrawn and
// is never posted to OpenCode; the message whose turn runs cannot.
func TestWithdrawTakesAQueuedMessageBackButNotTheRunningOne(t *testing.T) {
	r := newRig(t)
	r.mustStart("")
	ctx := context.Background()
	first := r.send("/sleep 0.5")
	dropped := r.send("never mind this")
	kept := r.send("keep this")
	eventually(t, 5*time.Second, "queued", func() bool {
		return r.rec.count(agent.TypeMessageState, func(ev agent.Event) bool { return ev.State == agent.MessageQueued }) == 2
	})
	if ok, err := r.a.Withdraw(ctx, first); err != nil || ok {
		t.Fatalf("withdraw of the running message: %v, %v", ok, err)
	}
	if ok, err := r.a.Withdraw(ctx, dropped); err != nil || !ok {
		t.Fatalf("withdraw of a queued message: %v, %v", ok, err)
	}
	if ok, err := r.a.Withdraw(ctx, dropped); err != nil || ok {
		t.Fatalf("second withdraw: %v, %v", ok, err)
	}
	r.waitTurns(2)
	time.Sleep(500 * time.Millisecond)
	order := strings.Join(r.rec.turnOrder(), " ")
	if strings.Contains(order, dropped) || !strings.Contains(order, "start "+kept) || len(r.rec.turns()) != 2 {
		t.Fatalf("turns %q", order)
	}
	if strings.Contains(r.conversation(), "never mind this (turn") {
		t.Fatalf("the withdrawn message was answered:\n%s", r.conversation())
	}
}
