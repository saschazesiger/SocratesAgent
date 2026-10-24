//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// hostRig is a chat run by a real agent host process (this test binary as
// `socrates agent-host`) with the OpenCode adapter on the fake.
type hostRig struct {
	t    *testing.T
	r    *rig
	l    *agenthost.Launcher
	chat string
}

func newHostRig(t *testing.T) *hostRig {
	r := newRig(t)
	h := &hostRig{t: t, r: r, chat: "oc" + strings.ReplaceAll(t.Name(), "_", ""),
		l: &agenthost.Launcher{DataDir: r.data, Binary: os.Args[0], Logf: t.Logf}}
	if len(h.chat) > 60 {
		h.chat = h.chat[:60]
	}
	t.Cleanup(h.kill)
	return h
}

func (h *hostRig) launch(resume string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := h.l.Launch(ctx, agenthost.Spec{ChatID: h.chat, Harness: Harness, Workdir: h.r.work, Model: "fake/model",
		Env: h.r.env, Conversation: resume, ParkAfterMS: time.Hour.Milliseconds()})
	if err != nil {
		h.t.Fatalf("launch: %v", err)
	}
}

func (h *hostRig) kill() {
	c, err := agenthost.Dial(context.Background(), agenthost.SocketPath(h.r.data, h.chat), 1<<62)
	if err != nil {
		return
	}
	_ = c.Send(agenthost.Command{Op: agenthost.OpStop})
	c.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if alive, _ := h.l.Alive(h.chat); !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// conn is a connection with its events read into a channel.
type conn struct {
	t      *testing.T
	c      *agenthost.Conn
	events chan agent.Event
}

func (h *hostRig) dial() *conn {
	h.t.Helper()
	c, err := agenthost.Dial(context.Background(), agenthost.SocketPath(h.r.data, h.chat), 0)
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	cn := &conn{t: h.t, c: c, events: make(chan agent.Event, 10000)}
	go func() {
		defer close(cn.events)
		for {
			f, err := c.Next()
			if err != nil {
				return
			}
			if f.Event != nil {
				cn.events <- *f.Event
			}
		}
	}()
	h.t.Cleanup(func() { c.Close() })
	return cn
}

func (cn *conn) until(what string, ok func(agent.Event) bool) agent.Event {
	cn.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev, open := <-cn.events:
			if !open {
				cn.t.Fatalf("the connection closed waiting for %s", what)
			}
			if ok(ev) {
				return ev
			}
		case <-deadline:
			cn.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (cn *conn) send(clientID, text string) {
	cn.t.Helper()
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpSend, ClientID: clientID, Text: text}); err != nil {
		cn.t.Fatal(err)
	}
}

func turnEnded(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded }

func TestAHostRunsOpenCodeSurvivesItsDeathAndRebuildsALostJournal(t *testing.T) {
	h := newHostRig(t)
	h.launch("")
	cn := h.dial()
	sid := cn.until("the conversation", func(ev agent.Event) bool { return ev.Type == agent.TypeConversation }).ConversationID
	ready := cn.until("ready", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady })
	cliPID := ready.PID
	cn.send("m1", "/stream 3 5")
	if ev := cn.until("the first turn", turnEnded); ev.Outcome != agent.OutcomeCompleted {
		t.Fatalf("first turn %+v", ev)
	}

	// The host dies in the middle of a turn: its OpenCode goes with it.
	cn.send("m2", "/sleep 30")
	cn.until("the slow turn", func(ev agent.Event) bool { return ev.Type == agent.TypeTurnStarted })
	if err := syscall.Kill(cn.c.Hello.HostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the server to follow its host", func() bool { return !alive(cliPID) })
	eventually(t, 5*time.Second, "the lock", func() bool { a, _ := h.l.Alive(h.chat); return !a })
	if err := h.l.Reap(context.Background(), h.chat); err != nil {
		t.Fatal(err)
	}

	// The next host resumes the session.
	h.launch("")
	cn = h.dial()
	cn.until("the resume notice", func(ev agent.Event) bool { return ev.Type == agent.TypeNotice && ev.Code == agent.NoticeResumed })
	cn.send("m3", "after")
	ev := cn.until("the third turn", func(ev agent.Event) bool {
		return ev.Type == agent.TypePart && ev.State == agent.PartDone && strings.HasPrefix(ev.Text, "you said: after")
	})
	if !strings.Contains(ev.Text, "(turn 3 of "+sid+")") {
		t.Fatalf("resumed reply %q", ev.Text)
	}
	cn.until("its end", turnEnded)
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpPark, ClientID: "p1"}); err != nil {
		t.Fatal(err)
	}
	cn.until("parked", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessParked })
	eventually(t, 10*time.Second, "the parked host to go", func() bool { a, _ := h.l.Alive(h.chat); return !a })

	// The journal is lost; the row still knows the session.
	if err := os.RemoveAll(agenthost.ChatDir(h.r.data, h.chat)); err != nil {
		t.Fatal(err)
	}
	h.launch(sid)
	cn = h.dial()
	cn.until("ready", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady })
	events, err := agent.ReadJournal(agenthost.JournalPath(h.r.data, h.chat))
	if err != nil {
		t.Fatal(err)
	}
	p := agent.Fold(events)
	var users []string
	for _, m := range p.Messages {
		if m.Role == agent.RoleUser {
			users = append(users, m.Text)
		}
	}
	if strings.Join(users, "|") != "/stream 3 5|/sleep 30|after" || p.ConversationID != sid || p.Turn != nil {
		t.Fatalf("rebuilt users %q conversation %q turn %+v", users, p.ConversationID, p.Turn)
	}
	if p.LastTurn == nil || p.LastTurn.Outcome != agent.OutcomeCompleted {
		t.Fatalf("last rebuilt turn %+v", p.LastTurn)
	}
}

