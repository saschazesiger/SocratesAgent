package agenthost

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

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

func TestMain(m *testing.M) {
	// The manager starts hosts by re-executing the Socrates binary. Under test
	// that binary is this test binary, so it has to answer to the same
	// subcommand - and the test adapter is registered by this package's init,
	// so the host process finds it.
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		dir := ""
		for i := 2; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--dir" || os.Args[i] == "-dir" {
				dir = os.Args[i+1]
			}
		}
		if err := RunHost(dir); err != nil {
			fmt.Fprintln(os.Stderr, "host:", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

// newManager builds a manager whose agent host is this test binary. The socket
// directory is moved into the test's own temp dir so a failed run leaves
// nothing behind in XDG_RUNTIME_DIR.
func newManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	root := filepath.Join(t.TempDir(), "agents")
	m, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { m.Detach(); reapHosts(t, root) })
	return m
}

// reapHosts ends every agent-host process serving a directory under root.
// Detach leaves hosts running on purpose, which is right for a shutdown and
// wrong for a test that has finished with them.
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

func script(t *testing.T, steps ...step) string {
	t.Helper()
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// testSpec is a Spec for the in-package test adapter, carrying its script.
func testSpec(t *testing.T, steps ...step) harness.Spec {
	t.Helper()
	return harness.Spec{
		Agent: "test", Model: "scripted", Cwd: t.TempDir(),
		Env: []string{testScriptEnv + "=" + script(t, steps...)},
	}
}

// openTest starts a scripted session through a manager.
func openTest(t *testing.T, m *Manager, chatID string, steps ...step) *Handle {
	t.Helper()
	h, err := m.Open(t.Context(), chatID, testSpec(t, steps...))
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background(), h.ID(), 2*time.Second) })
	return h
}

// collect drains a subscription until the turn ends, or the test times out.
func collect(t *testing.T, frames <-chan Frame, limit time.Duration) []harness.Event {
	t.Helper()
	var out []harness.Event
	deadline := time.After(limit)
	for {
		select {
		case f, open := <-frames:
			if !open {
				return out
			}
			if f.Event == nil {
				continue
			}
			out = append(out, *f.Event)
			if f.Event.Kind == harness.KindTurnFinished {
				return out
			}
		case <-deadline:
			t.Fatalf("the turn did not end within %s, got %d events", limit, len(out))
		}
	}
}

func TestManagerDrivesASessionThroughTheHost(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat1",
		step{Do: "text", Text: "Looking at it."},
		step{Do: "tool", Name: "Bash", Input: "go test ./...", Output: "ok\n"},
		step{Do: "text", Text: "All tests pass."},
		step{Do: "end", Outcome: "ok"})

	seq, err := h.Send(t.Context(), "run_1", "please run the tests")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	frames, unsubscribe := h.Subscribe(seq)
	defer unsubscribe()
	events := collect(t, frames, 15*time.Second)

	kinds := map[string]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
		if ev.Seq <= seq {
			t.Errorf("an event of this turn carries seq %d, at or below the floor %d", ev.Seq, seq)
		}
	}
	if kinds[harness.KindTurnStarted] != 1 || kinds[harness.KindTurnFinished] != 1 {
		t.Errorf("a turn must start and end exactly once: %#v", kinds)
	}
	if kinds[harness.KindToolStarted] != 1 || kinds[harness.KindToolFinished] != 1 {
		t.Errorf("the tool call did not arrive: %#v", kinds)
	}
	if kinds[harness.KindText] != 2 {
		t.Errorf("expected two completed text blocks: %#v", kinds)
	}
	if got := events[len(events)-1].Outcome; got != harness.OutcomeOK {
		t.Errorf("outcome = %q", got)
	}

	// The seqs the journal handed out are strictly increasing with no gap.
	for i := 1; i < len(events); i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("journal seqs jumped from %d to %d", events[i-1].Seq, events[i].Seq)
		}
	}
}

