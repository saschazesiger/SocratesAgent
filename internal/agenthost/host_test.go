//go:build linux

package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// init and TestMain make the test binary stand in for `socrates agent-host`:
// the launcher runs os.Args[0] with that subcommand, exactly as the server
// runs the real binary.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		runtime.LockOSThread()
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		os.Exit(Main(os.Args[2:], "test"))
	}
	os.Setenv(agent.FakeEnv, "1")
	agent.EnableFake()
	os.Exit(m.Run())
}

// rig is one chat with a real host process.
type rig struct {
	t    *testing.T
	data string
	chat string
	l    *Launcher
	park time.Duration
}

func newRig(t *testing.T, park time.Duration) *rig {
	t.Helper()
	data := t.TempDir()
	r := &rig{t: t, data: data, chat: "c" + strconv.FormatInt(time.Now().UnixNano(), 16),
		l: &Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}, park: park}
	t.Cleanup(r.kill)
	return r
}

func (r *rig) launch() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := r.l.Launch(ctx, Spec{ChatID: r.chat, Harness: agent.FakeHarness, Workdir: r.data, ParkAfterMS: r.park.Milliseconds()})
	if err != nil {
		r.t.Fatalf("launch: %v", err)
	}
}

// kill ends whatever host is left, so no test leaves a process behind.
func (r *rig) kill() {
	c, err := Dial(context.Background(), SocketPath(r.data, r.chat), 1<<62)
	if err != nil {
		return
	}
	_ = c.Send(Command{Op: OpStop})
	c.Close()
	r.waitDead(5 * time.Second)
}

func (r *rig) waitDead(d time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if alive, _ := r.l.Alive(r.chat); !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("the host of %s is still alive after %s", r.chat, d)
}

// client is a connection with its frames read into a channel.
type client struct {
	t      *testing.T
	c      *Conn
	frames chan Frame
	events []agent.Event
	acks   map[string]Ack
}

func (r *rig) dial(since int64) *client {
	r.t.Helper()
	c, err := Dial(context.Background(), SocketPath(r.data, r.chat), since)
	if err != nil {
		r.t.Fatalf("dial: %v", err)
	}
	cl := &client{t: r.t, c: c, frames: make(chan Frame, 4096), acks: map[string]Ack{}}
	go func() {
		defer close(cl.frames)
		for {
			f, err := c.Next()
			if err != nil {
				return
			}
			cl.frames <- f
		}
	}()
	r.t.Cleanup(func() { c.Close() })
	return cl
}

// until reads frames until ok says so about an event, and fails at timeout.
func (cl *client) until(timeout time.Duration, ok func(agent.Event) bool) agent.Event {
	cl.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case f, open := <-cl.frames:
			if !open {
				cl.t.Fatalf("the connection closed while waiting (%d events)", len(cl.events))
			}
			if f.Ack != nil {
				cl.acks[f.Ack.ClientID] = *f.Ack
			}
			if f.Event != nil {
				cl.events = append(cl.events, *f.Event)
				if ok(*f.Event) {
					return *f.Event
				}
			}
		case <-deadline:
			cl.t.Fatalf("timed out after %d events", len(cl.events))
		}
	}
}

func (cl *client) ack(clientID string, timeout time.Duration) Ack {
	cl.t.Helper()
	deadline := time.After(timeout)
	for {
		if a, ok := cl.acks[clientID]; ok {
			return a
		}
		select {
		case f, open := <-cl.frames:
			if !open {
				cl.t.Fatal("the connection closed while waiting for an ack")
			}
			if f.Ack != nil {
				cl.acks[f.Ack.ClientID] = *f.Ack
			}
			if f.Fail != nil {
				cl.t.Fatalf("refused: %+v", f.Fail)
			}
			if f.Event != nil {
				cl.events = append(cl.events, *f.Event)
			}
		case <-deadline:
			cl.t.Fatalf("no ack for %s", clientID)
		}
	}
}

func (cl *client) send(clientID, text string) {
	cl.t.Helper()
	if err := cl.c.Send(Command{Op: OpSend, ClientID: clientID, Text: text}); err != nil {
		cl.t.Fatal(err)
	}
}

func isType(typ string) func(agent.Event) bool {
	return func(ev agent.Event) bool { return ev.Type == typ }
}

func isProcess(state string) func(agent.Event) bool {
	return func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == state }
}

func (r *rig) journal() []agent.Event {
	r.t.Helper()
	events, err := agent.ReadJournal(JournalPath(r.data, r.chat))
	if err != nil {
		r.t.Fatal(err)
	}
	return events
}

