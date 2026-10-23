//go:build linux

package agenthost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// An ask whose answer is meant to arrive as the next message (Codex code
// mode) is delivered that way, with no "no longer waited on" notice, and the
// agent's subagent parts come out nested.
func TestHostDeliversADeferredAnswerAsTheNextMessage(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/asknext")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	if err := cl.c.Send(Command{Op: OpAnswer, ClientID: "a1", AskID: ask.AskID, Answer: json.RawMessage(`"blue"`)}); err != nil {
		t.Fatal(err)
	}
	cl.until(5*time.Second, func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded && ev.Seq > ask.Seq+3 })
	journal := r.journal()
	p := agent.Fold(journal)
	if a := p.Ask(ask.AskID); a.Delivered != agent.ViaMessage || a.Deliver != agent.ViaMessage {
		t.Fatalf("ask %+v", a)
	}
	for _, ev := range journal {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeAnswerAsMessage {
			t.Fatal("a deferred answer got the lost-call notice")
		}
	}
	if got := lastReply(journal); !strings.Contains(got, "blue") {
		t.Fatalf("the agent got %q", got)
	}
	cl.send("m2", "/subagent")
	last := journal[len(journal)-1].Seq
	cl.until(5*time.Second, func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded && ev.Seq > last })
	p = agent.Fold(r.journal())
	var task *agent.Part
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if strings.HasSuffix(part.ID, "_task") {
				task = part
			}
		}
	}
	if task == nil || len(task.Children) != 2 || task.State != agent.PartDone {
		t.Fatalf("the subagent group %+v", task)
	}
}
