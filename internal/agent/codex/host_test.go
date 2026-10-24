//go:build linux

package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// The test binary stands in for `socrates agent-host` (TestMain), so the
// adapter runs inside a real host process here: spawned on its locked
// thread, with the parent-death signal.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		runtime.LockOSThread()
	}
}

type hostClient struct {
	t      *testing.T
	c      *agenthost.Conn
	events []agent.Event
}

func dialHost(t *testing.T, data, chat string, since int64) *hostClient {
	t.Helper()
	c, err := agenthost.Dial(context.Background(), agenthost.SocketPath(data, chat), since)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &hostClient{t: t, c: c}
}

// until reads frames until an event matches.
func (h *hostClient) until(what string, match func(agent.Event) bool) agent.Event {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		f, err := h.c.Next()
		if err != nil {
			h.t.Fatalf("waiting for %s: %v\n%s", what, err, dump(h.events))
		}
		if f.Event == nil {
			continue
		}
		h.events = append(h.events, *f.Event)
		if match(*f.Event) {
			return *f.Event
		}
	}
	h.t.Fatalf("timed out waiting for %s\n%s", what, dump(h.events))
	return agent.Event{}
}

func (h *hostClient) send(clientID, text string) {
	h.t.Helper()
	if err := h.c.Send(agenthost.Command{Op: agenthost.OpSend, ClientID: clientID, Text: text}); err != nil {
		h.t.Fatal(err)
	}
}

func TestThroughTheAgentHost(t *testing.T) {
	lab := newFakeLab(t)
	data := filepath.Join(lab.root, "data")
	chat := "codexhost"
	l := &agenthost.Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}
	spec := agenthost.Spec{ChatID: chat, Harness: Harness, Workdir: lab.work, Env: lab.spec("a", "").Env, ParkAfterMS: 60000}
	launch := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := l.Launch(ctx, spec); err != nil {
			t.Fatalf("launch: %v", err)
		}
	}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })

	launch()
	h := dialHost(t, data, chat, 0)
	ready := h.until("ready", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady })
	conv := agent.Fold(h.events).ConversationID
	h.send("c1", "hello")
	if end := h.until("the turn", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("turn %+v", end)
	}
	if got := lastTurnText(agent.Fold(h.events)); got != "you said: hello" {
		t.Fatalf("answer %q", got)
	}

	// A host killed mid-turn takes codex along; the next one resumes.
	h.send("c2", "/stream 500 10")
	h.until("streaming", isType(agent.TypePart))
	if err := syscall.Kill(h.c.Hello.HostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !processGone(ready.PID) {
		if time.Now().After(deadline) {
			t.Fatal("codex outlived its host")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for alive, _ := l.Alive(chat); alive; alive, _ = l.Alive(chat) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := l.Reap(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	last := h.events[len(h.events)-1].Seq
	launch()
	h2 := dialHost(t, data, chat, last)
	h2.until("the resumed notice", isNotice(agent.NoticeResumed))
	h2.until("ready", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady })
	if again := agent.Fold(h2.events).ConversationID; again != conv {
		t.Fatalf("resumed %s, want %s", again, conv)
	}
	h2.send("c3", "again")
	if end := h2.until("the turn", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("after resume %+v", end)
	}

	// Parking stops codex cleanly and ends the host.
	if err := h2.c.Send(agenthost.Command{Op: agenthost.OpPark, ClientID: "c4"}); err != nil {
		t.Fatal(err)
	}
	h2.until("parked", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessParked })
	journal, err := agent.ReadJournal(agenthost.JournalPath(data, chat))
	if err != nil {
		t.Fatal(err)
	}
	p := agent.Fold(journal)
	if p.ConversationID != conv || p.Turn != nil || len(p.Undelivered()) != 0 {
		t.Fatalf("journal after park: conv %s turn %+v undelivered %d", p.ConversationID, p.Turn, len(p.Undelivered()))
	}
}

// A chat moves A -> B -> A through a real host on one thread: each switch is
// a divider, every turn says its account, and the reasoning another account
// encrypted is dropped by the chain (REBUILD.md §4.3), so the turns go on.
func TestASwitchThroughTheHostRunsTheChain(t *testing.T) {
	lab := newFakeLab(t)
	data := filepath.Join(lab.root, "data")
	chat := "codexswitch"
	l := &agenthost.Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })
	envA, envB := lab.spec("a", "").Env, lab.spec("b", "").Env
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := l.Launch(ctx, agenthost.Spec{ChatID: chat, Harness: Harness, Workdir: lab.work, Account: "a", Env: envA,
		ParkAfterMS: 60000}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	h := dialHost(t, data, chat, 0)
	h.send("m1", "one")
	if end := h.until("the first turn", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("on A: %+v", end)
	}
	switchTo := func(clientID, account string, env []string) {
		t.Helper()
		if err := h.c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: clientID, Account: account, Env: env}); err != nil {
			t.Fatal(err)
		}
		h.until("the divider to "+account, func(ev agent.Event) bool {
			return ev.Type == agent.TypeAccountSwitched && ev.To == account
		})
	}
	switchTo("s1", "b", envB)
	h.send("m2", "two")
	if end := h.until("the turn on B", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("on B: %+v\n%s", end, dump(h.events))
	}
	stripped := false
	for _, ev := range h.events {
		stripped = stripped || (ev.Type == agent.TypeNotice && ev.Code == NoticeHistoryStripped)
	}
	if !stripped {
		t.Fatalf("A's reasoning was not dropped on B:\n%s", dump(h.events))
	}
	if got := lastTurnText(agent.Fold(h.events)); got != "you said: two" {
		t.Fatalf("on B: %q", got)
	}
	switchTo("s2", "a", envA)
	h.send("m3", "three")
	if end := h.until("the turn back on A", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		t.Fatalf("back on A: %+v\n%s", end, dump(h.events))
	}
	var accounts []string
	for _, ev := range h.events {
		if ev.Type == agent.TypeTurnStarted {
			accounts = append(accounts, ev.Account)
		}
	}
	if strings.Join(accounts, ",") != "a,b,a" {
		t.Fatalf("the turns ran on %v", accounts)
	}
	if got := lastTurnText(agent.Fold(h.events)); got != "you said: three" {
		t.Fatalf("back on A: %q", got)
	}
}

