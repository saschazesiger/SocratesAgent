//go:build linux

package agenthost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// sendLog is an adapter that keeps what it is sent.
type sendLog struct{ sent []agent.Outgoing }

func (a *sendLog) Start(context.Context, agent.StartSpec, agent.Emitter) error { return nil }
func (a *sendLog) Send(_ context.Context, msg agent.Outgoing) error {
	a.sent = append(a.sent, msg)
	return nil
}
func (a *sendLog) Interrupt(context.Context) error                       { return nil }
func (a *sendLog) Answer(context.Context, string, json.RawMessage) error { return agent.ErrAskGone }
func (a *sendLog) SetModel(context.Context, string, string) error        { return nil }
func (a *sendLog) Command(context.Context, string, string) error         { return nil }
func (a *sendLog) Park(context.Context) error                            { return nil }
func (a *sendLog) Stop() error                                           { return nil }
func (a *sendLog) Background() int                                       { return 0 }
func (a *sendLog) PID() int                                              { return 0 }

// A message goes to the adapter with its place in the journal: the turn that
// had ended last when it was written. A turn the limit ends between the
// message's journal write and its Send does not move it - the window in which
// the adapter used to take a message the person sent during the limited turn
// for one sent after it, let its hold go, and ran the message into the same
// limit on the same account (AGENT-HOST.md §5.3).
func TestAMessageGoesToTheAdapterWithItsPlaceInTheJournal(t *testing.T) {
	journal, events, err := agent.OpenJournal(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rec := agent.NewRecorder(journal, events, 0)
	t.Cleanup(func() { rec.Close() })
	ad := &sendLog{}
	h := &host{rec: rec, adapter: ad, conns: map[*hostConn]struct{}{}, tools: map[string]*hostConn{}, done: make(chan struct{})}
	out := emitter{h}
	ctx := context.Background()
	t.Cleanup(func() { journaledHook = nil })

	out.Emit(agent.Event{Type: agent.TypeTurnStarted, TurnID: "t_done"},
		agent.Event{Type: agent.TypeTurnEnded, TurnID: "t_done", Outcome: agent.OutcomeCompleted})
	out.Emit(agent.Event{Type: agent.TypeTurnStarted, TurnID: "t_limited"})
	// The person writes while t_limited runs, and the limit ends it before
	// the adapter is handed the message.
	journaledHook = func() {
		out.Emit(agent.Event{Type: agent.TypeTurnEnded, TurnID: "t_limited", Outcome: agent.OutcomeLimited})
	}
	if ack, ok := h.send(ctx, Command{Op: OpSend, ClientID: "c1", Text: "during"}).(Ack); !ok || ack.Seq == 0 {
		t.Fatalf("the first send was not acked: %+v", ack)
	}
	journaledHook = nil
	if _, ok := h.send(ctx, Command{Op: OpSend, ClientID: "c2", Text: "after"}).(Ack); !ok {
		t.Fatal("the second send was not acked")
	}

	if len(ad.sent) != 2 {
		t.Fatalf("the adapter was sent %+v", ad.sent)
	}
	if got := ad.sent[0].AfterTurn; got != "t_done" {
		t.Fatalf("the message written while the limited turn ran went after %q", got)
	}
	if got := ad.sent[1].AfterTurn; got != "t_limited" {
		t.Fatalf("the message written after the limited turn went after %q", got)
	}
	// The journal says the same, and so does what a new host hands over.
	var order []string
	h.rec.View(func(p *agent.Projection) {
		for _, m := range p.Undelivered() {
			order = append(order, outgoing(m).AfterTurn)
		}
	})
	if len(order) != 2 || order[0] != "t_done" || order[1] != "t_limited" {
		t.Fatalf("the journal has them after %q", order)
	}
	var journaled []string
	for _, ev := range h.rec.Feed().After(0) {
		switch ev.Type {
		case agent.TypeMessage:
			journaled = append(journaled, ev.Text)
		case agent.TypeTurnEnded:
			journaled = append(journaled, ev.TurnID)
		}
	}
	if got := strings.Join(journaled, " "); got != "t_done during t_limited after" {
		t.Fatalf("the journal's order: %s", got)
	}
}

// A host of a build before NoteProtocol would drop a send's note: the server
// puts it into the text for one, and leaves everything else as it is.
func TestFoldNote(t *testing.T) {
	cmd := Command{Op: OpSend, ClientID: "c", Text: "go on", Note: "From “API”.", Sender: &agent.Sender{Chat: "x"}}
	if got := FoldNote(cmd, NoteProtocol); got.Text != "go on" || got.Note != cmd.Note {
		t.Fatalf("a host that knows notes: %+v", got)
	}
	got := FoldNote(cmd, AskProtocol)
	if got.Note != "" || got.Text != agent.AgentText(cmd.Note, "go on") {
		t.Fatalf("an older host: %+v", got)
	}
	if got := FoldNote(Command{Op: OpCommand, Text: "compact", Note: "n"}, 1); got.Text != "compact" {
		t.Fatalf("not a send: %+v", got)
	}
}
