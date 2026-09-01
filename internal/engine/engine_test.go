package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost/hosttest"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func TestMain(m *testing.M) {
	// The manager starts hosts by re-executing the Socrates binary; under test
	// that is this test binary, and hosttest's init has registered the scripted
	// adapter it will look up.
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		dir := ""
		for i := 2; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--dir" || os.Args[i] == "-dir" {
				dir = os.Args[i+1]
			}
		}
		if err := agenthost.RunHost(dir); err != nil {
			fmt.Fprintln(os.Stderr, "host:", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

type env struct {
	engine *Engine
	store  *store.Store
	hosts  *agenthost.Manager
	bus    *Bus
	root   string
}

// shortDir is a temp directory whose name does not carry the test's own name:
// a descriptive test name plus a socket file is enough to blow sun_path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sox")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	data := shortDir(t)
	st, err := store.Open(filepath.Join(data, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return newEnvOn(t, st, filepath.Join(data, "agents"))
}

// newEnvOn builds an engine over an existing database and host root, which is
// what a restart looks like from the inside.
func newEnvOn(t *testing.T, st *store.Store, root string) *env {
	t.Helper()
	hosts, err := agenthost.NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	settings := config.Default()
	settings.Agent.WorkspaceRoot = shortDir(t)
	bus := NewBus()
	e := New(st, bus, func() config.Settings { return settings }, hosts)
	t.Cleanup(func() { hosts.Detach() })
	return &env{engine: e, store: st, hosts: hosts, bus: bus, root: root}
}

func script(t *testing.T, steps ...hosttest.Step) string {
	t.Helper()
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// chat creates a chat bound to the scripted test adapter.
func (e *env) chat(t *testing.T, id string, steps ...hosttest.Step) *store.Chat {
	t.Helper()
	t.Setenv(hosttest.ScriptEnv, script(t, steps...))
	c := &store.Chat{ID: id, Agent: "test", Model: "scripted"}
	if err := e.store.CreateChat(c); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	return c
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, what)
}

func (e *env) runStatus(t *testing.T, id string) string {
	t.Helper()
	run, err := e.store.GetRun(id)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	return run.Status
}

// A happy turn: the tool call becomes a card, the text blocks become one
// assistant message, and the draft that showed the answer while it was being
// written is gone by the time it is a message.
func TestATurnBecomesStepsAndOneMessage(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c1",
		hosttest.Step{Do: "text", Text: "Looking at it."},
		hosttest.Step{Do: "tool", Name: "Bash", Input: "go test ./...", Output: "ok\n"},
		hosttest.Step{Do: "usage"},
		hosttest.Step{Do: "text", Text: "All tests pass."},
		hosttest.Step{Do: "end", Outcome: "ok"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "run the tests", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the run to finish", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		t.Fatalf("run status = %q", got)
	}

	msgs, _ := env.store.ListMessages(chat.ID)
	var answers []store.Message
	for _, m := range msgs {
		if m.Role == "assistant" {
			answers = append(answers, m)
		}
	}
	if len(answers) != 1 {
		t.Fatalf("expected exactly one assistant message, got %d", len(answers))
	}
	if !strings.Contains(answers[0].Content, "Looking at it.") || !strings.Contains(answers[0].Content, "All tests pass.") {
		t.Fatalf("the answer lost a block: %q", answers[0].Content)
	}

	steps, _ := env.store.ListSteps(chat.ID)
	kinds := map[string]int{}
	for _, s := range steps {
		kinds[s.Kind]++
	}
	if kinds[store.StepTool] != 1 {
		t.Errorf("expected one tool card: %#v", kinds)
	}
	if kinds[store.StepUsage] != 1 {
		t.Errorf("expected one usage row: %#v", kinds)
	}
	if kinds[store.StepDraft] != 0 {
		t.Error("the draft survived the end of the turn")
	}

	// The native session id reached the chat row, which is what a resume is
	// built on.
	fresh, _ := env.store.GetChat(chat.ID)
	if fresh.AgentSession == "" {
		t.Error("the session id was not recorded on the chat")
	}
	if fresh.HostDir == "" {
		t.Error("the host directory was not recorded on the chat")
	}
}

// A graceful shutdown must not interrupt the turn. Dropping the sockets closes
// every subscription, and a run loop falling through that would write
// "interrupted" milliseconds before the process exits - so on restart there is
// no active run, Adopt has nothing to claim, and a turn still working inside
// its host is orphaned with its answer in the journal.
func TestDetachLeavesTheRunRunning(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_detach", hosttest.Step{Do: "hang"})

	_, ch := env.bus.Subscribe(chat.ID)
	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "work forever", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the turn to reach the agent", func() bool {
		c, _ := env.store.GetChat(chat.ID)
		return c.HostDir != ""
	})
	// Drain the run event Start published, so what is counted below is only
	// what the shutdown produced.
	drain(ch, 500*time.Millisecond)

	env.engine.Detach()
	time.Sleep(400 * time.Millisecond)

	if got := env.runStatus(t, run.ID); got != store.RunRunning {
		t.Fatalf("a graceful shutdown ended the run as %q", got)
	}
	for _, ev := range drain(ch, 300*time.Millisecond) {
		if ev.Type == "run" {
			t.Fatalf("the shutdown published a run event: %#v", ev.Run)
		}
	}
}

// Start creates the run row, commits the user message and publishes the run
// before it opens a host - so a crash in that window leaves an active run, a
// live idle host and a journal with no turn_started for it. Adopting that must
// end it, not wait forever: the chat would be busy for good, every later send
// a 409 the Outbox retries without end, and Stop a no-op.
func TestAdoptOfANeverSentRunEndsInterrupted(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_never", hosttest.Step{Do: "hang"})

	// A host that is up and idle, exactly as the crash would have left it.
	h, err := env.hosts.Open(t.Context(), chat.ID, harness.Spec{
		Agent: "test", Model: "scripted", Cwd: t.TempDir(),
		Env: []string{hosttest.ScriptEnv + "=" + script(t, hosttest.Step{Do: "hang"})},
	})
	if err != nil {
		t.Fatalf("open host: %v", err)
	}
	if err := env.store.SetChatHost(chat.ID, h.Dir()); err != nil {
		t.Fatal(err)
	}
	// A run row with nothing in the journal that belongs to it.
	run := &store.Run{ID: "run_never", ChatID: chat.ID, Status: store.RunRunning}
	if err := env.store.CreateRun(run); err != nil {
		t.Fatal(err)
	}

	claimed := env.engine.Adopt(context.Background())
	if len(claimed) != 1 || claimed[0] != run.ID {
		t.Fatalf("Adopt claimed %#v", claimed)
	}
	waitFor(t, 30*time.Second, "the adopted run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunInterrupted {
		t.Fatalf("run status = %q, want interrupted", got)
	}
	got, _ := env.store.GetRun(run.ID)
	if !strings.Contains(got.Error, "send it again") {
		t.Errorf("the run should say what to do about it, got %q", got.Error)
	}
	waitFor(t, 5*time.Second, "the chat to stop being busy", func() bool {
		return !env.engine.Busy(chat.ID)
	})
}

// Adopt replays the whole turn, because a turn that finished while Socrates
// was down has its answer sitting in the journal and nothing else will ever
// commit it. Applying an event twice therefore has to be a no-op with the same
// result: deterministic ids and upserts everywhere.
func TestAdoptReplaysAWholeTurnWithoutDuplicates(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_adopt",
		hosttest.Step{Do: "text", Text: "First."},
		hosttest.Step{Do: "tool", Name: "Bash", Input: "one", Output: "1\n"},
		hosttest.Step{Do: "text", Text: "Second."},
		hosttest.Step{Do: "tool", Name: "Bash", Input: "two", Output: "2\n"},
		hosttest.Step{Do: "usage"},
		hosttest.Step{Do: "text", Text: "Third."},
		hosttest.Step{Do: "end", Outcome: "ok"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the run to finish", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})

	// Put the run back the way a restart would find it and adopt it again,
	// against the same host and the same journal.
	if err := env.store.SetRunStatus(run.ID, store.RunRunning, ""); err != nil {
		t.Fatal(err)
	}
	claimed := env.engine.Adopt(context.Background())
	if len(claimed) != 1 {
		t.Fatalf("Adopt claimed %#v", claimed)
	}
	waitFor(t, 30*time.Second, "the replayed run to finish", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		t.Fatalf("the replayed run ended as %q", got)
	}

	msgs, _ := env.store.ListMessages(chat.ID)
	answers := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			answers++
			for _, want := range []string{"First.", "Second.", "Third."} {
				if strings.Count(m.Content, want) != 1 {
					t.Errorf("%q appears %d times in the answer: %q", want, strings.Count(m.Content, want), m.Content)
				}
			}
		}
	}
	if answers != 1 {
		t.Fatalf("the replay produced %d assistant messages", answers)
	}

	steps, _ := env.store.ListSteps(chat.ID)
	kinds := map[string]int{}
	ids := map[string]int{}
	for _, s := range steps {
		kinds[s.Kind]++
		ids[s.ID]++
	}
	if kinds[store.StepTool] != 2 {
		t.Errorf("expected exactly one card per tool id, got %#v", kinds)
	}
	if kinds[store.StepDraft] != 0 {
		t.Error("the replay left a draft behind")
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("step %s exists %d times", id, n)
		}
	}
}

