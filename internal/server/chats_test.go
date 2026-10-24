//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The test binary is also the agent host the chat manager launches (TestMain
// dispatches agent-host), with the scripted agent switched on.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		runtime.LockOSThread()
	}
	os.Setenv(agent.FakeEnv, "1")
	agent.EnableFake()
}

// chatEnv is a signed in server whose chats run in real host processes.
type chatEnv struct {
	*testEnv
	data string
}

func newChatEnv(t *testing.T, park time.Duration) *chatEnv {
	t.Helper()
	e := signedIn(t)
	c := &chatEnv{testEnv: e, data: e.srv.dataDir}
	c.tune(e.srv, park)
	e.srv.chats.start()
	t.Cleanup(func() {
		c.stopHosts(t)
		e.srv.chats.close()
	})
	return c
}

func (c *chatEnv) tune(srv *Server, park time.Duration) {
	srv.chats.launcher.Scope = false
	srv.chats.launcher.Binary = os.Args[0]
	srv.chats.parkAfter = park
	srv.chats.ackWait = 5 * time.Second
}

// restart is a new server process on the same store and data folder: the old
// one lets go of its hosts without stopping them, as a service restart does.
func (c *chatEnv) restart(t *testing.T, down time.Duration, before func()) {
	t.Helper()
	c.srv.chats.close()
	c.server.Close()
	if before != nil {
		before()
	}
	time.Sleep(down)
	srv, err := New(c.store, c.data)
	if err != nil {
		t.Fatal(err)
	}
	c.tune(srv, c.srv.chats.parkAfter)
	srv.chats.start()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.chats.close()
		ts.Close()
		srv.push.stop()
	})
	c.srv, c.server = srv, ts
}

