//go:build linux

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// setStartAt asks for a chat's scheduled start the way the page does.
func (c *chatEnv) setStartAt(t *testing.T, id string, at int64) (int, map[string]any) {
	t.Helper()
	res, body := c.do(t, c.client, "PUT", "/api/chats/"+id+"/start_at", fmt.Sprintf(`{"start_at":%d}`, at))
	return res.StatusCode, body
}

// noticeBefore is whether the journal has a notice with this code right
// before a user message with this text.
func noticeBefore(events []agent.Event, code, text string) bool {
	for i := 1; i < len(events); i++ {
		ev := events[i]
		if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser && ev.Text == text {
			prev := events[i-1]
			return prev.Type == agent.TypeNotice && prev.Code == code && prev.Text != ""
		}
	}
	return false
}

// A chat nobody has written to yet sends its draft by itself at the time it
// was given: once, as Send would, with a notice saying why, the field
// emptied and the schedule gone, and the phone told. Nothing happens before
// the time, and a chat that has a message cannot be scheduled.
func TestAScheduledStartSendsTheDraftOnce(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	id := c.create(t)
	if code, body := c.setStartAt(t, id, time.Now().Add(-time.Minute).UnixMilli()); code != http.StatusBadRequest {
		t.Fatalf("a time in the past: %d %v", code, body)
	}
	at := time.Now().Add(time.Hour).UnixMilli()
	code, body := c.setStartAt(t, id, at)
	if session, _ := body["session"].(map[string]any); code != http.StatusOK || session["start_at"] != float64(at) {
		t.Fatalf("set: %d %v", code, body)
	}
	if res, body := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/draft", `{"text":"the first words","base_rev":0}`); res.StatusCode != http.StatusOK {
		t.Fatalf("draft: %d %v", res.StatusCode, body)
	}

	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at-1000)); n != 0 {
		t.Fatalf("%d things were done before the time", n)
	}
	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at+1)); n != 1 {
		t.Fatalf("at the time, %d things were done", n)
	}
	started := pushOfKind(t, sent, pushStarted, 10*time.Second)
	if !strings.HasPrefix(started.Title, "Started: ") || started.ID != id {
		t.Fatalf("the push: %+v", started)
	}
	events := c.waitJournal(t, id, 20*time.Second, "the first turn", turnsEnded(1))
	users := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser })
	if users != 1 || !strings.Contains(lastText(events), "you said: the first words") {
		t.Fatalf("%d user messages, the agent said %q", users, lastText(events))
	}
	if !noticeBefore(events, agent.NoticeScheduledStart, "the first words") {
		t.Fatal("no notice before the scheduled message")
	}
	if draft, err := c.store.GetDraft(id); err != nil || draft.Text != "" || draft.Rev != 2 {
		t.Fatalf("the draft after the start: %#v %v", draft, err)
	}
	if row, _ := c.store.GetSession(id); row.StartAt != 0 {
		t.Fatalf("the schedule after the start: %d", row.StartAt)
	}
	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at+60000)); n != 0 {
		t.Fatalf("started again: %d", n)
	}
	if code, body := c.setStartAt(t, id, time.Now().Add(time.Hour).UnixMilli()); code != http.StatusConflict {
		t.Fatalf("a chat with a message was scheduled: %d %v", code, body)
	}

	// With nothing written, the time comes and the start fails, once, and
	// says so.
	code, made := c.post(t, "/api/chats", map[string]any{"client_id": "empty", "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, made)
	}
	empty := made["session"].(map[string]any)["id"].(string)
	if code, body := c.setStartAt(t, empty, at); code != http.StatusOK {
		t.Fatalf("set: %d %v", code, body)
	}
	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at+1)); n != 1 {
		t.Fatalf("%d things were done", n)
	}
	failed := pushOfKind(t, sent, pushStarted, 10*time.Second)
	if !strings.HasPrefix(failed.Title, "Scheduled start failed: ") || failed.Body != "nothing was written" || failed.ID != empty {
		t.Fatalf("the failure's push: %+v", failed)
	}
	if row, _ := c.store.GetSession(empty); row.StartAt != 0 {
		t.Fatalf("a failed start kept its schedule: %d", row.StartAt)
	}
	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at+2)); n != 0 {
		t.Fatalf("a failed start was tried again: %d", n)
	}

	// Cleared, it is gone; and a chat somebody writes to loses its schedule.
	if code, _ := c.setStartAt(t, empty, at); code != http.StatusOK {
		t.Fatal("set again")
	}
	if code, body := c.setStartAt(t, empty, 0); code != http.StatusOK || body["session"].(map[string]any)["start_at"] != float64(0) {
		t.Fatalf("clear: %d %v", code, body)
	}
	if code, _ := c.setStartAt(t, empty, at); code != http.StatusOK {
		t.Fatal("set once more")
	}
	c.send(t, empty, "by-hand", "hello")
	if row, _ := c.store.GetSession(empty); row.StartAt != 0 {
		t.Fatalf("a chat somebody started kept its schedule: %d", row.StartAt)
	}
}