func TestAHostStartsFreshWhenTheSessionIsGone(t *testing.T) {
	h := newHostRig(t)
	h.launch("ses_gone000000000000000000")
	cn := h.dial()
	cn.until("the notice", func(ev agent.Event) bool {
		return ev.Type == agent.TypeNotice && ev.Code == agent.NoticeConversationLost
	})
	conv := cn.until("a new conversation", func(ev agent.Event) bool { return ev.Type == agent.TypeConversation })
	if conv.ConversationID == "ses_gone000000000000000000" || !validID(conv.ConversationID, "ses") {
		t.Fatalf("conversation %q", conv.ConversationID)
	}
	cn.send("m1", "hi")
	if ev := cn.until("the turn", turnEnded); ev.Outcome != agent.OutcomeCompleted {
		t.Fatalf("turn %+v", ev)
	}
}

// ask_user runs as the local MCP server `socrates mcp-ask` OpenCode starts
// from its config: listed with its instructions and a day's timeout, a call
// is an ask of the running turn whose answer goes back on the call, and the
// model gets §5.2.
func TestAskUserIsALocalMCPServerThatReachesTheHost(t *testing.T) {
	h := newHostRig(t)
	h.launch("")
	cn := h.dial()
	cn.until("ready", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady })
	cn.send("m1", "/mcp")
	status := cn.until("the MCP status", func(ev agent.Event) bool {
		return ev.Type == agent.TypePart && ev.State == agent.PartDone && strings.HasPrefix(ev.Text, "socrates:")
	}).Text
	if !strings.Contains(status, "socrates: connected timeout=86400000 tools="+agenthost.MCPAskTool+" instructions=When you need") {
		t.Fatalf("status %q", status)
	}
	cn.until("its end", turnEnded)

	cn.send("m2", "/askuser")
	ask := cn.until("the ask", func(ev agent.Event) bool { return ev.Type == agent.TypeAsk })
	p := askuser.Decode(ask.Payload)
	if p == nil || len(p.Questions) != 2 || ask.TurnID == "" || ask.Via != "" {
		t.Fatalf("ask %+v", ask)
	}
	answer := json.RawMessage(`{"answers":{"db":{"selected":["sqlite"]},"name":{"other":"Atlas"}},"general_note":"thanks"}`)
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpAnswer, ClientID: "a1", AskID: ask.AskID, Answer: answer, Device: "phone"}); err != nil {
		t.Fatal(err)
	}
	delivered := cn.until("the delivery", func(ev agent.Event) bool { return ev.Type == agent.TypeAskDelivered })
	if delivered.Via != agent.ViaTool {
		t.Fatalf("delivered %+v\n%s", delivered, journalDump(t, h))
	}
	text := cn.until("the answer", func(ev agent.Event) bool {
		return ev.Type == agent.TypePart && ev.State == agent.PartDone && strings.HasPrefix(ev.Text, "ask_user returned:")
	}).Text
	if !strings.Contains(text, `{"answers":{"db":{"selected":["sqlite"]`) || !strings.Contains(text, `"general_note":"thanks"`) {
		t.Fatalf("the model got %q", text)
	}
	if ev := cn.until("its end", turnEnded); ev.Outcome != agent.OutcomeCompleted {
		t.Fatalf("turn %+v", ev)
	}
}

// An abort withdraws the call: the mcp-ask process lets go of its question,
// the ask stays open, and its answer reaches the agent as a message.
func TestAnAbortedAskUserCallIsAnsweredAsAMessage(t *testing.T) {
	h := newHostRig(t)
	h.launch("")
	cn := h.dial()
	cn.send("m1", "/askuser")
	ask := cn.until("the ask", func(ev agent.Event) bool { return ev.Type == agent.TypeAsk })
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpInterrupt, ClientID: "i1"}); err != nil {
		t.Fatal(err)
	}
	if ev := cn.until("the turn's end", turnEnded); ev.Outcome != agent.OutcomeInterrupted {
		t.Fatalf("turn %+v", ev)
	}
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpAnswer, ClientID: "a1", AskID: ask.AskID,
		Answer: json.RawMessage(`{"answers":{"db":{"selected":["pg"]}}}`)}); err != nil {
		t.Fatal(err)
	}
	if ev := cn.until("the delivery", func(ev agent.Event) bool { return ev.Type == agent.TypeAskDelivered }); ev.Via != agent.ViaMessage {
		t.Fatalf("delivered %+v", ev)
	}
	cn.until("the notice", func(ev agent.Event) bool {
		return ev.Type == agent.TypeNotice && ev.Code == agent.NoticeAnswerAsMessage
	})
	reply := cn.until("the answer's turn", func(ev agent.Event) bool {
		return ev.Type == agent.TypePart && ev.State == agent.PartDone && strings.HasPrefix(ev.Text, "you said: Answer to your earlier question (Setup)")
	})
	if !strings.Contains(reply.Text, "PostgreSQL [pg]") {
		t.Fatalf("the agent got %q", reply.Text)
	}
}

