package term

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newManager builds a manager whose session host is this test binary.
func newManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "terminals"), os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { m.Detach() })
	return m
}

// openFake starts the fake CLI through a manager and waits for its prompt.
func openFake(t *testing.T, m *Manager, chatID string) *Handle {
	t.Helper()
	h, err := m.Open(t.Context(), chatID, "fake", fakeTUISpec(t.TempDir()))
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background(), h.ID(), 2*time.Second) })
	ok, _, err := h.WaitFor(t.Context(), `ready>`, 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("prompt never appeared (ok=%v err=%v)\nscreen:\n%s", ok, err, h.State().Screen)
	}
	return h
}

func TestManagerDrivesASessionThroughTheHost(t *testing.T) {
	m := newManager(t)
	h := openFake(t, m, "chat1")

	if err := h.Type(t.Context(), "hello\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	ok, state, err := h.WaitFor(t.Context(), `world`, 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("answer never appeared (ok=%v err=%v)\nscreen:\n%s", ok, err, state.Screen)
	}
	if !strings.Contains(state.Screen, "world") {
		t.Errorf("the screen returned by the host is missing the answer:\n%s", state.Screen)
	}

	out, err := h.Output(t.Context(), 0)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if !strings.Contains(out, "world") {
		t.Errorf("transcript is missing the answer:\n%s", out)
	}
}