func assertContiguous(t *testing.T, events []agent.Event, from int64) {
	t.Helper()
	for i, ev := range events {
		if ev.Seq != from+int64(i) {
			t.Fatalf("event %d has seq %d, want %d (gap or duplicate)", i, ev.Seq, from+int64(i))
		}
	}
}

func lastReply(events []agent.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := events[i]; ev.Type == agent.TypePart && ev.State == agent.PartDone {
			return ev.Text
		}
	}
	return ""
}

func TestHostRunsATurnAndActsOnAClientIDOnce(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "hello")
	first := cl.ack("m1", 5*time.Second)
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	delete(cl.acks, "m1")
	cl.send("m1", "hello")
	if again := cl.ack("m1", 5*time.Second); again.Seq != first.Seq {
		t.Fatalf("ack %+v then %+v", first, again)
	}
	if !cl.acks["m1"].Dup {
		t.Fatalf("the repeat was not a dup: %+v", cl.acks["m1"])
	}
	users := 0
	for _, ev := range r.journal() {
		if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("%d user messages in the journal", users)
	}
	if got := lastReply(r.journal()); !strings.HasPrefix(got, "you said: hello (turn 1") {
		t.Fatalf("reply %q", got)
	}
}

// A client that drops in the middle of a turn and comes back with since gets
// exactly the events it missed.
func TestHostReplaysExactlyWhatAClientMissed(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/stream 150 10")
	cl.until(5*time.Second, isType(agent.TypePartDelta))
	cl.c.Close()
	got := cl.events
	time.Sleep(300 * time.Millisecond)
	since := got[len(got)-1].Seq
	again := r.dial(since)
	again.until(10*time.Second, isType(agent.TypeTurnEnded))
	all := append(append([]agent.Event{}, got...), again.events...)
	assertContiguous(t, all, 1)
	journal := r.journal()
	if len(journal) < len(all) {
		t.Fatalf("journal %d, client %d", len(journal), len(all))
	}
	for i := range all {
		a, _ := json.Marshal(all[i])
		b, _ := json.Marshal(journal[i])
		if string(a) != string(b) {
			t.Fatalf("seq %d differs:\n%s\n%s", all[i].Seq, a, b)
		}
	}
	if got := agent.Fold(all).Part(lastPart(all)).Text; !strings.HasSuffix(got, "w150 ") {
		t.Fatalf("folded text ends %q", got[max(0, len(got)-20):])
	}
}

func lastPart(events []agent.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == agent.TypePart && events[i].Kind == agent.PartText {
			return events[i].PartID
		}
	}
	return ""
}

