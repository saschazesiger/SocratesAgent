//go:build linux

package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// askPayload is a small ask_user call.
var askPayload = json.RawMessage(`{"title":"Setup","questions":[{"id":"db","question":"Which database?","kind":"single",` +
	`"options":[{"id":"pg","label":"PostgreSQL"},{"id":"sqlite","label":"SQLite"}]}]}`)

// seen is the first event so far that ok says so about, read already or yet
// to come.
func (cl *client) seen(timeout time.Duration, ok func(agent.Event) bool) agent.Event {
	cl.t.Helper()
	for _, ev := range cl.events {
		if ok(ev) {
			return ev
		}
	}
	return cl.until(timeout, ok)
}

func isNotice(code string) func(agent.Event) bool {
	return func(ev agent.Event) bool { return ev.Type == agent.TypeNotice && ev.Code == code }
}

func (cl *client) answer(clientID, askID, device string, answer json.RawMessage) Ack {
	cl.t.Helper()
	if err := cl.c.Send(Command{Op: OpAnswer, ClientID: clientID, AskID: askID, Answer: answer, Device: device}); err != nil {
		cl.t.Fatal(err)
	}
	return cl.ack(clientID, 5*time.Second)
}

// The first device's answer wins; the second is told who answered, and the
// first one's repeat is a duplicate.
func TestTwoDevicesAnswerOneAsk(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	other := r.dial(0)
	first := cl.answer("a1", ask.AskID, "phone", json.RawMessage(`{"answers":{"q1":{"selected":["b"]}}}`))
	second := other.answer("b1", ask.AskID, "desktop", json.RawMessage(`{"answers":{"q1":{"selected":["a"]}}}`))
	if first.Seq == 0 || first.AnsweredElsewhere {
		t.Fatalf("first %+v", first)
	}
	if !second.AnsweredElsewhere || second.ByDevice != "phone" {
		t.Fatalf("second %+v", second)
	}
	cl.seen(5*time.Second, isType(agent.TypeTurnEnded))
	delete(cl.acks, "a1")
	if again := cl.answer("a1", ask.AskID, "phone", json.RawMessage(`{}`)); !again.Dup || again.Seq != first.Seq {
		t.Fatalf("repeat %+v", again)
	}
	if got := lastReply(r.journal()); !strings.Contains(got, `"selected":["b"]`) {
		t.Fatalf("the agent got %q", got)
	}
}

// An ask left by an interrupted turn stays open, but does not keep the host
// from parking: its call is gone. Answered later, it reaches the agent of
// the next host as a message.
func TestAnAskOfAnInterruptedTurnLetsTheHostParkAndIsAnsweredAsAMessage(t *testing.T) {
	r := newRig(t, 300*time.Millisecond)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	if err := cl.c.Send(Command{Op: OpInterrupt, ClientID: "i1"}); err != nil {
		t.Fatal(err)
	}
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.until(5*time.Second, isProcess(agent.ProcessParked))
	r.waitDead(5 * time.Second)
	p := agent.Fold(r.journal())
	if len(p.OpenAsks()) != 1 || p.Summary().Activity != "waiting" {
		t.Fatalf("after the park: %d open asks, activity %s", len(p.OpenAsks()), p.Summary().Activity)
	}
	last := r.journal()[len(r.journal())-1].Seq
	r.park = time.Hour
	r.launch()
	next := r.dial(last)
	next.seen(5*time.Second, isProcess(agent.ProcessReady))
	next.answer("a1", ask.AskID, "phone", json.RawMessage(`{"answers":{"q1":{"selected":["a"]}}}`))
	next.seen(5*time.Second, isNotice(agent.NoticeAnswerAsMessage))
	next.seen(5*time.Second, isType(agent.TypeTurnEnded))
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: Answer to your earlier question (Fake question)") ||
		!strings.Contains(got, "A [a]") {
		t.Fatalf("the agent got %q", got)
	}
}

// A question survives the death of its host: recovery closes the turn but
// not the ask, and the answer reaches the next host's agent as a message.
func TestAnAskSurvivesTheDeathOfItsHost(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	if err := syscall.Kill(cl.c.Hello.HostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	r.waitDead(5 * time.Second)
	if err := r.l.Reap(context.Background(), r.chat); err != nil {
		t.Fatal(err)
	}
	r.launch()
	next := r.dial(0)
	next.seen(5*time.Second, isNotice(agent.NoticeResumed))
	next.seen(5*time.Second, isProcess(agent.ProcessReady))
	if p := agent.Fold(r.journal()); p.Ask(ask.AskID) == nil || p.Ask(ask.AskID).Answered || len(p.OpenAsks()) != 1 {
		t.Fatalf("the ask after recovery: %+v", p.Ask(ask.AskID))
	}
	next.answer("a1", ask.AskID, "desktop", json.RawMessage(`{"answers":{"q1":{"selected":["b"]}}}`))
	next.seen(5*time.Second, isNotice(agent.NoticeAnswerAsMessage))
	next.seen(5*time.Second, func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded && ev.Outcome == agent.OutcomeCompleted })
	if got := lastReply(r.journal()); !strings.Contains(got, "B [b]") {
		t.Fatalf("the agent got %q", got)
	}
}

