//go:build linux

package agenthost

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// A journal that is lost while the server still knows the conversation: the
// next host resumes it and the history comes back from the CLI's transcript
// before anything new.
func TestALostJournalIsRebuiltFromTheConversation(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	conv := cl.until(5*time.Second, isType(agent.TypeConversation)).ConversationID
	cl.send("m1", "hello")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	r.kill()
	if err := os.Remove(JournalPath(r.data, r.chat)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.l.Launch(ctx, Spec{ChatID: r.chat, Harness: agent.FakeHarness, Workdir: r.data,
		Conversation: conv, ParkAfterMS: time.Hour.Milliseconds()}); err != nil {
		t.Fatal(err)
	}
	cl = r.dial(0)
	cl.until(5*time.Second, isProcess(agent.ProcessReady))
	cl.send("m2", "again")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	p := agent.Fold(r.journal())
	var users []string
	for _, m := range p.Messages {
		if m.Role == agent.RoleUser {
			users = append(users, m.Text)
		}
	}
	rebuilt := false
	for _, ev := range r.journal() {
		rebuilt = rebuilt || (ev.Type == agent.TypeNotice && ev.Code == agent.NoticeRebuilt)
	}
	if strings.Join(users, "|") != "hello|again" || !rebuilt || p.ConversationID != conv {
		t.Fatalf("users %q, rebuilt %v, conversation %q", users, rebuilt, p.ConversationID)
	}
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: again (turn 2") {
		t.Fatalf("reply %q", got)
	}
}
