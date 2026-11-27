//go:build linux

package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
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

// An ask of a turn the person stopped is closed, so the chat no longer
// waits on it, and it does not keep the host from parking: its call is gone.
// Answered later all the same, it reaches the agent of the next host as a
// message.
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
	if a := p.Ask(ask.AskID); a == nil || !a.Closed || a.ClosedBy != agent.AskClosedStopped ||
		len(p.OpenAsks()) != 0 || p.Summary().Activity != agent.ActivityIdle {
		t.Fatalf("after the park: %+v, %d open asks, activity %s", a, len(p.OpenAsks()), p.Summary().Activity)
	}
	last := r.journal()[len(r.journal())-1].Seq
	r.park = time.Hour
	r.launch()
	next := r.dial(last)
	next.seen(5*time.Second, isProcess(agent.ProcessReady))
	next.answer("a1", ask.AskID, "phone", json.RawMessage(`{"answers":{"q1":{"selected":["a"]}}}`))
	next.seen(5*time.Second, isNotice(agent.NoticeAnswerAsMessage))
	next.seen(5*time.Second, isType(agent.TypeTurnEnded))
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: The person answered your question "+ask.AskID+" (Fake question)") ||
		!strings.Contains(got, "A [a]") {
		t.Fatalf("the agent got %q", got)
	}
}

// A message the person sends while the call waits closes the question and
// releases the call, which returns that it was not answered: the agent goes
// on with the message, and the chat stops waiting. A closed question still
// takes an answer, which goes as a message.
func TestAMessageWhileTheCallWaitsClosesTheQuestion(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	cl.send("m2", "skip that, do X")
	closed := cl.seen(5*time.Second, isType(agent.TypeAskClosed))
	if closed.AskID != ask.AskID || closed.Code != agent.AskClosedMessage {
		t.Fatalf("closed %+v", closed)
	}
	// The released turn, then the message's.
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	p := agent.Fold(r.journal())
	if a := p.Ask(ask.AskID); a.Answered || !a.Closed || len(p.OpenAsks()) != 0 || p.Summary().Activity != agent.ActivityIdle {
		t.Fatalf("after the message: %+v, %d open, %s", a, len(p.OpenAsks()), p.Summary().Activity)
	}
	released := ""
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if strings.HasPrefix(part.Text, "you answered: ") {
				released = part.Text
			}
		}
	}
	if !strings.Contains(released, "wrote a new message instead") {
		t.Fatalf("the released call returned %q", released)
	}
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: skip that, do X") {
		t.Fatalf("the agent got %q", got)
	}
	cl.answer("a1", ask.AskID, "phone", json.RawMessage(`{"answers":{"q1":{"selected":["a"]}}}`))
	if ev := cl.seen(5*time.Second, isType(agent.TypeAskDelivered)); ev.Via != agent.ViaMessage {
		t.Fatalf("delivered %+v", ev)
	}
	cl.seen(5*time.Second, isNotice(agent.NoticeAnswerAsMessage))
}

// An ask_user question (answered as a message by design) stays open through
// a message of the person's and a Stop: the agent never stopped for it. Its
// answer then goes as a message, without a notice.
func TestAMessageOrAStopLeavesAnAskUserQuestionOpen(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/asknext")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.send("m2", "also do X")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.send("m3", "/sleep 30")
	cl.until(5*time.Second, isType(agent.TypeTurnStarted))
	if err := cl.c.Send(Command{Op: OpInterrupt, ClientID: "i1"}); err != nil {
		t.Fatal(err)
	}
	cl.ack("i1", 5*time.Second)
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	p := agent.Fold(r.journal())
	if a := p.Ask(ask.AskID); a.Closed || len(p.OpenAsks()) != 1 || p.Summary().Activity != agent.ActivityWaiting {
		t.Fatalf("after a message and a stop: %+v, %d open, %s", a, len(p.OpenAsks()), p.Summary().Activity)
	}
	cl.answer("a1", ask.AskID, "phone", json.RawMessage(`{"answers":{"q1":{"selected":["a"]}}}`))
	if ev := cl.seen(5*time.Second, isType(agent.TypeAskDelivered)); ev.Via != agent.ViaMessage {
		t.Fatalf("delivered %+v", ev)
	}
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	for _, ev := range r.journal() {
		if ev.Type == agent.TypeNotice && ev.Code == agent.NoticeAnswerAsMessage {
			t.Fatal("a notice for an answer meant to go as a message")
		}
	}
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: The person answered your question "+ask.AskID) {
		t.Fatalf("the agent got %q", got)
	}
}

