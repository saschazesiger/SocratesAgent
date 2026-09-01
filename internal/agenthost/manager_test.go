package agenthost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	m, err := NewManager(filepath.Join(t.TempDir(), "agents"), os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { m.Detach() })
	return m
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
	again, ok := second.ByID(id)
	if !ok {
		t.Fatalf("session %s was not restored", id)
	}
	if !again.Alive() {
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

func TestRestoreDropsHostsWhoseAdapterEnded(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	root := filepath.Join(t.TempDir(), "agents")
	first, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	h, err := first.Open(t.Context(), "chat1", testSpec(t, step{Do: "die"}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := h.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool { return !h.Alive() })
	first.Detach()

	second, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("second manager: %v", err)
	}
	t.Cleanup(second.Detach)
	if restored := second.Restore(t.Context()); restored != 0 {
		t.Errorf("reconnected to %d finished sessions, want 0", restored)
	}
}

// A chat whose host has finished must still be able to open a new one, or a
// crash would lock the conversation out for good.
func TestFinishedHostsDoNotCountAgainstTheLimit(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "busy", step{Do: "die"})
	if _, err := h.Send(t.Context(), "run_1", "go"); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool { return !h.Alive() })

	// The host is still there and still restartable, so it holds the slot; it
	// is the engine that reuses it. Close it the way archiving would and the
	// slot is free again.
	if err := m.Close(context.Background(), h.ID(), 2*time.Second); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := m.Open(t.Context(), "busy", testSpec(t, step{Do: "hang"})); err != nil {
		t.Fatalf("a chat whose host finished should accept a new one: %v", err)
	}
	t.Cleanup(func() { m.CloseChat(context.Background(), "busy") })
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

	short := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", short)
	path, err := SocketPath("host_0123456789ab")
	if err != nil {
		t.Fatalf("a short path was refused: %v", err)
	}
	if !strings.HasPrefix(path, short) || !strings.HasSuffix(path, ".sock") {
		t.Errorf("socket path = %q", path)
	}
}

// shortDir is a temp directory whose name does not carry the test's own name.
// t.TempDir() does, and a descriptive test name plus a socket file is enough
// to blow the sun_path limit the code under test is here to respect.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sox")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
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