func TestManagerSendKeysAndIdle(t *testing.T) {
	m := newManager(t)
	h := openFake(t, m, "chat1")

	if err := h.SendKeys(t.Context(), []string{"s", "l", "o", "w", "enter"}); err != nil {
		t.Fatalf("send keys: %v", err)
	}
	ok, state, err := h.WaitIdle(t.Context(), 700*time.Millisecond, 20*time.Second)
	if err != nil || !ok {
		t.Fatalf("wait idle: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(state.Screen, "finished") {
		t.Errorf("idle fired before the burst was over:\n%s", state.Screen)
	}
}

func TestManagerListsAndScopesByChat(t *testing.T) {
	m := newManager(t)
	openFake(t, m, "chat1")
	openFake(t, m, "chat2")

	if got := len(m.List("chat1")); got != 1 {
		t.Errorf("chat1 has %d sessions, want 1", got)
	}
	if got := len(m.List("")); got != 2 {
		t.Errorf("all chats have %d sessions, want 2", got)
	}
	for _, state := range m.States("chat2") {
		if state.ChatID != "chat2" {
			t.Errorf("States returned a session of %q", state.ChatID)
		}
		if state.Name != "fake" {
			t.Errorf("session name = %q, want fake", state.Name)
		}
	}
}

func TestManagerEnforcesTheSessionLimit(t *testing.T) {
	m := newManager(t)
	for i := 0; i < MaxSessionsPerChat; i++ {
		if _, err := m.Open(t.Context(), "busy", "fake", fakeTUISpec(t.TempDir())); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	t.Cleanup(func() { m.CloseChat(context.Background(), "busy") })
	if _, err := m.Open(t.Context(), "busy", "fake", fakeTUISpec(t.TempDir())); err == nil {
		t.Fatal("opening past the limit should fail")
	}
}

func TestManagerCloseChat(t *testing.T) {
	m := newManager(t)
	openFake(t, m, "doomed")
	openFake(t, m, "kept")

	m.CloseChat(t.Context(), "doomed")
	if got := len(m.List("doomed")); got != 0 {
		t.Errorf("doomed chat still has %d sessions", got)
	}
	if got := len(m.List("kept")); got != 1 {
		t.Errorf("the other chat lost its session")
	}
}

// This is the point of running sessions in their own host process: Socrates
// can go away and come back to find the program still running.
func TestSessionSurvivesARestartOfSocrates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "terminals")
	workdir := t.TempDir()

	first, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	h, err := first.Open(t.Context(), "chat1", "survivor", fakeTUISpec(workdir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := h.ID()
	if ok, _, _ := h.WaitFor(t.Context(), `ready>`, 10*time.Second); !ok {
		t.Fatalf("prompt never appeared:\n%s", h.State().Screen)
	}
	if err := h.Type(t.Context(), "before restart\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	if ok, _, _ := h.WaitFor(t.Context(), `you said: before restart`, 10*time.Second); !ok {
		t.Fatalf("echo never appeared:\n%s", h.State().Screen)
	}

	// Socrates goes away without touching the running programs.
	first.Detach()

	second, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("second manager: %v", err)
	}
	t.Cleanup(func() { second.CloseChat(context.Background(), "chat1") })
	if restored := second.Restore(t.Context()); restored != 1 {
		t.Fatalf("reconnected to %d sessions, want 1", restored)
	}
	again, ok := second.Get(id)
	if !ok {
		t.Fatalf("session %s was not restored", id)
	}
	if !again.Alive() {
		t.Fatal("the restored session is not running any more")
	}
	// The session must still hold its history and still accept input.
	if !strings.Contains(again.State().Screen, "you said: before restart") {
		t.Errorf("the restored screen lost its history:\n%s", again.State().Screen)
	}
	if err := again.Type(t.Context(), "after restart\r"); err != nil {
		t.Fatalf("type after restart: %v", err)
	}
	if ok, state, _ := again.WaitFor(t.Context(), `you said: after restart`, 10*time.Second); !ok {
		t.Fatalf("the restored session does not accept input:\n%s", state.Screen)
	}
}

func TestRestoreDropsSessionsWhoseProgramEnded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "terminals")
	first, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	h, err := first.Open(t.Context(), "chat1", "shortlived", fakeTUISpec(t.TempDir()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if ok, _, _ := h.WaitFor(t.Context(), `ready>`, 10*time.Second); !ok {
		t.Fatal("prompt never appeared")
	}
	if err := h.Type(t.Context(), "exit\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	// Wait for the host to write its final state and go away.
	deadline := time.Now().Add(10 * time.Second)
	for h.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.Alive() {
		t.Fatal("the session did not end")
	}
	first.Detach()

	second, err := NewManager(root, os.Args[0])
	if err != nil {
		t.Fatalf("second manager: %v", err)
	}
	if restored := second.Restore(t.Context()); restored != 0 {
		t.Errorf("reconnected to %d finished sessions, want 0", restored)
	}
	if final, ok := readFinal(filepath.Join(root, h.ID())); !ok {
		t.Error("the host left no record of how the session ended")
	} else if !strings.Contains(final.Output, "bye") {
		t.Errorf("the final transcript is missing the last output: %q", final.Output)
	}
}

func TestOpenReportsAMissingCommand(t *testing.T) {
	m := newManager(t)
	_, err := m.Open(t.Context(), "chat1", "broken", Spec{Command: "definitely-not-a-real-command-xyz"})
	if err == nil {
		t.Fatal("opening a missing command should fail")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("the error should explain that the command is missing, got: %v", err)
	}
}

func TestFinishedSessionsDoNotCountAgainstTheLimit(t *testing.T) {
	m := newManager(t)
	// Fill the chat, then let every one of them exit.
	for i := 0; i < MaxSessionsPerChat; i++ {
		h, err := m.Open(t.Context(), "busy", "fake", fakeTUISpec(t.TempDir()))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if ok, _, _ := h.WaitFor(t.Context(), `ready>`, 10*time.Second); !ok {
			t.Fatalf("session %d never started", i)
		}
		if err := h.Type(t.Context(), "exit\r"); err != nil {
			t.Fatalf("exit %d: %v", i, err)
		}
	}
	t.Cleanup(func() { m.CloseChat(context.Background(), "busy") })

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		alive := 0
		for _, h := range m.List("busy") {
			if h.Alive() {
				alive++
			}
		}
		if alive == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A chat full of finished sessions must still accept a new one.
	if _, err := m.Open(t.Context(), "busy", "fake", fakeTUISpec(t.TempDir())); err != nil {
		t.Fatalf("finished sessions should not block a new one: %v", err)
	}
}

func TestWaitChangeDetectsAReaction(t *testing.T) {
	m := newManager(t)
	h := openFake(t, m, "chat1")

	// Nothing is happening, so there is nothing to see.
	before := h.State()
	if _, reacted := h.WaitChange(t.Context(), before.Revision, 400*time.Millisecond); reacted {
		t.Error("reported a change while the program was idle")
	}

	// Typing makes the program echo, which is a change.
	before = h.State()
	if err := h.Type(t.Context(), "hello"); err != nil {
		t.Fatalf("type: %v", err)
	}
	state, reacted := h.WaitChange(t.Context(), before.Revision, 5*time.Second)
	if !reacted {
		t.Fatalf("typing produced no visible reaction\nscreen:\n%s", state.Screen)
	}
	if !strings.Contains(state.Screen, "hello") {
		t.Errorf("the typed text is not on the screen:\n%s", state.Screen)
	}
}