// The person's message carries the agent's open ask_user questions in
// Socrates' note, so that the agent withdraws those it settles; another
// chat's message does not.
func TestAMessageOfThePersonsListsTheOpenQuestions(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/asknext")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.send("m2", "use SQLite")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	if err := cl.c.Send(Command{Op: OpSend, ClientID: "m3", Text: "hello from next door", Notice: agent.NoticeChatMessage,
		Sender: &agent.Sender{Chat: "c_other", Title: "Other"}}); err != nil {
		t.Fatal(err)
	}
	cl.ack("m3", 5*time.Second)
	notes := map[string]string{}
	for _, ev := range r.journal() {
		if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser {
			notes[ev.ClientID] = ev.Note
		}
	}
	if n := notes["m2"]; !strings.Contains(n, "- "+ask.AskID+": ") || !strings.Contains(n, askuser.WithdrawName) {
		t.Fatalf("the person's message has the note %q", n)
	}
	if n := notes["m3"]; strings.Contains(n, ask.AskID) {
		t.Fatalf("another chat's message has the note %q", n)
	}
	if notes["m1"] != "" {
		t.Fatalf("a message with no question open has the note %q", notes["m1"])
	}
}

// A message the person did not write - another chat's - leaves a question
// the call waits on open: the person has not moved on.
func TestAnotherChatsMessageLeavesTheQuestionOpen(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	if err := cl.c.Send(Command{Op: OpSend, ClientID: "m2", Text: "hello from next door", Notice: agent.NoticeChatMessage,
		Sender: &agent.Sender{Chat: "c_other", Title: "Other"}}); err != nil {
		t.Fatal(err)
	}
	cl.ack("m2", 5*time.Second)
	p := agent.Fold(r.journal())
	if a := p.Ask(ask.AskID); a.Closed || p.Summary().Activity != agent.ActivityWaiting {
		t.Fatalf("after the other chat's message: %+v, %s", a, p.Summary().Activity)
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
	next.seen(5*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnEnded && ev.Outcome == agent.OutcomeCompleted
	})
	if got := lastReply(r.journal()); !strings.Contains(got, "B [b]") {
		t.Fatalf("the agent got %q", got)
	}
}

// A local tool asks over the host protocol (version 2): the ask belongs to
// the running turn, and its answer comes back on the tool's connection.
func TestALocalToolAsksThroughTheHost(t *testing.T) {
	r := newRig(t, 200*time.Millisecond)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/sleep 30")
	turn := cl.until(5*time.Second, isType(agent.TypeTurnStarted))
	tool := r.dial(NoEvents)
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
		switch {
		case f.Ack != nil && f.Ack.AskID != ask.AskID:
			t.Fatalf("ack %+v", f.Ack)
		case f.Event != nil || f.Delta != nil:
			t.Fatalf("a tool's connection got %+v", f)
		}
		f = tool.frame(5 * time.Second)
	}
	if f.Answer.AskID != ask.AskID || string(f.Answer.Answer) != string(answer) {
		t.Fatalf("answer frame %+v", f.Answer)
	}
	if ev := cl.seen(5*time.Second, isType(agent.TypeAskDelivered)); ev.Via != agent.ViaTool {
		t.Fatalf("delivered %+v", ev)
	}

	// An interrupt withdraws the call with its turn, though the tool has not
	// let go of its connection: the host parks all the same, and the ask is
	// answered as a message through the next one.
	waiting := r.dial(NoEvents)
	if err := waiting.c.Send(Command{Op: OpAsk, Payload: askPayload}); err != nil {
		t.Fatal(err)
	}
	second := cl.until(5*time.Second, isType(agent.TypeAsk))
	if err := cl.c.Send(Command{Op: OpInterrupt, ClientID: "i1"}); err != nil {
		t.Fatal(err)
	}
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.until(5*time.Second, isProcess(agent.ProcessParked))
	r.waitDead(5 * time.Second)
	waiting.c.Close()
	r.park = time.Hour
	r.launch()
	next := r.dial(cl.events[len(cl.events)-1].Seq)
	next.answer("a2", second.AskID, "phone", answer)
	if ev := next.seen(5*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeAskDelivered && ev.AskID == second.AskID
	}); ev.Via != agent.ViaMessage {
		t.Fatalf("delivered %+v", ev)
	}

	// Outside a turn nothing could wait for an answer: such a question
	// comes from a call already withdrawn, and is refused.
	next.until(5*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnEnded && ev.Outcome == agent.OutcomeCompleted
	})
	late := r.dial(NoEvents)
	if err := late.c.Send(Command{Op: OpAsk, Payload: askPayload}); err != nil {
		t.Fatal(err)
	}
	if f := late.frame(5 * time.Second); f.Fail == nil || f.Fail.Code != CodeBadRequest {
		t.Fatalf("an ask outside a turn: %+v", f)
	}
}