// A crash inside the Open window leaves a live idle host whose directory the
// chat row never learned about. Resolving only by host_dir would find nothing,
// call Open, be refused by the one-host-per-chat limit and leave the chat
// permanently unable to send.
func TestSendAfterCrashInOpenReusesTheOrphanHost(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_orphan",
		hosttest.Step{Do: "text", Text: "hello"},
		hosttest.Step{Do: "end", Outcome: "ok"})

	h, err := env.hosts.Open(t.Context(), chat.ID, harness.Spec{
		Agent: "test", Model: "scripted", Cwd: t.TempDir(),
		Env: []string{hosttest.ScriptEnv + "=" + script(t,
			hosttest.Step{Do: "text", Text: "hello"},
			hosttest.Step{Do: "end", Outcome: "ok"})},
	})
	if err != nil {
		t.Fatalf("open host: %v", err)
	}
	// The chat row never learned about it, which is the whole point.
	if c, _ := env.store.GetChat(chat.ID); c.HostDir != "" {
		t.Fatalf("host_dir = %q, the test needs it empty", c.HostDir)
	}

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the run to finish", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		t.Fatalf("run status = %q", got)
	}
	if hosts := env.hosts.List(chat.ID); len(hosts) != 1 || hosts[0].ID() != h.ID() {
		t.Fatalf("a second host was opened beside the orphan: %#v", hosts)
	}
	fresh, _ := env.store.GetChat(chat.ID)
	if fresh.HostDir != h.Dir() {
		t.Errorf("the chat was not reconciled onto the orphan: %q", fresh.HostDir)
	}
}

