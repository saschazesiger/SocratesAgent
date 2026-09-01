package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// workspaceRoot is where a chat's own folder goes. Nothing creates that
	// folder but the engine, which is the thing the workspace tests check.
	workspaceRoot string
}

// shortDir is a temp directory whose path is short enough to leave room for a
// unix socket underneath it.
//
// t.TempDir() is not: it carries the test's own name, and it sits under
// TMPDIR, which on a build machine is often already most of the ~104 bytes
// sun_path allows. So this asks for the shortest base that works rather than
// whatever the environment happens to offer, and the tests below are then the
// same length whoever runs them.
func shortDir(t *testing.T) string {
	t.Helper()
	for _, base := range []string{"/tmp", ""} {
		dir, err := os.MkdirTemp(base, "s")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	t.Skip("no temp directory short enough to hold a unix socket")
	return ""
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
	root := filepath.Join(data, "agents")
	// Detach is what a shutdown does - it leaves the hosts running on purpose -
	// so a test that ends on one would leave a process behind. Every root
	// belongs to exactly one test, so reaping by root is exact.
	t.Cleanup(func() { reapHosts(t, root) })
	return newEnvOn(t, st, root)
}

// reapHosts ends every agent-host process serving a directory under root.
func reapHosts(t *testing.T, root string) {
	t.Helper()
	for _, pid := range hostProcesses(t, root) {
		_ = exec.Command("kill", "-TERM", pid).Run()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(hostProcesses(t, root)) > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range hostProcesses(t, root) {
		_ = exec.Command("kill", "-9", pid).Run()
	}
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
	workspaces := shortDir(t)
	settings.Agent.WorkspaceRoot = workspaces
	bus := NewBus()
	e := New(st, bus, func() config.Settings { return settings }, hosts)
	t.Cleanup(func() { hosts.Detach() })
	return &env{engine: e, store: st, hosts: hosts, bus: bus, root: root, workspaceRoot: workspaces}
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

	// The run row is written before the run goroutine has finished letting go
	// of the chat, and Adopt skips a chat that is still active - as it should,
	// since a real Adopt runs at startup with nothing running at all.
	waitFor(t, 30*time.Second, "the engine to let go of the chat", func() bool {
		return !env.engine.Busy(chat.ID)
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

// ---------------------------------------------------------------------------
// The cases the WP1 verification reproduced, kept as regression tests.

func hostProcesses(t *testing.T, root string) []string {
	t.Helper()
	out, _ := exec.Command("pgrep", "-f", "agent-host --dir "+root).CombinedOutput()
	var pids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

func hostDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// The adapter dies mid-turn; the next message must restart it with resume
// inside the SAME host. A host whose CLI died is still this chat's host - it
// holds the session - and treating it as gone opens a second host beside it
// and leaks the first, with nothing left that can ever close it.
func TestSecondSendAfterACrashStaysInTheSameHost(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_die", hosttest.Step{Do: "die"})

	first, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "one", ClientID: "k1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 30*time.Second, "the first run to end", func() bool {
		return env.runStatus(t, first.ID) != store.RunRunning
	})

	second, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "two", ClientID: "k2"})
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	waitFor(t, 30*time.Second, "the second run to end", func() bool {
		return env.runStatus(t, second.ID) != store.RunRunning
	})
	time.Sleep(300 * time.Millisecond)

	if procs := hostProcesses(t, env.root); len(procs) != 1 {
		for _, pid := range procs {
			_ = exec.Command("kill", "-9", pid).Run()
		}
		t.Fatalf("one chat ended up with %d host processes", len(procs))
	}
	if dirs := hostDirs(t, env.root); len(dirs) != 1 {
		t.Fatalf("one chat ended up with %d host directories: %v", len(dirs), dirs)
	}
	if hosts := env.hosts.List(chat.ID); len(hosts) != 1 {
		t.Fatalf("the manager tracks %d hosts for the chat", len(hosts))
	}
	// The second turn really ran: the restart is announced in its transcript.
	steps, _ := env.store.ListSteps(chat.ID)
	notice := false
	for _, st := range steps {
		if st.Kind == store.StepNotice && strings.Contains(st.Body, "restarted") {
			notice = true
		}
	}
	if !notice {
		for _, st := range steps {
			t.Logf("step %s kind=%s status=%s title=%q body=%q", st.ID, st.Kind, st.Status, st.Title, st.Body)
		}
		for _, h := range env.hosts.List(chat.ID) {
			raw, _ := os.ReadFile(filepath.Join(h.Dir(), "events.jsonl"))
			t.Logf("journal:\n%s", raw)
		}
		t.Error("the restart was not announced in the transcript")
	}
}

// A subscribe below the point rotation trimmed to cannot be answered. The run
// has to end with the sentence that says so - not spin redialling a host that
// is perfectly healthy and will refuse in exactly the same way every time.
//
// The rotated journal is written by hand rather than grown to 64 MiB: a host
// recovers its trim point from the first seq in the file, so a file that
// starts at seq 1000 is a rotated one as far as everything downstream is
// concerned, and the test costs milliseconds instead of a minute.
func TestAdoptOfARotatedJournalEndsWithTheReplayWindowSentence(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_rot", hosttest.Step{Do: "hang"})

	id := "host_rotated0001"
	dir := filepath.Join(env.root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock, err := agenthost.SocketPath(id)
	if err != nil {
		t.Fatal(err)
	}
	spec := agenthost.HostSpec{ID: id, Socket: sock, Created: time.Now().UnixMilli(), Spec: harness.Spec{
		Agent: "test", Model: "scripted", Cwd: t.TempDir(), ChatID: chat.ID, Dir: dir,
		Env: []string{hosttest.ScriptEnv + "=" + script(t, hosttest.Step{Do: "hang"})},
	}}
	raw, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var journal []byte
	for seq := 1000; seq < 1010; seq++ {
		journal = append(journal, []byte(fmt.Sprintf(
			`{"kind":"text_delta","seq":%d,"ts":1,"turn_id":"run_old","id":"t","text":"x"}`+"\n", seq))...)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "agent-host", "--dir", dir)
	logf, _ := os.Create(filepath.Join(dir, "host.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	waitFor(t, 30*time.Second, "the host to answer", func() bool {
		return env.hosts.Restore(ctx) == 1 || len(env.hosts.List(chat.ID)) == 1
	})
	if err := env.store.SetChatHost(chat.ID, dir); err != nil {
		t.Fatal(err)
	}
	// A turn that began below the point the journal was cut at.
	if err := env.store.SetChatHostSeq(chat.ID, 5); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_rot", ChatID: chat.ID, Status: store.RunRunning}
	if err := env.store.CreateRun(run); err != nil {
		t.Fatal(err)
	}

	if claimed := env.engine.Adopt(ctx); len(claimed) != 1 {
		t.Fatalf("Adopt claimed %#v", claimed)
	}
	waitFor(t, 30*time.Second, "the adopted run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	got, _ := env.store.GetRun(run.ID)
	if got.Status != store.RunInterrupted {
		t.Fatalf("run status = %q, want interrupted", got.Status)
	}
	if !strings.Contains(got.Error, "too long to replay") {
		t.Errorf("the run does not say what happened: %q", got.Error)
	}
	if st, err := env.store.GetStep(run.ID + ":error"); err != nil {
		t.Errorf("no error step: %v", err)
	} else if !strings.Contains(st.Body, "too long to replay") {
		t.Errorf("error step = %q", st.Body)
	}
}

// The watcher that turns Stop into an interrupt has to end with its turn. The
// run context is deliberately never cancelled on a normal end - cancelling it
// would fire an Interrupt round trip on every finished turn - so a watcher
// without an exit of its own is one goroutine per turn, forever.
func TestNoGoroutineIsLeftBehindPerTurn(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_leak", hosttest.Step{Do: "text", Text: "hi"}, hosttest.Step{Do: "end"})
	warm, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "warm", ClientID: "k0"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the warm-up turn", func() bool {
		return env.runStatus(t, warm.ID) != store.RunRunning
	})
	time.Sleep(500 * time.Millisecond)

	const turns = 8
	before := runtime.NumGoroutine()
	for i := 0; i < turns; i++ {
		run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: fmt.Sprintf("k%d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, 30*time.Second, "a turn", func() bool {
			return env.runStatus(t, run.ID) != store.RunRunning
		})
	}
	time.Sleep(time.Second)
	if after := runtime.NumGoroutine(); after-before >= turns {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("%d goroutines left over %d turns\n%s", after-before, turns, buf[:n])
	}
}

// A graceful restart in the middle of a turn: the answer is committed once,
// and the steps written after the adopt sort after the ones written before it.
// A counter that restarted at 1 would interleave them, and chat.js renders by
// seq - faithfully, out of order.
func TestGracefulRestartMidTurnKeepsTheTranscriptInOrder(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_restart",
		hosttest.Step{Do: "tool", Name: "Bash", Input: "first", Output: "1\n"},
		hosttest.Step{Do: "sleep", MS: 2500},
		hosttest.Step{Do: "tool", Name: "Bash", Input: "second", Output: "2\n"},
		hosttest.Step{Do: "text", Text: "Done."},
		hosttest.Step{Do: "end"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the first tool step", func() bool {
		_, err := env.store.GetStep(run.ID + ":tool:tool-0-0")
		return err == nil
	})
	env.engine.Detach()

	restarted := newEnvOn(t, env.store, env.root)
	ctx := context.Background()
	restarted.hosts.Restore(ctx)
	claimed := restarted.engine.Adopt(ctx)
	if len(claimed) != 1 {
		t.Fatalf("Adopt claimed %#v", claimed)
	}
	if err := env.store.RecoverRuns(claimed...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the adopted run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		t.Fatalf("run status = %q", got)
	}

	msgs, _ := env.store.ListMessages(chat.ID)
	answers := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("%d assistant messages after a graceful restart", answers)
	}

	steps, _ := env.store.ListSteps(chat.ID)
	var before, after *store.Step
	for i := range steps {
		switch steps[i].ID {
		case run.ID + ":tool:tool-0-0":
			before = &steps[i]
		case run.ID + ":tool:tool-2-0":
			after = &steps[i]
		}
	}
	if before == nil || after == nil {
		t.Fatalf("missing tool steps: %#v", steps)
	}
	if after.Seq <= before.Seq {
		t.Fatalf("a step created after the adopt has seq %d, not after %d - the transcript is reordered",
			after.Seq, before.Seq)
	}
	if before.Status != store.StatusDone {
		t.Errorf("the pre-restart step ended as %q", before.Status)
	}
}