// A chat moves A -> B -> A through a real host: the adapter switches the
// server itself (agent.AccountSwitcher), the session stays, the divider comes
// before anything runs on the new account, and the key of an API-key account
// arrives sealed and is opened by the host.
func TestASwitchThroughTheHostKeepsTheSession(t *testing.T) {
	h := newHostRig(t)
	key, err := vault.LoadOrCreateKeyFile(paths.New(h.r.data).SecretsKey())
	if err != nil {
		t.Fatal(err)
	}
	h.launch("")
	cn := h.dial()
	sid := cn.until("the conversation", func(ev agent.Event) bool { return ev.Type == agent.TypeConversation }).ConversationID
	cn.send("m1", "/whoami")
	cn.until("the first turn", turnEnded)
	sealed, err := agenthost.SealEnv(key, h.chat, []string{`OPENCODE_AUTH_CONTENT={"fake":{"type":"api","key":"KEY-B"}}`})
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.Event
	collect := func(what string, ok func(agent.Event) bool) agent.Event {
		ev := cn.until(what, func(ev agent.Event) bool {
			events = append(events, ev)
			return ok(ev)
		})
		return ev
	}
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: "s1", Account: "b", Env: h.r.env, SecretEnv: sealed}); err != nil {
		t.Fatal(err)
	}
	collect("the divider to b", func(ev agent.Event) bool { return ev.Type == agent.TypeAccountSwitched && ev.To == "b" })
	cn.send("m2", "/whoami")
	collect("the turn on b", turnEnded)
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: "s2", Account: "a", Env: h.r.env}); err != nil {
		t.Fatal(err)
	}
	collect("the divider to a", func(ev agent.Event) bool { return ev.Type == agent.TypeAccountSwitched && ev.To == "a" })
	cn.send("m3", "/whoami")
	collect("the turn on a", turnEnded)
	journal, err := agent.ReadJournal(agenthost.JournalPath(h.r.data, h.chat))
	if err != nil {
		t.Fatal(err)
	}
	p := agent.Fold(journal)
	if p.ConversationID != sid {
		t.Fatalf("the session changed: %s -> %s", sid, p.ConversationID)
	}
	var replies, accounts []string
	for _, ev := range journal {
		if ev.Type == agent.TypePart && ev.Kind == agent.PartText && ev.State == agent.PartDone && strings.HasPrefix(ev.Text, "account: ") {
			replies = append(replies, ev.Text)
		}
		if ev.Type == agent.TypeTurnStarted {
			accounts = append(accounts, ev.Account)
		}
	}
	if len(replies) != 3 || replies[1] != "account: fake:KEY-B (env)" || replies[2] != "account: none" {
		t.Fatalf("the CLI ran as %q", replies)
	}
	if strings.Join(accounts, ",") != ",b,a" {
		t.Fatalf("the turns ran on %v", accounts)
	}
	raw, _ := os.ReadFile(agenthost.ChatDir(h.r.data, h.chat) + "/host.json")
	if strings.Contains(string(raw), "KEY-B") {
		t.Fatal("host.json holds the key")
	}
}

// A switch after the turn, asked for while a limit holds the queue, is made:
// what the limit holds waits for the switch, not the switch for it - and
// then runs on the new account.
func TestASwitchAfterTheTurnIsMadeWhileALimitHoldsTheQueue(t *testing.T) {
	h := newHostRig(t)
	h.launch("")
	cn := h.dial()
	cn.until("the conversation", func(ev agent.Event) bool { return ev.Type == agent.TypeConversation })
	cn.send("m1", "/limit")
	cn.send("m2", "/whoami")
	if ev := cn.until("the limited turn", turnEnded); ev.Outcome != agent.OutcomeLimited {
		t.Fatalf("turn %+v", ev)
	}
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: "s1", Account: "b", Env: h.r.env,
		When: agenthost.WhenAfterTurn}); err != nil {
		t.Fatal(err)
	}
	cn.until("the divider to b", func(ev agent.Event) bool { return ev.Type == agent.TypeAccountSwitched && ev.To == "b" })
	started := cn.until("the held message's turn", func(ev agent.Event) bool { return ev.Type == agent.TypeTurnStarted })
	if started.Account != "b" {
		t.Fatalf("the held message ran on %q", started.Account)
	}
}