// A connection that asked for no events (a local tool's) gets no live
// deltas either: a long answer streaming in the chat is not its business.
func TestAToolsConnectionGetsNoDeltas(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.until(5*time.Second, isProcess(agent.ProcessReady))
	tool := r.dial(NoEvents)
	cl.send("m1", "/stream 40 1")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	select {
	case f := <-tool.frames:
		t.Fatalf("the tool got %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

// The ask op needs protocol 2 and an ask_user payload.
func TestTheAskOpIsRefusedWithoutItsProtocolOrAPayload(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.until(5*time.Second, isProcess(agent.ProcessReady))
	tool := r.dial(NoEvents)
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
	go func() {
		done <- ServeMCPAsk(context.Background(), SocketPath(r.data, r.chat), "test", chattools.Endpoint{}, "", inR, outW)
	}()
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
	call := `{"question":"Which database?","blocks":"The schema","options":[{"id":"pg","label":"PostgreSQL"}]}`
	res := rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":` + call + `}}`)
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	result := res["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if result["isError"] != false || text != askuser.QueuedText(ask.AskID, askuser.Decode(ask.Payload)) || ask.Via != agent.ViaMessage {
		t.Fatalf("the model got %v for %+v", res, ask)
	}
	tool := func(id int, name, args string) string {
		t.Helper()
		res := rpc(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, args))
		return res["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if got := tool(2, askuser.CheckName, `{}`); !strings.Contains(got, ask.AskID+" [open] Which database?") {
		t.Fatalf("check_questions: %s", got)
	}
	if got := tool(3, askuser.WithdrawName, `{"question_id":"`+ask.AskID+`","reason":"found it"}`); !strings.Contains(got, "is withdrawn") {
		t.Fatalf("withdraw_question: %s", got)
	}
	closed := cl.until(5*time.Second, isType(agent.TypeAskClosed))
	if closed.AskID != ask.AskID || closed.Code != agent.AskClosedWithdrawn || closed.Text != "found it" {
		t.Fatalf("closed %+v", closed)
	}
	if got := tool(4, askuser.WithdrawName, `{"question_id":"`+ask.AskID+`","reason":"again"}`); !strings.Contains(got, "not an open question") {
		t.Fatalf("a second withdraw: %s", got)
	}
	if got := tool(5, askuser.CheckName, `{}`); !strings.Contains(got, ask.AskID+" [withdrawn]") || !strings.Contains(got, "Withdrawn because: found it") {
		t.Fatalf("check_questions after it: %s", got)
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

// `socrates mcp-ask` tells a chat's agent about ask_user, the chat tools
// and its pages folder in its instructions.
func TestMCPAskInstructsAChat(t *testing.T) {
	instructions := func(chats chattools.Endpoint, pages string) string {
		t.Helper()
		in := strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}` + "\n")
		var out strings.Builder
		if err := ServeMCPAsk(context.Background(), "/nonexistent.sock", "test", chats, pages, in, &out); err != nil {
			t.Fatal(err)
		}
		var v struct {
			Result struct {
				Instructions string `json:"instructions"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(out.String()), &v); err != nil {
			t.Fatalf("%s: %v", out.String(), err)
		}
		return v.Result.Instructions
	}
	prefix := askuser.ServerName + "_"
	chat := chattools.Endpoint{Socket: "/nonexistent/chat-tools.sock", Token: "c.x"}
	base := askuser.Guidance(MCPAskTool, prefix) + "\n\n" + chattools.Guidance(prefix) + "\n\n" + agent.TodoGuidance("todowrite") + "\n\n" + agent.DocsGuidance
	if got := instructions(chat, ""); got != base {
		t.Fatalf("a chat's instructions:\n%s", got)
	}
	pages := "/data/chats/c1/pages"
	if got := instructions(chat, pages); got != base+"\n\n"+agent.PagesGuidance(pages) {
		t.Fatalf("a chat's instructions with its pages:\n%s", got)
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