// A turn that finished while Socrates was down has its whole answer sitting in
// the journal, and adopting it is the only thing that will ever commit it.
func TestATurnThatFinishedWhileDownIsStillCommitted(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_down",
		hosttest.Step{Do: "sleep", MS: 600},
		hosttest.Step{Do: "text", Text: "Alpha."},
		hosttest.Step{Do: "tool", Name: "Bash", Input: "x", Output: "y\n"},
		hosttest.Step{Do: "text", Text: "Omega."},
		hosttest.Step{Do: "end"})
	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	// The host having been recorded is not the same as the message having been
	// handed over: the chat row learns its host before the send. Wait for the
	// host itself to say it is working on this run.
	waitFor(t, 30*time.Second, "the turn to reach the agent", func() bool {
		for _, h := range env.hosts.List(chat.ID) {
			if h.Status().TurnID == run.ID {
				return true
			}
		}
		return false
	})
	env.engine.Detach()
	time.Sleep(2 * time.Second) // the turn finishes while we are "down"
	if got := env.runStatus(t, run.ID); got != store.RunRunning {
		t.Fatalf("the run should still say running while we are down, got %q", got)
	}

	restarted := newEnvOn(t, env.store, env.root)
	ctx := context.Background()
	restarted.hosts.Restore(ctx)
	claimed := restarted.engine.Adopt(ctx)
	_ = env.store.RecoverRuns(claimed...)
	waitFor(t, 30*time.Second, "the adopted run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	got, _ := env.store.GetRun(run.ID)
	if got.Status != store.RunDone {
		t.Fatalf("a turn that finished while Socrates was down ended as %q (%s)", got.Status, got.Error)
	}
	msgs, _ := env.store.ListMessages(chat.ID)
	answers := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			answers++
			if !strings.Contains(m.Content, "Alpha.") || !strings.Contains(m.Content, "Omega.") {
				t.Errorf("the answer lost a block: %q", m.Content)
			}
		}
	}
	if answers != 1 {
		t.Fatalf("%d assistant messages", answers)
	}
	steps, _ := env.store.ListSteps(chat.ID)
	for _, st := range steps {
		if st.Kind == store.StepDraft {
			t.Error("a draft step was left behind")
		}
	}
	if c, _ := env.store.GetChat(chat.ID); c.AgentSession == "" {
		t.Error("the session id was not recorded")
	}
}