// A local tool asks over the host protocol (version 2): the ask belongs to
// the running turn, its answer comes back on the tool's connection, and the
// host does not park while the tool waits.
func TestALocalToolAsksThroughTheHost(t *testing.T) {
	r := newRig(t, 200*time.Millisecond)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/sleep 30")
	turn := cl.until(5*time.Second, isType(agent.TypeTurnStarted))
	tool := r.dial(1 << 62)
	if err := tool.c.Send(Command{Op: OpAsk, Payload: askPayload}); err != nil {
		t.Fatal(err)
	}
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	if ask.TurnID != turn.TurnID || string(ask.Payload) != string(askPayload) {
		t.Fatalf("ask %+v", ask)
	}
	answer := json.RawMessage(`{"answers":{"db":{"selected":["pg"]}}}`)
	cl.answer("a1", ask.AskID, "phone", answer)
	f := tool.frame(5 * time.Second)
	for f.Answer == nil {
		if f.Ack != nil && f.Ack.AskID != ask.AskID {
			t.Fatalf("ack %+v", f.Ack)
		}
		f = tool.frame(5 * time.Second)
	}
	if f.Answer.AskID != ask.AskID || string(f.Answer.Answer) != string(answer) {
		t.Fatalf("answer frame %+v", f.Answer)
	}
	if ev := cl.seen(5*time.Second, isType(agent.TypeAskDelivered)); ev.Via != agent.ViaTool {
		t.Fatalf("delivered %+v", ev)
	}

	// Outside a turn, a waiting tool still keeps the host; once its
	// connection goes, the host parks, and the ask is answered as a message
	// through the next one.
	if err := cl.c.Send(Command{Op: OpInterrupt, ClientID: "i1"}); err != nil {
		t.Fatal(err)
	}
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	waiting := r.dial(1 << 62)
	if err := waiting.c.Send(Command{Op: OpAsk, Payload: askPayload}); err != nil {
		t.Fatal(err)
	}
	second := cl.until(5*time.Second, isType(agent.TypeAsk))
	time.Sleep(600 * time.Millisecond)
	if alive, _ := r.l.Alive(r.chat); !alive {
		t.Fatal("the host parked while a tool waited")
	}
	waiting.c.Close()
	cl.until(5*time.Second, isProcess(agent.ProcessParked))
	r.waitDead(5 * time.Second)
	r.park = time.Hour
	r.launch()
	next := r.dial(cl.events[len(cl.events)-1].Seq)
	next.answer("a2", second.AskID, "phone", answer)
	if ev := next.seen(5*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeAskDelivered && ev.AskID == second.AskID
	}); ev.Via != agent.ViaMessage {
		t.Fatalf("delivered %+v", ev)
	}
}

// The ask op needs protocol 2 and an ask_user payload.
func TestTheAskOpIsRefusedWithoutItsProtocolOrAPayload(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.until(5*time.Second, isProcess(agent.ProcessReady))
	tool := r.dial(1 << 62)
	if err := tool.c.Send(Command{Op: OpAsk, Payload: json.RawMessage(`{"questions":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if f := tool.frame(5 * time.Second); f.Fail == nil || f.Fail.Code != CodeBadRequest {
		t.Fatalf("an empty payload: %+v", f)
	}
	nc, err := net.Dial("unix", SocketPath(r.data, r.chat))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	br := bufio.NewReader(nc)
	for _, line := range []string{`{"op":"hello","proto":1,"since":4611686018427387904}`,
		`{"op":"ask","payload":` + string(askPayload) + `}`} {
		if _, err := io.WriteString(nc, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		f, _ := DecodeFrame(line)
		if f.Hello != nil {
			if f.Hello.Proto != 1 {
				t.Fatalf("hello %+v", f.Hello)
			}
			continue
		}
		if f.Fail == nil || f.Fail.Code != CodeBadRequest || !strings.Contains(f.Fail.Msg, "protocol 2") {
			t.Fatalf("a protocol 1 ask: %s", line)
		}
		break
	}
}

// `socrates mcp-ask`: ask_user on MCP stdio, each call through the host, and
// the model gets REBUILD.md §5.2 as the tool's text.
func TestMCPAskServesAskUserThroughTheHost(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/sleep 30")
	cl.until(5*time.Second, isType(agent.TypeTurnStarted))
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeMCPAsk(context.Background(), SocketPath(r.data, r.chat), inR, outW) }()
	out := bufio.NewReader(outR)
	rpc := func(msg string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(inW, msg+"\n"); err != nil {
			t.Fatal(err)
		}
		line, err := out.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return v
	}
	init := rpc(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`)
	if res := init["result"].(map[string]any); res["protocolVersion"] != "2025-11-25" ||
		!strings.Contains(res["instructions"].(string), MCPAskTool) {
		t.Fatalf("initialize %v", init)
	}
	if _, err := io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	go func() {
		ask := cl.until(5*time.Second, isType(agent.TypeAsk))
		cl.answer("a1", ask.AskID, "phone", json.RawMessage(`{"answers":{"db":{"selected":["sqlite"],"note":"one file"}}}`))
	}()
	res := rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":` + string(askPayload) + `}}`)
	result := res["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if result["isError"] != false || !strings.HasPrefix(text, `{"answers":{"db":{"selected":["sqlite"],"other":null,"note":"one file"`) {
		t.Fatalf("the model got %v", res)
	}
	inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mcp-ask did not end with its input")
	}
}

// frame is the next frame of a connection that reads no events.
func (cl *client) frame(timeout time.Duration) Frame {
	cl.t.Helper()
	select {
	case f, open := <-cl.frames:
		if !open {
			cl.t.Fatal("the connection closed")
		}
		return f
	case <-time.After(timeout):
		cl.t.Fatal("no frame")
	}
	return Frame{}
}