// The native session id has to reach spec.json, or a host restarted by hand -
// or an adapter restarted after a crash - would start a fresh conversation.
func TestTheSessionIDIsRecordedOnDisk(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_session", step{Do: "end", Outcome: "ok"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if spec, ok := readSpec(h.Dir()); ok && spec.Spec.SessionID != "" {
			if spec.Spec.SessionID != "sess_chat_session" {
				t.Fatalf("session id = %q", spec.Spec.SessionID)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the session id never reached spec.json")
}

func TestManagerListsAndScopesByChat(t *testing.T) {
	m := newManager(t)
	openTest(t, m, "chat1", step{Do: "hang"})
	openTest(t, m, "chat2", step{Do: "hang"})

	if got := len(m.List("chat1")); got != 1 {
		t.Errorf("chat1 has %d sessions, want 1", got)
	}
	if got := len(m.List("")); got != 2 {
		t.Errorf("all chats have %d sessions, want 2", got)
	}
	for _, h := range m.List("chat2") {
		if h.ChatID() != "chat2" {
			t.Errorf("List returned a session of %q", h.ChatID())
		}
		if h.Status().Agent != "test" {
			t.Errorf("status agent = %q", h.Status().Agent)
		}
	}
}

// Reaching this refusal means the engine and the manager disagree about what
// is running, which is a bug rather than a limit anybody hits.
func TestManagerEnforcesTheHostLimit(t *testing.T) {
	m := newManager(t)
	for i := 0; i < MaxHostsPerChat; i++ {
		if _, err := m.Open(t.Context(), "busy", testSpec(t, step{Do: "hang"})); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	t.Cleanup(func() { m.CloseChat(context.Background(), "busy") })
	_, err := m.Open(t.Context(), "busy", testSpec(t, step{Do: "hang"}))
	if err == nil {
		t.Fatal("opening past the limit should fail")
	}
	if !strings.Contains(err.Error(), "bug") {
		t.Errorf("the refusal should read like the bug it is, got: %v", err)
	}
}

func TestOpenReportsAnUnknownAgent(t *testing.T) {
	m := newManager(t)
	_, err := m.Open(t.Context(), "chat1", harness.Spec{Agent: "nothing-like-this", Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no adapter") {
		t.Fatalf("an unknown agent should be refused by name, got %v", err)
	}
}

func TestManagerCloseChat(t *testing.T) {
	m := newManager(t)
	openTest(t, m, "doomed", step{Do: "hang"})
	openTest(t, m, "kept", step{Do: "hang"})

	m.CloseChat(t.Context(), "doomed")
	if got := len(m.List("doomed")); got != 0 {
		t.Errorf("doomed chat still has %d sessions", got)
	}
	if got := len(m.List("kept")); got != 1 {
		t.Error("the other chat lost its session")
	}
}

// This is the point of running sessions in their own host process: Socrates
// can go away and come back to find the turn still running, and the journal it
// wrote while nobody was listening still there to replay.
func TestSessionSurvivesARestartOfSocrates(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	root := filepath.Join(t.TempDir(), "agents")
	t.Cleanup(func() { reapHosts(t, root) })

	first, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	h, err := first.Open(t.Context(), "chat1", testSpec(t,
		step{Do: "text", Text: "before the restart"},
		step{Do: "sleep", MS: 400},
		step{Do: "text", Text: "after the restart"},
		step{Do: "end", Outcome: "ok"}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := h.ID()
	seq, err := h.Send(t.Context(), "run_1", "do the work")
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Socrates goes away without touching the running session.
	first.Detach()

	second, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("second manager: %v", err)
	}
	t.Cleanup(func() { second.CloseChat(context.Background(), "chat1") })
	if restored := second.Restore(t.Context()); restored != 1 {
		t.Fatalf("reconnected to %d sessions, want 1", restored)
	}
	again, ok := second.Get(filepath.Join(root, id))
	if !ok {
		t.Fatalf("session %s was not restored", id)
	}
	if !again.Alive() || !again.Working() {
		t.Fatal("the restored session is not running any more")
	}

	// Everything the turn wrote while nobody was listening is still there.
	frames, unsubscribe := again.Subscribe(seq)
	defer unsubscribe()
	events := collect(t, frames, 15*time.Second)
	var texts []string
	for _, ev := range events {
		if ev.Kind == harness.KindText {
			texts = append(texts, ev.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "before the restart" || texts[1] != "after the restart" {
		t.Fatalf("the replay lost part of the turn: %#v", texts)
	}
	if events[len(events)-1].Kind != harness.KindTurnFinished {
		t.Fatal("the restored subscription never saw the turn end")
	}
}

// A host whose adapter died is still this chat's host: it holds the session
// id, it answers, and the next send restarts the CLI inside it with resume.
// Dropping it on Restore would leave a process nobody tracks, nobody prunes
// and nobody can close - one leaked host per crash, forever.
func TestRestoreKeepsAHostWhoseAdapterDied(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	root := filepath.Join(t.TempDir(), "agents")
	t.Cleanup(func() { reapHosts(t, root) })
	first, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	h, err := first.Open(t.Context(), "chat1", testSpec(t, step{Do: "die"}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := h.ID()
	if _, err := h.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool { return !h.Working() })
	if !h.Alive() {
		t.Fatal("the host stopped answering when its adapter died")
	}
	first.Detach()

	second, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("second manager: %v", err)
	}
	t.Cleanup(func() { second.CloseChat(context.Background(), "chat1") })
	if restored := second.Restore(t.Context()); restored != 1 {
		t.Fatalf("reconnected to %d hosts, want 1", restored)
	}
	again, ok := second.Get(filepath.Join(root, id))
	if !ok {
		t.Fatal("the host whose adapter died was not restored")
	}
	if !again.Alive() {
		t.Fatal("the restored host does not answer")
	}
	if again.Working() {
		t.Fatal("the adapter is reported as running although it died")
	}

	// Pruning must not remove a directory from under a living process.
	second.Prune()
	if _, err := os.Stat(filepath.Join(root, id, fileSpec)); err != nil {
		t.Fatalf("Prune removed the spec of a host that is still answering: %v", err)
	}

	// And the next send lands in the same host, with a notice saying so.
	floor, err := again.Send(t.Context(), "run_2", "again")
	if err != nil {
		t.Fatalf("the send that should have restarted the adapter failed: %v", err)
	}
	if hosts := second.List("chat1"); len(hosts) != 1 {
		t.Fatalf("a second host was opened for the chat: %d", len(hosts))
	}
	frames, unsubscribe := again.Subscribe(floor)
	defer unsubscribe()
	sawNotice := false
	for _, ev := range collect(t, frames, 15*time.Second) {
		if ev.Kind == harness.KindNotice && strings.Contains(ev.Error, "restarted") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Error("the restart was not announced in the transcript")
	}
}

// A host whose adapter died holds its chat's one slot, because it is the host
// that will answer the next message. Closing it - what archiving does - frees
// the slot.
func TestAHostWhoseAdapterDiedStillHoldsTheSlot(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "busy", step{Do: "die"})
	if _, err := h.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool { return !h.Working() })

	if _, err := m.Open(t.Context(), "busy", testSpec(t, step{Do: "hang"})); err == nil {
		t.Fatal("a second host was opened beside the one that is waiting to restart")
	}
	if err := m.Close(context.Background(), h.ID(), 2*time.Second); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := m.Open(t.Context(), "busy", testSpec(t, step{Do: "hang"})); err != nil {
		t.Fatalf("a chat whose host was closed should accept a new one: %v", err)
	}
	t.Cleanup(func() { m.CloseChat(context.Background(), "busy") })
}

// SIGTERM is one of the three ways a host ends. A host that merely closed its
// adapter and went on lingering with a live listener would make every
// `systemctl stop` wait for SIGKILL, and would let a later send restart an
// adapter inside a host that was told to stop.
func TestSigtermEndsTheHost(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_term", step{Do: "hang"})
	if _, err := h.Send(t.Context(), "run_1", "work"); err != nil {
		t.Fatalf("send: %v", err)
	}
	procs := hostProcesses(t, h.Dir())
	if len(procs) != 1 {
		t.Fatalf("expected one host process, got %v", procs)
	}
	if err := exec.Command("kill", "-TERM", procs[0]).Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && len(hostProcesses(t, h.Dir())) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if left := hostProcesses(t, h.Dir()); len(left) > 0 {
		for _, pid := range left {
			_ = exec.Command("kill", "-9", pid).Run()
		}
		t.Fatalf("the host was still running after SIGTERM: %v", left)
	}
}

// A plain status call - a Refresh from the API layer - must not inject a
// second caught-up frame into a live subscription. In adopt mode a stray one
// before the replay has reached the turn's end interrupts a healthy run.
func TestOnlyTheSubscribeReplyIsACaughtUpFrame(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_refresh", step{Do: "hang"})
	floor, err := h.Send(t.Context(), "run_1", "work")
	if err != nil {
		t.Fatal(err)
	}
	frames, unsubscribe := h.Subscribe(floor)
	defer unsubscribe()

	caught := 0
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f, open := <-frames:
			if !open {
				t.Fatal("the subscription ended early")
			}
			if f.CaughtUp != nil {
				caught++
				if caught == 1 {
					if _, err := h.Refresh(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
			}
		case <-deadline:
			if caught != 1 {
				t.Fatalf("%d caught-up frames for one subscribe", caught)
			}
			return
		}
	}
}

// Nothing may be left behind by a host that has been opened, subscribed to and
// closed - the read pump, the forwarder and the host process all go.
func TestNothingLeaksAcrossOpenAndClose(t *testing.T) {
	m := newManager(t)
	time.Sleep(200 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		h, err := m.Open(t.Context(), "chat_leak", testSpec(t, step{Do: "text", Text: "x"}, step{Do: "end"}))
		if err != nil {
			t.Fatal(err)
		}
		floor, _ := h.Send(t.Context(), "r", "go")
		frames, unsubscribe := h.Subscribe(floor)
		collect(t, frames, 15*time.Second)
		unsubscribe()
		if err := m.Close(t.Context(), h.ID(), 2*time.Second); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 10*time.Second, func() bool { return len(hostProcesses(t, h.Dir())) == 0 })
	}
	time.Sleep(time.Second)
	if after := runtime.NumGoroutine(); after > before+2 {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines leaked across open/close: %d -> %d\n%s", before, after, buf[:n])
	}
}

// hostProcesses lists the agent-host processes serving one directory.
func hostProcesses(t *testing.T, dir string) []string {
	t.Helper()
	out, _ := exec.Command("pgrep", "-f", "agent-host --dir "+dir).CombinedOutput()
	var pids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

// A socket path that cannot fit into sun_path has to be refused with a
// sentence naming the problem, not with an opaque listen error.
func TestSocketPathRefusesWhatWillNotFit(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), strings.Repeat("deep", 40)))
	if _, err := SocketPath("host_0123456789ab"); err == nil {
		t.Fatal("an over-long socket path was accepted")
	} else if !strings.Contains(err.Error(), "TMPDIR") {
		t.Errorf("the error should say what to do about it, got: %v", err)
	}

	// The other half needs a runtime directory that genuinely leaves room, and
	// it must not depend on how long TMPDIR happens to be on this machine: a
	// t.TempDir() under TMPDIR=/tmp/tmp.XXXXXXXXXX carries the test's own name
	// and lands one byte over the limit, which fails the assertion below for a
	// reason that has nothing to do with the code.
	short := shortDir(t)
	const id = "host_0123456789ab"
	if room := len(short) + len("/socrates/"+id+".sock"); room > maxSocketPath {
		t.Skipf("this machine has no directory short enough to test the accepting half: "+
			"%s would need %d bytes and the limit is %d", short, room, maxSocketPath)
	}
	t.Setenv("XDG_RUNTIME_DIR", short)
	path, err := SocketPath(id)
	if err != nil {
		t.Fatalf("a short path was refused: %v", err)
	}
	if !strings.HasPrefix(path, short) || !strings.HasSuffix(path, ".sock") {
		t.Errorf("socket path = %q", path)
	}
	if len(path) > maxSocketPath {
		t.Errorf("the accepted path is %d bytes, over the %d byte limit: %q", len(path), maxSocketPath, path)
	}
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

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the condition was still false after %s", limit)
}
