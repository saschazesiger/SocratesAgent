//go:build linux

package agenthost

import (
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A message that waits behind a running turn can be taken back: the ack says
// it was, the journal has it withdrawn, and the agent never answers it. One
// the agent has taken up stays, and its ack says so; a repeat of the same
// withdraw is answered from the journal.
func TestHostWithdrawsAQueuedMessage(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/sleep 2")
	cl.ack("m1", 5*time.Second)
	cl.until(5*time.Second, isType(agent.TypeTurnStarted))
	cl.send("m2", "never read")
	cl.ack("m2", 5*time.Second)
	queued := cl.until(5*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeMessageState && ev.State == agent.MessageQueued
	})
	if err := cl.c.Send(Command{Op: OpWithdraw, ClientID: "w1", Message: queued.MessageID}); err != nil {
		t.Fatal(err)
	}
	if a := cl.ack("w1", 5*time.Second); !a.Withdrawn {
		t.Fatalf("the queued message was not taken back: %+v", a)
	}
	cl.until(5*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeMessageState && ev.MessageID == queued.MessageID && ev.State == agent.MessageWithdrawn
	})
	delete(cl.acks, "w1")
	if err := cl.c.Send(Command{Op: OpWithdraw, ClientID: "w1", Message: queued.MessageID}); err != nil {
		t.Fatal(err)
	}
	if a := cl.ack("w1", 5*time.Second); !a.Dup || !a.Withdrawn {
		t.Fatalf("the repeat: %+v", a)
	}
	cl.until(10*time.Second, isType(agent.TypeTurnEnded))
	// The turn that ran is the only one: the withdrawn message never got one.
	time.Sleep(300 * time.Millisecond)
	turns, first := 0, ""
	for _, ev := range r.journal() {
		if ev.Type == agent.TypeTurnStarted {
			turns++
			first = ev.MessageID
		}
		if ev.Type == agent.TypeMessageState && ev.MessageID == queued.MessageID && ev.State == agent.MessageDelivered {
			t.Fatal("the withdrawn message was delivered")
		}
	}
	if turns != 1 || first == queued.MessageID {
		t.Fatalf("%d turns, the last for %s", turns, first)
	}
	// The message the turn ran for is the agent's: it stays.
	if err := cl.c.Send(Command{Op: OpWithdraw, ClientID: "w2", Message: first}); err != nil {
		t.Fatal(err)
	}
	if a := cl.ack("w2", 5*time.Second); a.Withdrawn {
		t.Fatalf("a delivered message was taken back: %+v", a)
	}
	// A message the chat never had is refused.
	if err := cl.c.Send(Command{Op: OpWithdraw, ClientID: "w3", Message: "m_nope"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-cl.frames:
			if f.Fail != nil && f.Fail.ClientID == "w3" {
				if f.Fail.Code != CodeUnknownMessage {
					t.Fatalf("refused with %+v", f.Fail)
				}
				return
			}
			if f.Ack != nil && f.Ack.ClientID == "w3" {
				t.Fatalf("an unknown message was acknowledged: %+v", f.Ack)
			}
		case <-deadline:
			t.Fatal("no answer to the withdraw of an unknown message")
		}
	}
}