// Stop while the host is still being opened is the user's own cancel, not a
// failure of theirs to see. "failed: context canceled" is neither true nor
// useful.
func TestStopBeforeTheTurnReachedTheAgentIsInterrupted(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_stop", hosttest.Step{Do: "hang"})
	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	env.engine.Stop(chat.ID)
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	got, _ := env.store.GetRun(run.ID)
	if got.Status != store.RunInterrupted {
		t.Fatalf("Stop ended the run as %q with %q", got.Status, got.Error)
	}
	if strings.Contains(got.Error, "context canceled") {
		t.Errorf("the run row carries a Go error: %q", got.Error)
	}
}

// A turn that ends badly must not leave a card spinning. An agent interrupted
// in the middle of a command sends no completion for the item it was running -
// Codex sends nothing at all for anything in flight after turn/interrupt - so
// the pump settles them itself, with the outcome of the turn they belonged to.
func TestAnInterruptedTurnSettlesItsOpenCards(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_open",
		hosttest.Step{Do: "tool", Name: "Bash", Input: "sleep 600", Open: true},
		hosttest.Step{Do: "hang"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	stepID := run.ID + ":tool:tool-0-0"
	waitFor(t, 30*time.Second, "the tool card to open", func() bool {
		st, err := env.store.GetStep(stepID)
		return err == nil && st.Status == store.StatusRunning
	})

	if !env.engine.Stop(chat.ID) {
		t.Fatal("Stop found nothing to stop")
	}
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunInterrupted {
		t.Fatalf("run status = %q, want interrupted", got)
	}
	st, err := env.store.GetStep(stepID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != store.StatusInterrupted {
		t.Fatalf("the tool card is still %q after the turn was interrupted", st.Status)
	}
}

// The same, for a turn that ends in an error: the cards settle as failed.
func TestAFailedTurnSettlesItsOpenCards(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_open_err",
		hosttest.Step{Do: "tool", Name: "Bash", Input: "build", Open: true},
		hosttest.Step{Do: "end", Outcome: "error", Error: "the model refused"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunFailed {
		t.Fatalf("run status = %q, want failed", got)
	}
	st, err := env.store.GetStep(run.ID + ":tool:tool-0-0")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != store.StatusFailed {
		t.Fatalf("the tool card is %q after the turn failed", st.Status)
	}
}

// And a turn that ends well leaves the cards exactly as the agent reported
// them - the settling must not overwrite a finished card.
func TestAGoodTurnLeavesItsCardsAlone(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_good",
		hosttest.Step{Do: "tool", Name: "Bash", Input: "go test", Output: "ok\n", Exit: 0},
		hosttest.Step{Do: "end", Outcome: "ok"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	st, err := env.store.GetStep(run.ID + ":tool:tool-0-0")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != store.StatusDone {
		t.Fatalf("a finished card ended as %q", st.Status)
	}
}

// A tool_finished is a patch, not a replacement. An adapter that does not
// repeat the name and the title it already sent must not blank the card: a row
// that loses its command the moment it completes is worse than one that never
// had it.
func TestATerseFinishKeepsWhatTheCardAlreadySaid(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_terse",
		hosttest.Step{Do: "tool", Name: "Bash", Input: "go build ./...", Output: "ok\n", Terse: true},
		hosttest.Step{Do: "end", Outcome: "ok"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	st, err := env.store.GetStep(run.ID + ":tool:tool-0-0")
	if err != nil {
		t.Fatal(err)
	}
	if st.Title != "Ran a command" {
		t.Errorf("the card lost its title: %q", st.Title)
	}
	if st.Status != store.StatusDone {
		t.Errorf("status = %q", st.Status)
	}
	detail := map[string]any{}
	if err := json.Unmarshal(st.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["name"] != "Bash" {
		t.Errorf("the card lost the tool's name: %#v", detail)
	}
	if detail["input"] != "go build ./..." {
		t.Errorf("the card lost the command: %#v", detail)
	}
	if st.Body != "ok\n" {
		t.Errorf("body = %q", st.Body)
	}
}

// The browser sends the next message the moment it sees the run turn done. A
// chat that is still registered as active at that instant answers ErrBusy for
// a turn that is already over, and the person is told to wait for something
// that has finished.
func TestASendTheInstantTheRunIsDoneIsAccepted(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_immediate",
		hosttest.Step{Do: "text", Text: "done."},
		hosttest.Step{Do: "end", Outcome: "ok"})

	_, ch := env.bus.Subscribe(chat.ID)
	first, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "one", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the run event that says the turn is over - the same signal the
	// browser acts on - and send again straight away.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case raw, open := <-ch:
			if !open {
				t.Fatal("the bus closed")
			}
			var ev Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			if ev.Type != "run" || ev.Run == nil || ev.Run.ID != first.ID {
				continue
			}
			if ev.Run.Status == store.RunRunning {
				continue
			}
			if _, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "two", ClientID: "k2"}); err != nil {
				t.Fatalf("a send on the heels of %q was refused: %v", ev.Run.Status, err)
			}
			return
		case <-deadline:
			t.Fatal("the run never reported that it was done")
		}
	}
}

// A connection that goes while a replay is streaming is still just a dropped
// connection: the turn behind it is healthy and the redial picks it back up.
// Treating it as a refusal - the host said no, do not ask again - interrupts a
// turn that was about to finish, and it is the likelier of the two moments for
// a socket to go, because a replay is when the wire is busiest.
func TestAConnectionLostDuringAReplayIsStillJustAHiccup(t *testing.T) {
	env := newEnv(t)
	// A long first turn, so the replay an adopt has to stream takes long
	// enough for the connection to be dropped in the middle of it.
	chat := env.chat(t, "c_midreplay",
		hosttest.Step{Do: "text", Text: "chatter", Count: 400},
		hosttest.Step{Do: "text", Text: "the answer"},
		hosttest.Step{Do: "end", Outcome: "ok"})

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the first turn to finish", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	waitFor(t, 30*time.Second, "the engine to let go of the chat", func() bool {
		return !env.engine.Busy(chat.ID)
	})

	// Put the run back the way a restart would find it, and drop the
	// connection while the adopt's replay of those hundreds of events is in
	// flight.
	if err := env.store.SetRunStatus(run.ID, store.RunRunning, ""); err != nil {
		t.Fatal(err)
	}
	hosts := env.hosts.List(chat.ID)
	if len(hosts) != 1 {
		t.Fatalf("expected one host, got %d", len(hosts))
	}
	go func() {
		time.Sleep(15 * time.Millisecond)
		hosts[0].Detach()
	}()

	if claimed := env.engine.Adopt(context.Background()); len(claimed) != 1 {
		t.Fatalf("Adopt claimed %#v", claimed)
	}
	waitFor(t, 60*time.Second, "the adopted run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	got, _ := env.store.GetRun(run.ID)
	if got.Status != store.RunDone {
		t.Fatalf("a connection lost mid-replay cost the turn: %q (%s)", got.Status, got.Error)
	}
	msgs, _ := env.store.ListMessages(chat.ID)
	answers := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			answers++
			if !strings.Contains(m.Content, "the answer") {
				t.Errorf("the answer lost its last block: %q", m.Content)
			}
		}
	}
	if answers != 1 {
		t.Fatalf("%d assistant messages", answers)
	}
}