// A host killed in the middle of a turn takes its CLI child along (the
// parent-death signal); a new host records the turn as interrupted, says it
// resumed, and continues the same conversation.
func TestHostKilledMidTurnIsRecoveredAndResumes(t *testing.T) {
	r := newRig(t, time.Hour)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/stream 400 10")
	cl.until(5*time.Second, isType(agent.TypePartDelta))
	hostPID, cliPID := cl.c.Hello.HostPID, cl.c.Hello.CLIPID
	if cliPID == 0 || !processAlive(cliPID) {
		t.Fatalf("the fake CLI child is not running: %d", cliPID)
	}
	if err := syscall.Kill(hostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	r.waitDead(5 * time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(cliPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(cliPID) {
		t.Fatalf("the CLI child %d outlived its host", cliPID)
	}
	if err := r.l.Reap(context.Background(), r.chat); err != nil {
		t.Fatal(err)
	}
	conv := agent.Fold(r.journal()).ConversationID
	since := r.journal()[len(r.journal())-1].Seq
	r.launch()
	next := r.dial(since)
	next.until(5*time.Second, isProcess(agent.ProcessReady))
	p := agent.Fold(r.journal())
	if p.Turn != nil || p.LastTurn == nil || p.LastTurn.Outcome != agent.OutcomeInterrupted {
		t.Fatalf("the killed turn: %+v %+v", p.Turn, p.LastTurn)
	}
	resumed := false
	for _, ev := range r.journal() {
		resumed = resumed || (ev.Type == agent.TypeNotice && ev.Code == agent.NoticeResumed)
	}
	if !resumed || p.ConversationID != conv {
		t.Fatalf("resumed notice %v, conversation %s then %s", resumed, conv, p.ConversationID)
	}
	next.send("m2", "again")
	next.until(5*time.Second, isType(agent.TypeTurnEnded))
	if got := lastReply(r.journal()); !strings.Contains(got, "(turn 2 of "+conv) {
		t.Fatalf("the resumed conversation answered %q", got)
	}
	// The dedupe set is rebuilt from the journal: the new host knows m1.
	next.send("m1", "/stream 400 10")
	if a := next.ack("m1", 5*time.Second); !a.Dup {
		t.Fatalf("m1 again on a new host: %+v", a)
	}
}

// An idle host parks after its time, leaves no process behind, and the next
// host resumes the conversation.
func TestHostParksWhenIdleAndTheNextOneResumes(t *testing.T) {
	r := newRig(t, 400*time.Millisecond)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "one")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.until(5*time.Second, isProcess(agent.ProcessParked))
	r.waitDead(5 * time.Second)
	if _, err := os.Stat(SocketPath(r.data, r.chat)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket is still there: %v", err)
	}
	r.launch()
	next := r.dial(r.journal()[len(r.journal())-1].Seq)
	next.send("m2", "two")
	next.until(5*time.Second, isType(agent.TypeTurnEnded))
	if got := lastReply(r.journal()); !strings.Contains(got, "(turn 2 of") {
		t.Fatalf("after the park: %q", got)
	}
}

// A running turn, an open question and a background task each keep a host
// from parking.
func TestHostDoesNotParkWhileBusy(t *testing.T) {
	r := newRig(t, 200*time.Millisecond)
	r.launch()
	cl := r.dial(0)
	cl.send("m1", "/ask")
	ask := cl.until(5*time.Second, isType(agent.TypeAsk))
	time.Sleep(800 * time.Millisecond)
	if alive, _ := r.l.Alive(r.chat); !alive {
		t.Fatal("a host parked with a question open")
	}
	if err := cl.c.Send(Command{Op: OpAnswer, ClientID: "a1", AskID: ask.AskID, Answer: json.RawMessage(`"a"`)}); err != nil {
		t.Fatal(err)
	}
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	cl.send("m2", "/bg 1")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	time.Sleep(600 * time.Millisecond)
	if alive, _ := r.l.Alive(r.chat); !alive {
		t.Fatal("a host parked with a background task running")
	}
	cl.until(5*time.Second, isProcess(agent.ProcessParked))
}

// The scope: the host runs in socrates-…-<chat>.scope, a tool process that
// escaped the CLI's process group keeps the scope alive after the host is
// killed, and Reap - which refuses while the host lives - ends it.
func TestHostInItsOwnScope(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create system scopes")
	}
	if out, err := runOut("systemctl", "is-system-running"); err != nil && !strings.Contains(out, "degraded") && !strings.Contains(out, "running") {
		t.Skipf("no systemd here: %s", out)
	}
	r := newRig(t, time.Hour)
	r.l.Scope = true
	r.l.UnitPrefix = "socrates-test-agents-"
	unit := r.l.Unit(r.chat) + ".scope"
	t.Cleanup(func() { _, _ = runOut("systemctl", "stop", unit) })
	r.launch()
	cl := r.dial(0)
	cgroup, _ := os.ReadFile("/proc/" + strconv.Itoa(cl.c.Hello.HostPID) + "/cgroup")
	if !strings.Contains(string(cgroup), unit) {
		t.Fatalf("the host is not in %s: %s", unit, cgroup)
	}
	if out, _ := runOut("systemctl", "show", "-p", "OOMPolicy", "--value", unit); strings.TrimSpace(out) != "continue" {
		t.Fatalf("OOMPolicy = %q", out)
	}
	if err := r.l.Reap(context.Background(), r.chat); !errors.Is(err, ErrAlive) {
		t.Fatalf("Reap of a live host: %v", err)
	}
	cl.send("m1", "/orphan")
	cl.until(5*time.Second, isType(agent.TypeTurnEnded))
	raw, err := os.ReadFile(filepath.Join(ChatDir(r.data, r.chat), stateDir, "fake", "orphan.pid"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, _ := strconv.Atoi(string(raw))
	_ = syscall.Kill(cl.c.Hello.HostPID, syscall.SIGKILL)
	r.waitDead(5 * time.Second)
	if out, _ := runOut("systemctl", "is-active", unit); strings.TrimSpace(out) != "active" {
		t.Fatalf("the scope should still hold the orphan: %s", out)
	}
	if err := r.l.Reap(context.Background(), r.chat); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(orphan) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(orphan) {
		t.Fatalf("the orphan %d survived the scope stop", orphan)
	}
	if out, _ := runOut("systemctl", "is-active", unit); strings.TrimSpace(out) == "active" {
		t.Fatalf("the scope is still active after Reap")
	}
}

func runOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
