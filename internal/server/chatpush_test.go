//go:build linux

package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// pushOfKind waits for the next push of a kind.
func pushOfKind(t *testing.T, sent <-chan sentPush, kind string, within time.Duration) pushNote {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case s := <-sent:
			if s.note.Kind == kind {
				return s.note
			}
		case <-deadline:
			t.Fatalf("no %s push", kind)
		}
	}
}

// A chat's push says what its journal says: the question it asks, at once,
// with the badge counting it; and how its turn ended, with a line of the
// answer, the badge and the tray letting the answered question go. A turn
// the person stopped themselves is pushed to nobody.
func TestAChatsPushesSayWhatItsJournalSays(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	p := c.srv.push
	sent := catch(p)
	if err := p.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	id := c.create(t)
	c.send(t, id, "m0", "hello")
	first := pushOfKind(t, sent, pushFinished, 10*time.Second)
	if !strings.HasPrefix(first.Body, "Finished · you said: hello") || first.ID != id {
		t.Fatalf("the first turn's push: %+v", first)
	}

	c.send(t, id, "m1", "/ask")
	asked := time.Now()
	q := pushOfKind(t, sent, pushQuestion, 10*time.Second)
	if q.Body != "Pick one" || q.Badge == nil || *q.Badge != 1 || len(q.Asking) != 1 || q.Asking[0] != id {
		t.Fatalf("the question's push: %+v", q)
	}
	if took := time.Since(asked); took > p.wait {
		t.Fatalf("the question waited %s for words it had", took)
	}
	if got := c.srv.askingSet(); len(got) != 1 || got[0] != id {
		t.Fatalf("asking %v", got)
	}

	ask := c.waitAsk(t, id)
	code, body := c.post(t, "/api/chats/"+id+"/asks/"+ask+"/answer", map[string]any{"client_id": "a1",
		"answer": map[string]any{"answers": map[string]any{"q1": map[string]any{"selected": []string{"a"}}}}, "device": "phone"})
	if code != http.StatusOK {
		t.Fatalf("answer: %d %v", code, body)
	}
	done := pushOfKind(t, sent, pushFinished, 10*time.Second)
	if !strings.HasPrefix(done.Body, "Finished · you answered:") || done.Badge == nil || *done.Badge != 0 || len(done.Asking) != 0 {
		t.Fatalf("the push after the answer: %+v", done)
	}

	c.send(t, id, "m2", "/limit")
	limited := pushOfKind(t, sent, pushFinished, 10*time.Second)
	if limited.Body != "Limit reached · The usage limit of this account is reached." {
		t.Fatalf("the limit's push: %+v", limited)
	}

	// Stopped by the person: unread, as any turn's end, and pushed to nobody.
	c.send(t, id, "m3", "/sleep 20")
	c.waitActivity(t, id, termux.StateBusy)
	if code, body := c.post(t, "/api/chats/"+id+"/interrupt", map[string]any{"client_id": "stop"}); code != http.StatusOK {
		t.Fatalf("interrupt: %d %v", code, body)
	}
	c.waitJournal(t, id, 10*time.Second, "the stopped turn", turnsEnded(4))
	c.waitActivity(t, id, termux.StateIdle)
	select {
	case s := <-sent:
		t.Fatalf("a turn the person stopped was pushed: %+v", s.note)
	case <-time.After(500 * time.Millisecond):
	}
	if b, _ := c.srv.chats.briefOfChat(id); b.Summary.LastOutcome != agent.OutcomeInterrupted {
		t.Fatalf("brief %+v", b.Summary)
	}
}

// The edges of a chat's activity as the badge and Home see them: a question
// left open by a turn that was stopped keeps the chat waiting and counted,
// through a park of its host too, until the person writes something else,
// which closes it.
func TestAQuestionLeftByAStoppedTurnKeepsTheChatWaiting(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/ask")
	c.waitAsk(t, id)
	c.waitActivity(t, id, termux.StateWaiting)
	if code, body := c.post(t, "/api/chats/"+id+"/interrupt", map[string]any{"client_id": "stop"}); code != http.StatusOK {
		t.Fatalf("interrupt: %d %v", code, body)
	}
	c.waitJournal(t, id, 10*time.Second, "the stopped turn", turnsEnded(1))
	if a := c.srv.manager.ActivityOf(id); a.State != termux.StateWaiting {
		t.Fatalf("after the stop: %+v", a)
	}
	if got := c.srv.askingSet(); len(got) != 1 || got[0] != id {
		t.Fatalf("asking after the stop: %v", got)
	}
	c.srv.chats.park(id)
	c.waitJournal(t, id, 10*time.Second, "the park", func(evs []agent.Event) bool {
		return count(evs, func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessParked }) > 0
	})
	if a := c.srv.manager.ActivityOf(id); a.State != termux.StateWaiting || len(c.srv.askingSet()) != 1 {
		t.Fatalf("after the park: %+v, asking %v", a, c.srv.askingSet())
	}
	if d := c.homeDigest(t, id); d["question"] == nil || d["summary"] != "Pick one" {
		t.Fatalf("Home after the park: %v", d)
	}
	c.send(t, id, "m2", "never mind, do this instead")
	c.waitJournal(t, id, 10*time.Second, "the next turn", turnsEnded(2))
	c.waitActivity(t, id, termux.StateIdle)
	if got := c.srv.askingSet(); len(got) != 0 {
		t.Fatalf("asking after moving on: %v", got)
	}
	if d := c.homeDigest(t, id); d["question"] != nil || !strings.HasPrefix(d["summary"].(string), "you said: never mind") {
		t.Fatalf("Home after moving on: %v", d)
	}
}