// Letting go of the chat and saying the turn is over are one step. With a gap
// between them a Start that slips through publishes run{running} first, this
// one publishes run{done} on top of it, and the browser sits idle in front of
// a turn that is working.
func TestTheRunEventsNeverContradictEachOther(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t, "c_order2",
		hosttest.Step{Do: "text", Text: "ok"},
		hosttest.Step{Do: "end", Outcome: "ok"})

	_, ch := env.bus.Subscribe(chat.ID)
	first, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "one", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}

	// A caller doing exactly what the browser does, only faster: spin on the
	// chat being free and send the instant it is.
	sent := make(chan *store.Run, 1)
	go func() {
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if !env.engine.Busy(chat.ID) {
				run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "two", ClientID: "k2"})
				if err == nil {
					sent <- run
					return
				}
			}
		}
		sent <- nil
	}()

	var second *store.Run
	select {
	case second = <-sent:
	case <-time.After(30 * time.Second):
		t.Fatal("the second turn never started")
	}
	if second == nil {
		t.Fatal("the second turn was refused for a chat that was free")
	}
	waitFor(t, 30*time.Second, "both turns to finish", func() bool {
		return env.runStatus(t, second.ID) != store.RunRunning
	})

	// The last thing said about the first run must come before the first thing
	// said about the second: a browser replaying this stream is never left
	// idle in front of a turn that is working.
	firstDone, secondStarted := -1, -1
	for i, ev := range drain(ch, time.Second) {
		if ev.Type != "run" || ev.Run == nil {
			continue
		}
		if ev.Run.ID == first.ID && ev.Run.Status != store.RunRunning && firstDone < 0 {
			firstDone = i
		}
		if ev.Run.ID == second.ID && ev.Run.Status == store.RunRunning && secondStarted < 0 {
			secondStarted = i
		}
	}
	if firstDone < 0 || secondStarted < 0 {
		t.Fatalf("the run events did not both arrive (done at %d, started at %d)", firstDone, secondStarted)
	}
	if secondStarted < firstDone {
		t.Fatalf("the second turn was announced as running (%d) before the first was announced as over (%d) - "+
			"the browser would be left idle in front of a turn that is working", secondStarted, firstDone)
	}
}