// reply reads frames until the host answers clientID, keeping the events that
// come before the answer.
func (h *hostClient) reply(clientID string) agenthost.Frame {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		f, err := h.c.Next()
		if err != nil {
			h.t.Fatalf("waiting for the answer to %s: %v\n%s", clientID, err, dump(h.events))
		}
		switch {
		case f.Event != nil:
			h.events = append(h.events, *f.Event)
		case f.Ack != nil && f.Ack.ClientID == clientID, f.Fail != nil && f.Fail.ClientID == clientID:
			return f
		}
	}
	h.t.Fatalf("timed out waiting for the answer to %s\n%s", clientID, dump(h.events))
	return agenthost.Frame{}
}

// holdBehindALimit runs a turn the account's limit ends, with a message the
// person wrote while it ran - steered into it and not taken up (fakecodex's
// "/limit steered" ends the turn when the steer comes), so the journal has
// it before the limited end whatever the timing - and returns that
// message's id: what the limit holds.
func (h *hostClient) holdBehindALimit() string {
	h.t.Helper()
	h.send("m1", "hello")
	if end := h.until("the first turn", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeCompleted {
		h.t.Fatalf("the first turn %+v", end)
	}
	h.send("m2", "/limit steered")
	h.until("the limited turn", isType(agent.TypeTurnStarted))
	h.send("m3", "written while the limited turn ran")
	held := h.until("the message", func(ev agent.Event) bool { return ev.Type == agent.TypeMessage && ev.ClientID == "m3" })
	if end := h.until("the limit", isType(agent.TypeTurnEnded)); end.Outcome != agent.OutcomeLimited {
		h.t.Fatalf("the turn ended %+v\n%s", end, dump(h.events))
	}
	p := agent.Fold(h.events)
	if m := p.Message(held.MessageID); m == nil || m.State == agent.MessageDelivered || m.AfterTurn == p.LastTurn.ID {
		h.t.Fatalf("the message is not one the limit holds: %+v\n%s", m, dump(h.events))
	}
	return held.MessageID
}

// ranSince reports whether a turn started for message id among events.
func ranSince(events []agent.Event, id string) bool {
	for _, ev := range events {
		if ev.Type == agent.TypeTurnStarted && ev.MessageID == id {
			return true
		}
	}
	return false
}

// goOn sends a message after the limit, which lets the hold go here: the held
// message runs first, on account, and then the person's.
func (h *hostClient) goOn(held, account string) {
	h.t.Helper()
	h.send("m4", "going on")
	started := h.until("the held message's turn", func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnStarted && ev.MessageID == held
	})
	if started.Account != account {
		h.t.Fatalf("the held message ran on %q, want %q", started.Account, account)
	}
	h.until("the person's message taken up", func(ev agent.Event) bool {
		if ev.Type != agent.TypeMessageState || ev.State != agent.MessageDelivered {
			return false
		}
		m := agent.Fold(h.events).Message(ev.MessageID)
		return m != nil && m.ClientID == "m4"
	})
}

