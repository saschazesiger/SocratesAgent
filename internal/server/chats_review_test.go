//go:build linux

package server

import (
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// Only a message or an answer waits in the outbox for the next host; an
// interrupt or a model change refused by a host that was going away would
// mean nothing to the next one.
func TestOnlySendsAndAnswersWaitForTheNextHost(t *testing.T) {
	for _, tc := range []struct {
		op, code string
		keep     bool
	}{
		{agenthost.OpSend, agenthost.CodeNotRunning, true},
		{agenthost.OpAnswer, agenthost.CodeStopping, true},
		{agenthost.OpInterrupt, agenthost.CodeStopping, false},
		{agenthost.OpSetModel, agenthost.CodeNotRunning, false},
		{agenthost.OpCommand, agenthost.CodeStopping, false},
		{agenthost.OpSend, agenthost.CodeBadRequest, false},
	} {
		if got := keepForNextHost(&agenthost.Fail{Op: tc.op, Code: tc.code}); got != tc.keep {
			t.Errorf("%s refused with %s: keep = %v, want %v", tc.op, tc.code, got, tc.keep)
		}
	}
}

// A connection that drops while the host is alive is taken up again, so what
// the host does next on its own - a background task ending - is heard.
func TestTheServerStaysWithALiveHost(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/bg 1")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	p, _ := c.srv.chats.peer(id)
	deadline := time.Now().Add(5 * time.Second)
	for p.summary().Tasks != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the background task never showed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.drop()
	deadline = time.Now().Add(5 * time.Second)
	for {
		journal := c.journal(t, id)
		if s := p.summary(); s.Tasks == 0 && s.LastSeq == journal[len(journal)-1].Seq {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server missed the end of the background task: %+v", p.summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The detector hears of it as soon as the event is applied, not in the
	// same instant the projection shows it.
	for a := c.srv.manager.ActivityOf(id); a.State != "idle"; a = c.srv.manager.ActivityOf(id) {
		if time.Now().After(deadline) {
			t.Fatalf("activity %+v", a)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := p.summary(); s.Activity != agent.ActivityIdle {
		t.Fatalf("summary %+v", s)
	}
}