// A chat works in its own folder under the workspace root, and something has
// to create it. Nothing did: the dashboard makes the root and stops there, so
// every real chat's first turn died inside exec with "no such file or
// directory" - which reads as a missing agent binary and sent whoever saw it
// looking for the wrong thing entirely. Only a real process can notice, which
// is why this test runs one.
func TestTheWorkspaceIsCreatedBeforeTheAgentRunsInIt(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t,
		"c_workspace",
		hosttest.Step{Do: "exec", Exec: "pwd"},
		hosttest.Step{Do: "end", Outcome: "ok"})

	want := filepath.Join(env.workspaceRoot, chat.ID)
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("the workspace already exists, so this test would prove nothing: %v", err)
	}

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		got, _ := env.store.GetRun(run.ID)
		t.Fatalf("the turn failed: %s (%s)", got.Status, got.Error)
	}

	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("the workspace was not created: %v", err)
	}
	st, err := env.store.GetStep(run.ID + ":tool:exec-0-0")
	if err != nil {
		t.Fatalf("the command left no card: %v", err)
	}
	if strings.TrimSpace(st.Body) != want {
		t.Fatalf("the agent ran in %q, want %q", strings.TrimSpace(st.Body), want)
	}
	if st.Status != store.StatusDone {
		t.Fatalf("the command failed: %#v", st)
	}
}