// stopHosts ends every host the test started, so no test leaves a process.
func (c *chatEnv) stopHosts(t *testing.T) {
	entries, _ := os.ReadDir(filepath.Join(c.data, "chats"))
	for _, e := range entries {
		conn, err := agenthost.Dial(context.Background(), agenthost.SocketPath(c.data, e.Name()), 1<<62)
		if err == nil {
			_ = conn.Send(agenthost.Command{Op: agenthost.OpStop})
			conn.Close()
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if alive, _ := c.srv.chats.launcher.Alive(e.Name()); !alive {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func (c *chatEnv) post(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, decoded := c.do(t, c.client, "POST", path, string(raw))
	return res.StatusCode, decoded
}

func (c *chatEnv) create(t *testing.T) string {
	t.Helper()
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "create-" + t.Name(), "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	return body["session"].(map[string]any)["id"].(string)
}

func (c *chatEnv) send(t *testing.T, id, clientID, text string) map[string]any {
	t.Helper()
	code, body := c.post(t, "/api/chats/"+id+"/messages", map[string]any{"client_id": clientID, "text": text})
	if code != http.StatusOK {
		t.Fatalf("send %s: %d %v", clientID, code, body)
	}
	return body
}

func (c *chatEnv) journal(t *testing.T, id string) []agent.Event {
	t.Helper()
	events, err := agent.ReadJournal(agenthost.JournalPath(c.data, id))
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// waitJournal waits for the chat's journal to satisfy cond.
func (c *chatEnv) waitJournal(t *testing.T, id string, within time.Duration, what string, cond func([]agent.Event) bool) []agent.Event {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		events := c.journal(t, id)
		if cond(events) {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (%d events)", what, len(events))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func count(events []agent.Event, ok func(agent.Event) bool) int {
	n := 0
	for _, ev := range events {
		if ok(ev) {
			n++
		}
	}
	return n
}

func turnsEnded(n int) func([]agent.Event) bool {
	return func(events []agent.Event) bool {
		return count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded }) >= n
	}
}

// chatClient is one browser on a chat stream.
type chatClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu     sync.Mutex
	events []agent.Event
	deltas int
	hello  map[string]any
	synced bool
	err    error
	woke   chan struct{}
}

func (c *chatEnv) stream(t *testing.T, id string, since int64) *chatClient {
	t.Helper()
	url := "ws" + strings.TrimPrefix(c.server.URL, "http") + "/api/chats/" + id + "/stream"
	conn, res, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: c.eventsHeader(t), Subprotocols: []string{chatSubprotocol},
	})
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("dial the chat stream: %v (%d)", err, status)
	}
	conn.SetReadLimit(64 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	hello, _ := json.Marshal(map[string]any{"t": "hello", "since": since})
	if err := conn.Write(context.Background(), websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	cl := &chatClient{t: t, conn: conn, woke: make(chan struct{}, 1)}
	go func() {
		for {
			_, payload, err := conn.Read(context.Background())
			cl.mu.Lock()
			if err != nil {
				cl.err = err
				cl.mu.Unlock()
				cl.wake()
				return
			}
			var head struct {
				T string `json:"t"`
			}
			_ = json.Unmarshal(payload, &head)
			switch head.T {
			case "hello":
				_ = json.Unmarshal(payload, &cl.hello)
			case "synced":
				cl.synced = true
			case "delta":
				cl.deltas++
			case "ev":
				var ev agent.Event
				_ = json.Unmarshal(payload, &ev)
				cl.events = append(cl.events, ev)
			}
			cl.mu.Unlock()
			cl.wake()
		}
	}()
	return cl
}

func (cl *chatClient) wake() {
	select {
	case cl.woke <- struct{}{}:
	default:
	}
}

// until waits for an event that satisfies ok, and returns every event so far.
func (cl *chatClient) until(within time.Duration, what string, ok func(agent.Event) bool) []agent.Event {
	cl.t.Helper()
	deadline := time.After(within)
	for {
		cl.mu.Lock()
		for _, ev := range cl.events {
			if ok(ev) {
				out := append([]agent.Event{}, cl.events...)
				cl.mu.Unlock()
				return out
			}
		}
		err := cl.err
		cl.mu.Unlock()
		if err != nil {
			cl.t.Fatalf("the stream ended waiting for %s: %v", what, err)
		}
		select {
		case <-cl.woke:
		case <-deadline:
			cl.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (cl *chatClient) snapshot() []agent.Event {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return append([]agent.Event{}, cl.events...)
}

func isEv(typ string) func(agent.Event) bool {
	return func(ev agent.Event) bool { return ev.Type == typ }
}

// sameAsJournal checks that what a client received is the journal, from
// first, without a gap or a repeat.
func sameAsJournal(t *testing.T, got, journal []agent.Event, first int64) {
	t.Helper()
	for i, ev := range got {
		if ev.Seq != first+int64(i) {
			t.Fatalf("event %d has seq %d, want %d: a gap or a duplicate", i, ev.Seq, first+int64(i))
		}
		j := journal[ev.Seq-1]
		a, _ := json.Marshal(ev)
		b, _ := json.Marshal(j)
		if string(a) != string(b) {
			t.Fatalf("seq %d differs from the journal:\n%s\n%s", ev.Seq, a, b)
		}
	}
}

func lastText(events []agent.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := events[i]; ev.Type == agent.TypePart && ev.Kind == agent.PartText && ev.State == agent.PartDone {
			return ev.Text
		}
	}
	return ""
}

func TestChatCreateIsIdempotentAndHasNoTerminal(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "create-" + t.Name(), "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data})
	if code != http.StatusOK || body["session"].(map[string]any)["id"] != id {
		t.Fatalf("a repeated create: %d %v", code, body)
	}
	row, _ := c.store.GetSession(id)
	if !row.IsChat() || row.Harness != "fake" {
		t.Fatalf("row %+v", row)
	}
	// The test binary has the adapters of the three agents (chatagents_test.go);
	// a Shell has none.
	if code, _ := c.post(t, "/api/chats", map[string]any{"client_id": "x", "harness": "shell"}); code != http.StatusBadRequest {
		t.Fatalf("a harness with no adapter: %d", code)
	}
	if res, _ := c.do(t, c.client, "POST", "/api/sessions/"+id+"/restart", ""); res.StatusCode != http.StatusConflict {
		t.Fatalf("a chat's terminal route answered %d", res.StatusCode)
	}
	c.send(t, id, "m1", "hello")
	events := c.waitJournal(t, id, 10*time.Second, "the reply", turnsEnded(1))
	if got := lastText(events); !strings.HasPrefix(got, "you said: hello (turn 1") {
		t.Fatalf("reply %q", got)
	}
	row, _ = c.store.GetSession(id)
	if row.CLISessionID == "" || row.CLISessionState != store.CLIKnown {
		t.Fatalf("the conversation id was not recorded: %+v", row)
	}
	// The detector has the chat, through the journal: a turn ended unread,
	// until somebody sends it something, which is having seen it.
	if a := c.waitActivity(t, id, termux.StateIdle); !a.Unread {
		t.Fatalf("a finished turn nobody looked at: %+v", a)
	}
	// A repeat of the message is answered from the journal and sent nowhere.
	if body := c.send(t, id, "m1", "hello"); body["dup"] != true {
		t.Fatalf("a repeated message: %v", body)
	}
	res, got := c.do(t, c.client, "GET", "/api/chats/"+id+"?history=1", "")
	if res.StatusCode != http.StatusOK || got["chat"].(map[string]any)["activity"] != "idle" || got["messages"] == nil {
		t.Fatalf("GET chat: %d %v", res.StatusCode, got)
	}
}

// The server restarts in the middle of a turn: the turn completes, and a
// browser that comes back with the last seq it had gets exactly the rest.
func TestChatSurvivesAServerRestartMidTurn(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	browser := c.stream(t, id, 0)
	c.send(t, id, "m1", "/stream 300 10")
	browser.until(10*time.Second, "streaming", isEv(agent.TypePartDelta))
	c.restart(t, 300*time.Millisecond, nil)
	got := browser.snapshot()
	if journal := c.journal(t, id); agent.Fold(journal).Turn == nil {
		t.Fatal("the turn ended before the restart; the test proves nothing")
	}
	again := c.stream(t, id, got[len(got)-1].Seq)
	rest := again.until(15*time.Second, "the end of the turn", isEv(agent.TypeTurnEnded))
	all := append(got, rest...)
	journal := c.journal(t, id)
	sameAsJournal(t, all, journal, 1)
	if !strings.HasSuffix(lastText(journal), "w300 ") {
		t.Fatalf("the turn did not complete: %q", lastText(journal))
	}
	if again.hello == nil || again.hello["last_seq"] == nil {
		t.Fatalf("hello %v", again.hello)
	}
}

// The server is down for thirty seconds while a turn finishes without it.
func TestChatTurnFinishesWhileTheServerIsDown(t *testing.T) {
	if testing.Short() {
		t.Skip("waits thirty seconds")
	}
	t.Parallel()
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	browser := c.stream(t, id, 0)
	c.send(t, id, "m1", "/stream 200 20")
	browser.until(10*time.Second, "streaming", isEv(agent.TypePartDelta))
	var whileDown []agent.Event
	c.restart(t, 30*time.Second, func() {
		c.waitJournal(t, id, 20*time.Second, "the turn to end with no server", turnsEnded(1))
		whileDown = c.journal(t, id)
	})
	got := browser.snapshot()
	if agent.Fold(whileDown).Turn != nil {
		t.Fatal("the turn did not finish while the server was down")
	}
	again := c.stream(t, id, got[len(got)-1].Seq)
	rest := again.until(10*time.Second, "the replay", isEv(agent.TypeTurnEnded))
	sameAsJournal(t, append(got, rest...), c.journal(t, id), 1)
}

// The host is killed in the middle of a turn: the server notices, confirms it
// dead, starts a new one, which records the turn as interrupted and resumes
// the conversation. Nobody stopped that turn, so it is news: pushed as
// "Stopped" with what it had said, unread, and on Home with its words.
func TestChatRecoversAKilledHost(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	id := c.create(t)
	c.send(t, id, "m1", "/stream 500 10")
	events := c.waitJournal(t, id, 10*time.Second, "streaming", func(evs []agent.Event) bool {
		return count(evs, isEv(agent.TypePartDelta)) > 0
	})
	p := agent.Fold(events)
	if p.HostPID == 0 {
		t.Fatal("no host pid in the journal")
	}
	if err := syscall.Kill(p.HostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	events = c.waitJournal(t, id, 15*time.Second, "the resume", func(evs []agent.Event) bool {
		return count(evs, func(ev agent.Event) bool { return ev.Type == agent.TypeNotice && ev.Code == agent.NoticeResumed }) == 1 &&
			count(evs, func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady }) == 2
	})
	after := agent.Fold(events)
	if after.Turn != nil || after.LastTurn.Outcome != agent.OutcomeInterrupted || after.ConversationID != p.ConversationID {
		t.Fatalf("after the recovery: turn %+v last %+v conv %s/%s", after.Turn, after.LastTurn, p.ConversationID, after.ConversationID)
	}
	if row, _ := c.store.GetSession(id); !row.Resumed {
		t.Fatal("the row does not say the chat resumed")
	}
	if push := pushOfKind(t, sent, pushFinished, 10*time.Second); !strings.HasPrefix(push.Body, "Stopped · w1 w2") {
		t.Fatalf("the push of the killed turn: %+v", push)
	}
	if a := c.waitActivity(t, id, termux.StateIdle); !a.Unread {
		t.Fatalf("the killed turn left the chat %+v", a)
	}
	if b, _ := c.srv.chats.briefOfChat(id); !strings.HasPrefix(b.Line, "w1 w2") || !strings.HasPrefix(b.News, "Stopped · w1 w2") {
		t.Fatalf("Home's line %q, the news %q", b.Line, b.News)
	}
	c.send(t, id, "m2", "after")
	events = c.waitJournal(t, id, 10*time.Second, "the next turn", turnsEnded(2))
	if got := lastText(events); !strings.Contains(got, fmt.Sprintf("(turn 2 of %s)", p.ConversationID)) {
		t.Fatalf("the resumed conversation said %q", got)
	}
}

// The server crashes right after sending a message, before the ack: the next
// server sends it again from the outbox, and the agent gets it once.
func TestChatSendIsExactlyOnceAcrossAServerCrash(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m0", "warm up")
	c.waitJournal(t, id, 10*time.Second, "the first turn", turnsEnded(1))
	cmd := agenthost.Command{Op: agenthost.OpSend, ClientID: "m1", Text: "once"}
	raw, _ := json.Marshal(cmd)
	if _, err := c.store.PutOutbox(id, "m1", raw); err != nil {
		t.Fatal(err)
	}
	p, _ := c.srv.chats.peer(id)
	p.mu.Lock()
	err := p.conn.Send(cmd)
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Gone before the ack could be read.
	c.restart(t, 0, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending, _ := c.store.Outbox(id)
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the outbox still holds %+v", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
	events := c.waitJournal(t, id, 10*time.Second, "the second turn", turnsEnded(2))
	if n := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeMessage && ev.ClientID == "m1" }); n != 1 {
		t.Fatalf("m1 is in the journal %d times", n)
	}
	time.Sleep(300 * time.Millisecond)
	if n := count(c.journal(t, id), isEv(agent.TypeTurnStarted)); n != 2 {
		t.Fatalf("%d turns for two messages", n)
	}
}

// Two devices on one chat see the same events.
func TestChatTwoDevicesOnOneStream(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	phone, laptop := c.stream(t, id, 0), c.stream(t, id, 0)
	c.send(t, id, "m1", "/tool")
	a := phone.until(10*time.Second, "the turn", isEv(agent.TypeTurnEnded))
	b := laptop.until(10*time.Second, "the turn", isEv(agent.TypeTurnEnded))
	journal := c.journal(t, id)
	sameAsJournal(t, a, journal, 1)
	sameAsJournal(t, b, journal, 1)
	if len(a) != len(b) {
		t.Fatalf("the devices saw %d and %d events", len(a), len(b))
	}
	if phone.deltas == 0 || laptop.deltas == 0 {
		t.Fatalf("no live deltas: %d %d", phone.deltas, laptop.deltas)
	}
}

// waitActivity waits for the detector's picture of a chat, which follows the
// server's copy of the journal a moment behind the file.
func (c *chatEnv) waitActivity(t *testing.T, id string, want termux.State) termux.Activity {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a := c.srv.manager.ActivityOf(id)
		if a.State == want {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("the chat is %+v, want %s", a, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *chatEnv) waitAsk(t *testing.T, id string) string {
	t.Helper()
	events := c.waitJournal(t, id, 10*time.Second, "the question", func(evs []agent.Event) bool {
		return count(evs, isEv(agent.TypeAsk)) > 0
	})
	for _, ev := range events {
		if ev.Type == agent.TypeAsk {
			return ev.AskID
		}
	}
	return ""
}

// Two devices answer one question: the first wins, and the second is told it
// was answered elsewhere.
func TestChatAnswerFromTwoDevices(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/ask")
	ask := c.waitAsk(t, id)
	c.waitActivity(t, id, termux.StateWaiting)
	path := "/api/chats/" + id + "/asks/" + ask + "/answer"
	code, body := c.post(t, path, map[string]any{"client_id": "a1", "answer": "a", "device": "phone"})
	if code != http.StatusOK {
		t.Fatalf("the first answer: %d %v", code, body)
	}
	code, body = c.post(t, path, map[string]any{"client_id": "b1", "answer": "b", "device": "laptop"})
	if code != http.StatusConflict || body["answered_elsewhere"] != true || body["by_device"] != "phone" {
		t.Fatalf("the second device: %d %v", code, body)
	}
	if code, body = c.post(t, path, map[string]any{"client_id": "a1", "answer": "a", "device": "phone"}); code != http.StatusOK {
		t.Fatalf("the first device again: %d %v", code, body)
	}
	events := c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	if got := lastText(events); got != `you answered: "a"` {
		t.Fatalf("the agent got %q", got)
	}
	if n := count(events, isEv(agent.TypeAskAnswered)); n != 1 {
		t.Fatalf("%d answers journaled", n)
	}
	if code, _ := c.post(t, "/api/chats/"+id+"/asks/nope/answer", map[string]any{"client_id": "c1", "answer": 1}); code != http.StatusNotFound {
		t.Fatalf("an unknown question: %d", code)
	}
}

// An answer that arrives after the call that asked is gone - here the turn was
// interrupted - reaches the agent as a user message.
func TestChatAnswerAfterTheCallIsGoneBecomesAMessage(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/ask")
	ask := c.waitAsk(t, id)
	if code, body := c.post(t, "/api/chats/"+id+"/interrupt", map[string]any{"client_id": "i1"}); code != http.StatusOK {
		t.Fatalf("interrupt: %d %v", code, body)
	}
	c.waitJournal(t, id, 10*time.Second, "the interrupt", turnsEnded(1))
	// The question is still open: the agent still waits for it.
	c.waitActivity(t, id, termux.StateWaiting)
	code, body := c.post(t, "/api/chats/"+id+"/asks/"+ask+"/answer", map[string]any{"client_id": "a1", "answer": "b", "device": "phone"})
	if code != http.StatusOK {
		t.Fatalf("answer: %d %v", code, body)
	}
	events := c.waitJournal(t, id, 10*time.Second, "the turn the answer starts", turnsEnded(2))
	p := agent.Fold(events)
	if a := p.Ask(ask); a.Delivered != agent.ViaMessage {
		t.Fatalf("delivered %q", a.Delivered)
	}
	if got := lastText(events); !strings.Contains(got, "Answer to your earlier question (Fake question):") {
		t.Fatalf("the agent got %q", got)
	}
}

// An idle chat parks its host, and the next message starts a new one that
// resumes the conversation.
func TestChatParksWhenIdleAndResumes(t *testing.T) {
	c := newChatEnv(t, 300*time.Millisecond)
	id := c.create(t)
	c.send(t, id, "m1", "one")
	c.waitJournal(t, id, 10*time.Second, "the park", func(evs []agent.Event) bool {
		return agent.Fold(evs).Process == agent.ProcessParked
	})
	deadline := time.Now().Add(5 * time.Second)
	for alive, _ := c.srv.chats.launcher.Alive(id); alive; alive, _ = c.srv.chats.launcher.Alive(id) {
		if time.Now().After(deadline) {
			t.Fatal("the parked host did not exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.send(t, id, "m2", "two")
	events := c.waitJournal(t, id, 10*time.Second, "the resumed turn", turnsEnded(2))
	if got := lastText(events); !strings.Contains(got, "(turn 2 of") {
		t.Fatalf("after the park: %q", got)
	}
}

// Messages sent during a turn are queued, and taken up one turn each, in order.
func TestChatQueuesMessagesDuringATurn(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/sleep 1")
	c.waitJournal(t, id, 10*time.Second, "the first turn", func(evs []agent.Event) bool { return agent.Fold(evs).Turn != nil })
	c.send(t, id, "m2", "second")
	c.send(t, id, "m3", "third")
	if s := c.srv.chats.chats[id].summary(); s.Queued != 2 || s.Activity != agent.ActivityBusy {
		t.Fatalf("summary %+v", s)
	}
	events := c.waitJournal(t, id, 10*time.Second, "three turns", turnsEnded(3))
	p := agent.Fold(events)
	var order []string
	for _, ev := range events {
		if ev.Type == agent.TypeTurnStarted {
			order = append(order, p.Message(ev.MessageID).ClientID)
		}
	}
	if strings.Join(order, ",") != "m1,m2,m3" {
		t.Fatalf("turn order %v", order)
	}
	queued := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeMessageState && ev.State == agent.MessageQueued })
	if queued != 2 {
		t.Fatalf("%d messages were queued", queued)
	}
}

// A stream needs a hello, and ends with 4401 when its login is revoked.
func TestChatStreamHelloAndRevoke(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	cl := c.stream(t, id, 0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		cl.mu.Lock()
		ok := cl.synced
		cl.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.do(t, c.client, "POST", "/api/logout", "")
	deadline = time.Now().Add(5 * time.Second)
	for {
		cl.mu.Lock()
		err := cl.err
		cl.mu.Unlock()
		if err != nil {
			if websocket.CloseStatus(err) != eventsSignedOut {
				t.Fatalf("closed with %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream outlived the login")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A chat restored onto a machine without its folder says so in the session
// list, and is pointed to a folder that is there: the agent then runs in it.
// A folder that is not there is refused, and so is a terminal that runs.
func TestAChatWhoseFolderIsMissingIsPointedToAnother(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gone := filepath.Join(t.TempDir(), "old-project")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "create-" + t.Name(), "harness": "fake",
		"workdir_mode": "custom", "workdir": gone})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["session"].(map[string]any)["id"].(string)
	c.send(t, id, "m1", "hello")
	c.waitJournal(t, id, 10*time.Second, "the first reply", turnsEnded(1))
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	_, got := c.do(t, c.client, "GET", "/api/sessions/"+id, "")
	if got["session"].(map[string]any)["workdir_missing"] != true {
		t.Fatalf("a chat without its folder: %v", got["session"])
	}

	put := func(dir string) (int, map[string]any) {
		raw, _ := json.Marshal(map[string]string{"workdir": dir})
		res, body := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/workdir", string(raw))
		return res.StatusCode, body
	}
	if code, body := put(filepath.Join(t.TempDir(), "nowhere")); code != http.StatusBadRequest {
		t.Fatalf("a folder that is not there: %d %v", code, body)
	}
	here := t.TempDir()
	code, body = put(here)
	session, _ := body["session"].(map[string]any)
	if code != http.StatusOK || session["workdir"] != here || session["workdir_missing"] == true || session["workdir_mode"] != "custom" {
		t.Fatalf("pointed to %s: %d %v", here, code, body)
	}
	if alive, _ := c.srv.chats.launcher.Alive(id); alive {
		t.Fatal("the host in the old folder is still up")
	}
	c.send(t, id, "m2", "again")
	c.waitJournal(t, id, 10*time.Second, "the reply in the new folder", turnsEnded(2))

	if err := c.store.CreateSession(&store.Session{ID: "term", Title: "Shell", Harness: "shell", Workdir: here,
		WorkdirMode: "custom", TmuxName: "t-term", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"workdir": t.TempDir()})
	if res, _ := c.do(t, c.client, "PUT", "/api/sessions/term/workdir", string(raw)); res.StatusCode != http.StatusConflict {
		t.Fatalf("a running terminal pointed elsewhere: %d", res.StatusCode)
	}
}

// A turn the server has not heard of yet when a chat is pointed to another
// folder - another device's message, taken up a moment before - is not cut
// off: the host finishes it in the old folder and parks, and the next message
// starts the agent in the new one.
func TestPointingAChatElsewhereLetsATurnThatJustStartedFinish(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "/sleep 1.5")
	p, err := c.srv.chats.peer(id)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !p.summary().TurnRunning {
		if time.Now().After(deadline) {
			t.Fatal("the turn never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The server's view is a moment behind the host's, as it is when the
	// turn starts between the handler's look and what it sends the host.
	p.mu.Lock()
	p.proj.Turn = nil
	p.brief = briefOf(p.proj)
	p.mu.Unlock()

	here := t.TempDir()
	raw, _ := json.Marshal(map[string]string{"workdir": here})
	if res, body := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/workdir", string(raw)); res.StatusCode != http.StatusOK {
		t.Fatalf("pointed to %s: %d %v", here, res.StatusCode, body)
	}
	events := c.waitJournal(t, id, 10*time.Second, "the host's end", func(evs []agent.Event) bool {
		process := agent.Fold(evs).Process
		return process == agent.ProcessParked || process == agent.ProcessExited
	})
	if last := agent.Fold(events); last.Process != agent.ProcessParked || last.LastTurn == nil ||
		last.LastTurn.Outcome != agent.OutcomeCompleted {
		t.Fatalf("the host ended %q, the turn %+v", last.Process, last.LastTurn)
	}
	c.send(t, id, "m2", "again")
	c.waitJournal(t, id, 10*time.Second, "the reply in the new folder", turnsEnded(2))
	if spec, err := agenthost.ReadSpec(c.data, id); err != nil || spec.Workdir != here {
		t.Fatalf("the next host's folder: %q %v", spec.Workdir, err)
	}
}