// A connection dropped by the host's write deadline is a hiccup, not a lost
// turn: the engine redials once and replays from the same floor. Without this
// a thirty second stall would cost the user a turn; with it, thirty seconds.
func TestUnexpectedCloseResubscribes(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_hiccup",
		hosttest.Step{Do: "text", Text: "Working."},
		hosttest.Step{Do: "sleep", MS: 700},
		hosttest.Step{Do: "text", Text: "Finished."},
		hosttest.Step{Do: "end", Outcome: "ok"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the first block to arrive", func() bool {
		st, err := env.store.GetStep(run.ID + ":draft")
		return err == nil && strings.Contains(st.Body, "Working.")
	})

	// Drop the connection underneath the turn, exactly as the host's write
	// deadline would. The host and the turn inside it are untouched.
	hosts := env.hosts.List(chat.ID)
	if len(hosts) != 1 {
		t.Fatalf("expected one host, got %d", len(hosts))
	}
	hosts[0].Detach()

	waitFor(t, 30*time.Second, "the run to finish after the hiccup", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		t.Fatalf("a dropped connection cost the turn: run status = %q", got)
	}
	msgs, _ := env.store.ListMessages(chat.ID)
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "Finished.") {
			found = true
			if strings.Count(m.Content, "Working.") != 1 {
				t.Errorf("the replay duplicated a block: %q", m.Content)
			}
		}
	}
	if !found {
		t.Fatalf("the answer never arrived: %#v", msgs)
	}
}