// A chat pinned to a directory of its own gets that one created too, which is
// the case a PATCH of the workspace makes and the one a person is most likely
// to type somewhere that does not exist yet.
func TestAPinnedWorkspaceIsCreatedToo(t *testing.T) {
	env := newEnv(t)
	chat := env.chat(t,
		"c_pinned",
		hosttest.Step{Do: "exec", Exec: "pwd"},
		hosttest.Step{Do: "end", Outcome: "ok"})

	pinned := filepath.Join(shortDir(t), "somewhere", "of", "its", "own")
	if err := env.store.UpdateChat(chat.ID, "", pinned); err != nil {
		t.Fatal(err)
	}

	run, err := env.engine.Start(Turn{ChatID: chat.ID, Text: "go", ClientID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the run to end", func() bool {
		return env.runStatus(t, run.ID) != store.RunRunning
	})
	if got := env.runStatus(t, run.ID); got != store.RunDone {
		got, _ := env.store.GetRun(run.ID)
		t.Fatalf("the turn failed: %s (%s)", got.Status, got.Error)
	}
	st, err := env.store.GetStep(run.ID + ":tool:exec-0-0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(st.Body) != pinned {
		t.Fatalf("the agent ran in %q, want the pinned %q", strings.TrimSpace(st.Body), pinned)
	}
}