// A chat its account's limit stopped goes on by itself a minute after the
// limit renews: once, with a notice, the phone told - and the fact that it
// waits survives a restart of the server. With the option off it waits for
// the person.
func TestAChatGoesOnAfterItsLimitRenews(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	id := c.create(t)
	c.send(t, id, "m1", "/limit")
	c.waitJournal(t, id, 10*time.Second, "the limited turn", turnsEnded(1))
	var brief chatBrief
	waitForCond(t, 10*time.Second, "the server to have the limit", func() bool {
		brief, _ = c.srv.chats.briefOfChat(id)
		return brief.Limit != nil
	})
	if brief.Limit == nil || brief.Limit.RenewsAt < time.Now().Add(time.Hour).UnixMilli() {
		t.Fatalf("the limit: %+v", brief.Limit)
	}
	renews := brief.Limit.RenewsAt

	// A restart keeps it: the brief says it, and the journal would.
	c.restart(t, 0, nil)
	brief, _ = c.srv.chats.briefOfChat(id)
	if brief.Limit == nil || brief.Limit.RenewsAt != renews {
		t.Fatalf("after a restart the limit is %+v", brief.Limit)
	}
	sent = catch(c.srv.push)

	// Off, nothing happens; on again (the default), it is due.
	res, body := c.do(t, c.client, "PUT", "/api/chats/"+id+"/auto-continue", `{"on":false}`)
	if opts, _ := body["session"].(map[string]any)["options"].(map[string]any); res.StatusCode != http.StatusOK || opts["auto_continue"] != false {
		t.Fatalf("off: %d %v", res.StatusCode, body)
	}
	due := time.UnixMilli(renews).Add(autoContinueDelay)
	if n := c.srv.sched.runDue(context.Background(), due.Add(time.Hour)); n != 0 {
		t.Fatalf("with the option off, %d things were done", n)
	}
	res, body = c.do(t, c.client, "PUT", "/api/chats/"+id+"/auto-continue", `{"on":true}`)
	if opts, _ := body["session"].(map[string]any)["options"].(map[string]any); res.StatusCode != http.StatusOK || opts["auto_continue"] != nil {
		t.Fatalf("on: %d %v", res.StatusCode, body)
	}
	if next := c.srv.sched.nextDue(); next != due.UnixMilli() {
		t.Fatalf("next due %d, want %d", next, due.UnixMilli())
	}
	if n := c.srv.sched.runDue(context.Background(), due.Add(-time.Second)); n != 0 {
		t.Fatalf("before the time, %d things were done", n)
	}
	if n := c.srv.sched.runDue(context.Background(), due); n != 1 {
		t.Fatalf("at the time, %d things were done", n)
	}
	events := c.waitJournal(t, id, 20*time.Second, "the continued turn", turnsEnded(2))
	waitForCond(t, 10*time.Second, "the server to have the turn", func() bool {
		brief, _ := c.srv.chats.briefOfChat(id)
		return brief.Limit == nil
	})
	if !noticeBefore(events, agent.NoticeAutoContinued, agenthost.ContinueText) {
		t.Fatal("no notice before the continue")
	}
	if !strings.Contains(lastText(events), "you said: "+agenthost.ContinueText) {
		t.Fatalf("the agent said %q", lastText(events))
	}
	continued := pushOfKind(t, sent, pushStarted, 10*time.Second)
	if !strings.HasPrefix(continued.Title, "Continued: ") || continued.ID != id {
		t.Fatalf("the push: %+v", continued)
	}
	if brief, _ := c.srv.chats.briefOfChat(id); brief.Limit != nil {
		t.Fatalf("still at a limit: %+v", brief.Limit)
	}
	if n := c.srv.sched.runDue(context.Background(), due.Add(time.Hour)); n != 0 {
		t.Fatalf("went on twice: %d", n)
	}

	// The person writing after a limit is the person acting: no continue.
	c.send(t, id, "m2", "/limit")
	c.waitJournal(t, id, 10*time.Second, "the second limit", turnsEnded(3))
	c.send(t, id, "m3", "hello")
	c.waitJournal(t, id, 10*time.Second, "the person's turn", turnsEnded(4))
	waitForCond(t, 10*time.Second, "the limit to be gone", func() bool {
		brief, _ := c.srv.chats.briefOfChat(id)
		return brief.Limit == nil
	})
}

// limitOf: a limited last turn nobody acted on, on the account it ran on,
// with the time its used-up window renews.
func TestLimitOf(t *testing.T) {
	ended := time.Now().UnixMilli()
	renews := time.Now().Add(2 * time.Hour).Unix()
	base := []agent.Event{
		{Type: agent.TypeMessage, MessageID: "u1", Role: agent.RoleUser, Text: "go", At: ended - 1000},
		{Type: agent.TypeTurnStarted, TurnID: "t1", MessageID: "u1", Account: "a", At: ended - 900},
		{Type: agent.TypeRateLimit, Window: "weekly", Used: 0.3, ResetsAt: renews + 86400, At: ended - 10},
		{Type: agent.TypeRateLimit, Window: "five_hour", Used: 1, ResetsAt: renews, At: ended - 10},
		{Type: agent.TypeTurnEnded, TurnID: "t1", Outcome: agent.OutcomeLimited, At: ended},
	}
	fold := func(extra ...agent.Event) *chatLimit {
		p := agent.NewProjection()
		for i, ev := range append(append([]agent.Event{}, base...), extra...) {
			ev.Seq = int64(i + 1)
			p.Apply(ev)
		}
		return limitOf(p)
	}
	if l := fold(); l == nil || l.Turn != "t1" || l.RenewsAt != renews*1000 {
		t.Fatalf("the limit: %+v", l)
	}
	if l := fold(agent.Event{Type: agent.TypeMessage, MessageID: "u2", Role: agent.RoleUser, Text: "later", At: ended + 5}); l != nil {
		t.Fatalf("a message after the limit left %+v", l)
	}
	if l := fold(agent.Event{Type: agent.TypeAccountSwitched, To: "b", At: ended + 5}); l != nil {
		t.Fatalf("a switch to another account left %+v", l)
	}
	if l := fold(agent.Event{Type: agent.TypeRateLimit, Window: "five_hour", Used: 0.1, ResetsAt: renews, At: ended + 5}); l == nil || l.RenewsAt != 0 {
		t.Fatalf("with no window used up the renewal is unknown: %+v", l)
	}
}
