//go:build linux

package agenthost

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// limitedOn journals, through h, a chat whose last turn the limit ended on
// account, with a message the person wrote while that turn ran: what the
// adapters hold after a limit (AGENT-HOST.md §5.2, §5.3).
func limitedOn(h *host, account string) {
	out := emitter{h}
	out.Emit(agent.Event{Type: agent.TypeTurnStarted, TurnID: "t_done", Account: account},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t_done", Outcome: agent.OutcomeCompleted})
	out.Emit(agent.Event{Type: agent.TypeTurnStarted, TurnID: "t_limited", Account: account})
	out.Emit(agent.Event{Type: agent.TypeMessage, MessageID: "m_during", ClientID: "c1", Role: agent.RoleUser,
		State: agent.MessagePending, Text: "written while the limited turn ran"})
	out.Emit(agent.Event{Type: agent.TypeTurnEnded, TurnID: "t_limited", Outcome: agent.OutcomeLimited})
}

// bootNow runs boot as start does, with the command lock taken for it.
func bootNow(h *host) {
	h.cmdMu.Lock()
	h.boot()
}

// checkResent fails unless an adapter was handed the message written during
// the limited turn, with its place before that turn's end, and nothing else.
func checkResent(t *testing.T, r *recording) {
	t.Helper()
	sent := recordings.sentTo(r)
	if len(sent) == 0 || sent[0].MessageID != "m_during" || sent[0].AfterTurn != "t_done" {
		t.Fatalf("the adapter on %s was handed %+v", r.spec.Account, sent)
	}
}

// A host that starts on the account its journal's last turn was limited on -
// the host before it died while the limit held a message, and a command that
// is not a message brought this one up - tells the adapter which turn holds
// (StartSpec.Held), so that what it hands over waits as before. On another
// account (the server moved the chat, or a switch waits in its outbox)
// nothing is held: the messages are meant to run there.
func TestABootOnTheLimitedAccountHolds(t *testing.T) {
	for _, c := range []struct{ account, held string }{{"acct-a", "t_limited"}, {"acct-b", ""}} {
		t.Run(c.account, func(t *testing.T) {
			recordings.reset()
			h := newInProcessHost(t, c.account)
			limitedOn(h, "acct-a")
			bootNow(h)
			started := recordings.all()
			if len(started) != 1 {
				t.Fatalf("%d adapters started", len(started))
			}
			if got := started[0].spec.Held; got != c.held {
				t.Fatalf("an adapter started on %s after a limit on acct-a holds %q, want %q", c.account, got, c.held)
			}
			checkResent(t, started[0])
		})
	}
	// A turn that ran since the limit says the account goes on: nothing is
	// held.
	recordings.reset()
	h := newInProcessHost(t, "acct-a")
	limitedOn(h, "acct-a")
	emitter{h}.Emit(agent.Event{Type: agent.TypeTurnStarted, TurnID: "t_after", Account: "acct-a"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t_after", Outcome: agent.OutcomeCompleted})
	bootNow(h)
	if got := recordings.all()[0].spec.Held; got != "" {
		t.Fatalf("an adapter started after a turn that ran since the limit holds %q", got)
	}
}

// A switch away from the account a limit stopped - here the automatic one,
// with Continue - starts the adapter on the other account without a hold, so
// that what waited runs there, followed by the continue. A switch whose
// account cannot start the agent puts the chat back on the limited account,
// and the adapter restarted there holds again: what resendOwed hands it waits
// instead of running into the same limit.
func TestASwitchHoldsOnlyOnTheLimitedAccount(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH")}
	cmd := Command{Op: OpSwitchAccount, ClientID: "auto-t_limited", Account: "acct-b", When: WhenNow, Continue: true,
		Reason: "The limit of acct-a was reached."}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switchNow := func(h *host) error {
		h.cmdMu.Lock()
		defer h.cmdMu.Unlock()
		return h.doSwitch(ctx, cmd, env)
	}

	t.Run("made", func(t *testing.T) {
		recordings.reset()
		h := newInProcessHost(t, "acct-a")
		limitedOn(h, "acct-a")
		bootNow(h)
		if err := switchNow(h); err != nil {
			t.Fatal(err)
		}
		started := recordings.all()
		if len(started) != 2 || started[1].spec.Account != "acct-b" {
			t.Fatalf("started %d adapters", len(started))
		}
		if got := started[1].spec.Held; got != "" {
			t.Fatalf("the adapter on the other account holds %q", got)
		}
		checkResent(t, started[1])
		if sent := recordings.sentTo(started[1]); len(sent) != 2 || sent[1].Text != ContinueText {
			t.Fatalf("the adapter on the other account was handed %+v", sent)
		}
	})

	t.Run("failed", func(t *testing.T) {
		recordings.reset("acct-b")
		h := newInProcessHost(t, "acct-a")
		limitedOn(h, "acct-a")
		bootNow(h)
		if err := switchNow(h); err == nil {
			t.Fatal("a switch to an account that cannot start the agent was made")
		}
		started := recordings.all()
		if len(started) != 2 || started[1].spec.Account != "acct-a" {
			t.Fatalf("started %d adapters", len(started))
		}
		if got := started[1].spec.Held; got != "t_limited" {
			t.Fatalf("the adapter restarted on the limited account holds %q, want t_limited", got)
		}
		checkResent(t, started[1])
		if sent := recordings.sentTo(started[1]); len(sent) != 1 {
			t.Fatalf("the adapter restarted on the limited account was handed %+v", sent)
		}
		if got := h.currentAccount(); got != "acct-a" {
			t.Fatalf("the host runs on %s", got)
		}
	})
}