// One turn per chat, and the refusal has to be the transient one: the browser
// retries a 409 until it succeeds, which is right for this and catastrophic
// for anything permanent.
func TestSecondSendWhileBusyIsRefusedAsBusy(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_busy", hosttest.Step{Do: "hang"})
	if _, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "one", ClientID: "k1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "two", ClientID: "k2"}); err != ErrBusy {
		t.Fatalf("second send = %v, want ErrBusy", err)
	}
	// The same key again is the phone retrying, and it gets the run that
	// already exists rather than a second one.
	again, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "one", ClientID: "k1"})
	if err != nil {
		t.Fatalf("a repeated send was refused: %v", err)
	}
	if again == nil || again.ChatID != chat.ID {
		t.Fatalf("run = %#v", again)
	}
}

// A message that arrives while Socrates is letting go of its hosts must wait
// for the restart, not start a second host beside a turn that is still
// running. The browser retries a 503, and client_id keeps it exactly once.
func TestSendWhileShuttingDownIsRefused(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_shutdown", hosttest.Step{Do: "end", Outcome: "ok"})
	env.engine.Detach()
	if _, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "late", ClientID: "k1"}); err != ErrShuttingDown {
		t.Fatalf("send during shutdown = %v, want ErrShuttingDown", err)
	}
	msgs, _ := env.store.ListMessages(chat.ID)
	if len(msgs) != 0 {
		t.Fatalf("the refused send wrote something: %#v", msgs)
	}
}

// A chat that predates the rewrite has no agent, and there is no CLI that saw
// the half of the conversation a different mechanism produced.
func TestALegacyChatHasNoAgent(t *testing.T) {
	env := newEnv(t)
	if err := env.store.CreateChat(&store.Chat{ID: "c_legacy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.engine.Start(Turn{ChatID: "c_legacy", Text: "hi", ClientID: "k1"}); err != ErrNoAgent {
		t.Fatalf("send to a legacy chat = %v, want ErrNoAgent", err)
	}
}

// The stub adapters return "not implemented yet" from Start, and that has to
// reach the transcript as a plain run error rather than a chat that hangs.
func TestAnAgentThatCannotStartFailsTheRun(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_broken", hosttest.Step{Do: "failstart"})
	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 60*time.Second, "the run to fail", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunFailed {
		t.Fatalf("run status = %q, want failed", got)
	}
	st, err := env.store.GetStep(run.ID + ":error")
	if err != nil {
		t.Fatalf("no error step: %v", err)
	}
	if st.Status != store.StatusFailed || strings.TrimSpace(st.Body) == "" {
		t.Fatalf("error step = %#v", st)
	}
}

// The browser treats "highest revision seen" as "everything up to here is
// mine", which is only true if delivery order is revision order. commitMu is
// what makes that true, and it is the whole of the offline replay story.
func TestEventsArriveInRevisionOrder(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_order",
		hosttest.Step{Do: "text", Text: "one"},
		hosttest.Step{Do: "tool", Name: "Bash", Input: "x", Output: "y\n"},
		hosttest.Step{Do: "text", Text: "two"},
		hosttest.Step{Do: "end", Outcome: "ok"})

	_, ch := env.bus.Subscribe(chat.ID)
	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the run to finish", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	events := drain(ch, time.Second)

	last := int64(0)
	seen := 0
	for _, ev := range events {
		var rev int64
		switch {
		case ev.Step != nil:
			rev = ev.Step.Rev
		case ev.Message != nil:
			rev = ev.Message.Rev
		default:
			continue
		}
		seen++
		if rev <= last {
			t.Fatalf("revision %d arrived after %d - a client would think it had everything", rev, last)
		}
		last = rev
	}
	if seen < 4 {
		t.Fatalf("only %d revisioned events arrived", seen)
	}
}

// drain reads what the bus has to say for a while and decodes it.
func drain(ch <-chan []byte, wait time.Duration) []Event {
	var out []Event
	deadline := time.After(wait)
	for {
		select {
		case raw, open := <-ch:
			if !open {
				return out
			}
			var ev Event
			if err := json.Unmarshal(raw, &ev); err == nil {
				out = append(out, ev)
			}
		case <-deadline:
			return out
		}
	}
}
