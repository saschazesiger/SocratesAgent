//go:build linux

package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
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
	for !gone(ready.PID) {
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