// A switch to an account whose agent cannot start puts the chat back on the
// account it was on, at its limit. The host restarts the adapter there and
// hands it what was never taken up: a message written while the limited turn
// ran waits, as it did on the adapter before, instead of running into the
// same limit - the new adapter knows the hold from the journal
// (StartSpec.Held). The person's next message lets it go.
func TestAFailedSwitchKeepsWhatTheLimitHeld(t *testing.T) {
	lab := newFakeLab(t)
	data := filepath.Join(lab.root, "data")
	chat := "codexfailedswitch"
	l := &agenthost.Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := l.Launch(ctx, agenthost.Spec{ChatID: chat, Harness: Harness, Workdir: lab.work, Account: "a",
		Env: lab.spec("a", "").Env, ParkAfterMS: 60000}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	h := dialHost(t, data, chat, 0)
	held := h.holdBehindALimit()

	// Account b has no codex to start.
	envB := append(lab.spec("b", "").Env, BinEnv+"="+filepath.Join(lab.root, "no-codex"))
	if err := h.c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: "s1", Account: "b", Env: envB,
		Reason: "The limit of a was reached.", Continue: true}); err != nil {
		t.Fatal(err)
	}
	if f := h.reply("s1"); f.Fail == nil {
		t.Fatalf("the switch to an account without codex was made: %+v", f.Ack)
	}
	// The switch's answer comes after the host restarted a and handed it
	// what was owed.
	if ranSince(h.events, held) {
		t.Fatalf("the message written during the limited turn ran into the limit after the switch failed\n%s", dump(h.events))
	}
	p := agent.Fold(h.events)
	if p.Account != "a" || p.Process != agent.ProcessReady || messageState(p, held) != agent.MessageQueued {
		t.Fatalf("after the failed switch: account %s, process %s, the held message %q\n%s", p.Account, p.Process,
			messageState(p, held), dump(h.events))
	}
	h.goOn(held, "a")
}

// A host that dies while the limit holds a message is followed by one on the
// same account, started by a command that is not a message (a model change):
// boot hands the new adapter what was never taken up, and it waits as it did,
// until the person writes again.
func TestAHostThatComesBackKeepsWhatTheLimitHeld(t *testing.T) {
	lab := newFakeLab(t)
	data := filepath.Join(lab.root, "data")
	chat := "codexheldreboot"
	l := &agenthost.Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })
	spec := agenthost.Spec{ChatID: chat, Harness: Harness, Workdir: lab.work, Account: "a", Env: lab.spec("a", "").Env,
		ParkAfterMS: 60000}
	launch := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := l.Launch(ctx, spec); err != nil {
			t.Fatalf("launch: %v", err)
		}
	}
	launch()
	h := dialHost(t, data, chat, 0)
	held := h.holdBehindALimit()

	if err := syscall.Kill(h.c.Hello.HostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for alive, _ := l.Alive(chat); alive; alive, _ = l.Alive(chat) {
		if time.Now().After(deadline) {
			t.Fatal("the killed host is still alive")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := l.Reap(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	launch()
	h2 := dialHost(t, data, chat, 0)
	if err := h2.c.Send(agenthost.Command{Op: agenthost.OpSetModel, ClientID: "x1", Model: "gpt-fake", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	// The command waits for boot, which hands the adapter what is owed.
	if f := h2.reply("x1"); f.Ack == nil {
		t.Fatalf("the model change was refused: %+v", f.Fail)
	}
	if ranSince(h2.events, held) {
		t.Fatalf("the message written during the limited turn ran into the limit on the next host\n%s", dump(h2.events))
	}
	if p := agent.Fold(h2.events); p.Process != agent.ProcessReady || messageState(p, held) != agent.MessageQueued {
		t.Fatalf("on the next host: process %s, the held message %q\n%s", p.Process, messageState(p, held), dump(h2.events))
	}
	h2.goOn(held, "a")
}
