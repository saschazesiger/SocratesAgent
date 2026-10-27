//go:build linux

package agenthost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// launchWatched launches the rig's host with the watch's clock w.
func (r *rig) launchWatched(w WatchSpec) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := r.l.Launch(ctx, Spec{ChatID: r.chat, Harness: agent.FakeHarness, Workdir: r.data,
		ParkAfterMS: r.park.Milliseconds(), Watch: &w})
	if err != nil {
		r.t.Fatalf("launch: %v", err)
	}
}

// A turn that says nothing for long is said to be quiet, with when the
// agent was last heard from, and heard from again at its next word; the
// projection's running turn says so in between.
func TestAQuietTurnIsSaidAndHeardFromAgain(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launchWatched(WatchSpec{QuietAfterMS: 300})
	cl := r.dial(0)
	cl.send("m1", "/hang 1.5")
	cl.ack("m1", 5*time.Second)
	quiet := cl.until(5*time.Second, isType(agent.TypeQuiet))
	if quiet.Since == 0 || quiet.TurnID == "" {
		t.Fatalf("quiet event %+v: no last sign of life", quiet)
	}
	if p := agent.Fold(r.journal()); p.Turn == nil || p.Turn.QuietSince != quiet.Since || p.Summary().QuietSince != quiet.Since {
		t.Fatalf("the running turn does not say it is quiet: %+v", p.Turn)
	}
	again := cl.until(5*time.Second, isType(agent.TypeQuiet))
	if again.Since != 0 || again.TurnID != quiet.TurnID {
		t.Fatalf("heard from again: %+v", again)
	}
	end := cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	if end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("the turn ended %q", end.Outcome)
	}
	if got := lastReply(r.journal()); got != "woke up" {
		t.Fatalf("reply %q", got)
	}
}

// A turn that waits for the person's answer is not quiet: the agent is
// rightly silent until it is answered.
func TestATurnWaitingOnThePersonIsNotQuiet(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launchWatched(WatchSpec{QuietAfterMS: 200, QuietLimitMS: 400})
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	time.Sleep(time.Second)
	if err := cl.c.Send(Command{Op: OpAnswer, ClientID: "a1", AskID: ask.AskID, Answer: json.RawMessage(`"yes"`)}); err != nil {
		t.Fatal(err)
	}
	if end := cl.until(5*time.Second, isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("the turn ended %q", end.Outcome)
	}
	for _, ev := range r.journal() {
		if ev.Type == agent.TypeQuiet || ev.Type == agent.TypeNotice {
			t.Fatalf("a turn waiting on the person was watched: %+v", ev)
		}
	}
}

// A Stop the agent does not act on does not leave the chat busy: the CLI is
// ended, the turn closed with why, and a new one takes the conversation up -
// a message sent after it is answered.
func TestAStopTheAgentIgnoresStartsItAgain(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launchWatched(WatchSpec{StopWaitMS: 500})
	cl := r.dial(0)
	cl.send("m1", "/hang 60")
	cl.until(5*time.Second, isType(agent.TypeTurnStarted))
	if err := cl.c.Send(Command{Op: OpInterrupt, ClientID: "i1"}); err != nil {
		t.Fatal(err)
	}
	end := cl.until(10*time.Second, isType(agent.TypeTurnEnded))
	if end.Outcome != agent.OutcomeInterrupted || !strings.Contains(end.Text, "did not stop") {
		t.Fatalf("the turn ended %q: %q", end.Outcome, end.Text)
	}
	cl.until(5*time.Second, isNotice(agent.NoticeUnresponsive))
	cl.until(5*time.Second, isProcess(agent.ProcessReady))
	cl.send("m2", "hello")
	cl.ack("m2", 5*time.Second)
	if end := cl.until(10*time.Second, isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("the next turn ended %q", end.Outcome)
	}
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: hello") {
		t.Fatalf("reply %q", got)
	}
	if p := agent.Fold(r.journal()); p.LastTurn == nil || p.LastTurn.Outcome != agent.OutcomeCompleted || p.Turn != nil {
		t.Fatalf("the chat is not idle: turn %+v, last %+v", p.Turn, p.LastTurn)
	}
}

// A turn quiet beyond the agent's limit is stopped by the watch, and the
// agent started again when it does not act on that either.
func TestATurnQuietBeyondTheLimitIsStopped(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launchWatched(WatchSpec{QuietAfterMS: 200, QuietLimitMS: 600, StopWaitMS: 400})
	cl := r.dial(0)
	cl.send("m1", "/hang 60")
	cl.until(5*time.Second, isType(agent.TypeQuiet))
	said := cl.until(5*time.Second, isNotice(agent.NoticeUnresponsive))
	if !strings.Contains(said.Text, "no sign of life") {
		t.Fatalf("notice %q", said.Text)
	}
	end := cl.until(10*time.Second, isType(agent.TypeTurnEnded))
	if end.Outcome != agent.OutcomeInterrupted {
		t.Fatalf("the turn ended %q", end.Outcome)
	}
	cl.until(5*time.Second, isProcess(agent.ProcessReady))
	if p := agent.Fold(r.journal()); p.Turn != nil || p.LastTurn.Stopped {
		t.Fatalf("turn %+v, last %+v: the watch's stop is nobody's", p.Turn, p.LastTurn)
	}
}
