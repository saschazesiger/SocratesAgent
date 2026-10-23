//go:build linux

package agenthost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A chat's scopes are its own unit and the <unit>-<unix seconds> a refused
// name made - never another chat whose id starts the same way.
func TestOwnScopeMatchesOnlyTheChatsScopes(t *testing.T) {
	unit := "socrates-agents-ab"
	for name, want := range map[string]bool{
		unit + ".scope":            true,
		unit + "-1792761513.scope": true,
		unit + "-cd.scope":         false,
		unit + "-12-cd.scope":      false,
		unit + "-.scope":           false,
		unit + "c.scope":           false,
		unit + "-123.service":      false,
	} {
		if got := ownScope(unit, name); got != want {
			t.Errorf("ownScope(%q) = %v, want %v", name, got, want)
		}
	}
}

// A CLI that does not know the conversation to resume gets a fresh one, and
// the chat says so, instead of a host that fails on every start.
func TestHostStartsAFreshConversationWhenTheOldOneIsGone(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "one")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	old := agent.Fold(r.journal()).ConversationID
	if err := cl.c.Send(Command{Op: OpPark}); err != nil {
		t.Fatal(err)
	}
	cl.until(5*time.Second, isProcess(agent.ProcessParked))
	r.waitDead(5 * time.Second)
	if err := os.Remove(filepath.Join(ChatDir(r.data, r.chat), stateDir, "fake", old+".json")); err != nil {
		t.Fatal(err)
	}
	since := r.journal()[len(r.journal())-1].Seq
	r.launch()
	next := r.dial(since)
	next.until(5*time.Second, isProcess(agent.ProcessReady))
	next.send("m2", "two")
	next.until(5*time.Second, isType(agent.TypeTurnEnded))
	journal := r.journal()
	p := agent.Fold(journal)
	if p.ConversationID == old || p.ConversationID == "" {
		t.Fatalf("the conversation is still %s", p.ConversationID)
	}
	lost := 0
	for _, ev := range journal {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeConversationLost {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("%d conversation-lost notices", lost)
	}
	if got := lastReply(journal); !strings.Contains(got, "(turn 1 of "+p.ConversationID) {
		t.Fatalf("the new conversation said %q", got)
	}
}
