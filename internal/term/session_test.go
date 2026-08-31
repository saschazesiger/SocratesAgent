package term

import (
	"strings"
	"testing"
	"time"
)

func TestSessionTypesAndReadsTheScreen(t *testing.T) {
	s := startFake(t)

	if err := s.Type("hello\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	if ok, err := s.WaitFor(t.Context(), `world`, 5*time.Second); err != nil || !ok {
		t.Fatalf("answer never appeared (ok=%v err=%v)\nscreen:\n%s", ok, err, s.Screen())
	}
	screen := s.Screen()
	if !strings.Contains(screen, "Fake Agent") {
		t.Errorf("banner missing from the screen:\n%s", screen)
	}
	if strings.Contains(screen, "\x1b") {
		t.Errorf("the rendered screen still contains escape sequences:\n%q", screen)
	}
}

func TestSessionSendKeys(t *testing.T) {
	s := startFake(t)
	// "keys " then a literal escape sequence, submitted with the enter key.
	if err := s.SendKeys([]string{"k", "e", "y", "s", "space", "a", "enter"}); err != nil {
		t.Fatalf("send keys: %v", err)
	}
	if ok, _ := s.WaitFor(t.Context(), `got "a"`, 5*time.Second); !ok {
		t.Fatalf("keys did not arrive as typed\nscreen:\n%s", s.Screen())
	}
}

func TestSessionWaitIdle(t *testing.T) {
	s := startFake(t)
	start := time.Now()
	if err := s.Type("slow\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	// The program prints a line every 250ms for a second; a 600ms quiet
	// window must not fire in the middle of that burst.
	ok, err := s.WaitIdle(t.Context(), 600*time.Millisecond, 15*time.Second)
	if err != nil || !ok {
		t.Fatalf("wait idle: ok=%v err=%v", ok, err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("returned after %s, before the program had finished printing", elapsed)
	}
	if !strings.Contains(s.Screen(), "finished") {
		t.Errorf("returned before the last line was printed:\n%s", s.Screen())
	}
}

func TestSessionWaitIdleTimesOut(t *testing.T) {
	s := startFake(t)
	if err := s.Type("slow\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	// A quiet window longer than the whole burst, with a limit that expires
	// first, has to report failure rather than block.
	ok, err := s.WaitIdle(t.Context(), 5*time.Second, 400*time.Millisecond)
	if err != nil {
		t.Fatalf("wait idle: %v", err)
	}
	if ok {
		t.Error("reported the session idle while it was still printing")
	}
}

func TestSessionWaitForTimesOut(t *testing.T) {
	s := startFake(t)
	ok, err := s.WaitFor(t.Context(), `this never appears`, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("wait for: %v", err)
	}
	if ok {
		t.Error("matched a pattern that was never printed")
	}
}

func TestSessionRepaintIsRenderedNotAppended(t *testing.T) {
	s := startFake(t)
	if err := s.Type("repaint\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	if ok, _ := s.WaitFor(t.Context(), `2\. No`, 5*time.Second); !ok {
		t.Fatalf("menu never appeared\nscreen:\n%s", s.Screen())
	}
	screen := s.Screen()
	if strings.Contains(screen, "Fake Agent") {
		t.Errorf("the cleared banner is still on the screen:\n%s", screen)
	}
	if !strings.Contains(screen, "Allow this action?") {
		t.Errorf("question missing:\n%s", screen)
	}
}

func TestSessionJournalKeepsScrolledOutput(t *testing.T) {
	s := startFake(t)
	for _, word := range []string{"alpha", "bravo", "charlie"} {
		if err := s.Type(word + "\r"); err != nil {
			t.Fatalf("type: %v", err)
		}
		if ok, _ := s.WaitFor(t.Context(), "you said: "+word, 5*time.Second); !ok {
			t.Fatalf("%s was not echoed\nscreen:\n%s", word, s.Screen())
		}
	}
	out := s.Output(0)
	for _, word := range []string{"alpha", "bravo", "charlie"} {
		if !strings.Contains(out, "you said: "+word) {
			t.Errorf("transcript lost %q:\n%s", word, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("transcript still contains escape sequences:\n%q", out)
	}
}

func TestSessionJournalCollapsesProgressLines(t *testing.T) {
	s := startFake(t)
	if err := s.Type("progress\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	if ok, _ := s.WaitFor(t.Context(), `downloaded`, 5*time.Second); !ok {
		t.Fatalf("progress never finished\nscreen:\n%s", s.Screen())
	}
	out := s.Output(0)
	if strings.Contains(out, "downloading 25%") {
		t.Errorf("carriage returns were not applied, the transcript kept every step:\n%s", out)
	}
	if !strings.Contains(out, "downloaded") {
		t.Errorf("final state missing from the transcript:\n%s", out)
	}
}

func TestSessionRecordsExitCode(t *testing.T) {
	s := startFake(t)
	if err := s.Type("fail\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the program did not exit")
	}
	state := s.State()
	if state.Running {
		t.Error("session still reports itself as running")
	}
	if state.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", state.ExitCode)
	}
}

func TestSessionTypeAfterExit(t *testing.T) {
	s := startFake(t)
	if err := s.Type("exit\r"); err != nil {
		t.Fatalf("type: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the program did not exit")
	}
	if err := s.Type("anything\r"); err != ErrClosed {
		t.Errorf("Type after exit = %v, want ErrClosed", err)
	}
}

func TestSessionResize(t *testing.T) {
	if !HasPTY {
		t.Skip("resizing needs a real terminal")
	}
	s := startFake(t)
	if err := s.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	state := s.State()
	if state.Cols != 120 || state.Rows != 40 {
		t.Errorf("size = %dx%d, want 120x40", state.Cols, state.Rows)
	}
}

func TestSessionCloseIsGraceful(t *testing.T) {
	s := startFake(t)
	if err := s.Close(3 * time.Second); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.Running() {
		t.Error("session still running after Close")
	}
}

func TestStartRejectsMissingCommand(t *testing.T) {
	_, err := Start("x", "x", "chat", Spec{Command: "definitely-not-a-real-command-xyz"})
	if err == nil {
		t.Fatal("starting a missing command should fail")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say the command was not found, got: %v", err)
	}
}

func TestSessionCarriesExtraEnvironment(t *testing.T) {
	s, err := Start("env-1", "env", "chat", Spec{
		Command: "sh",
		Args:    []string{"-c", "printf 'sandbox=[%s]\\n' \"$IS_SANDBOX\""},
		Env:     []string{"IS_SANDBOX=1"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(time.Second) })
	if ok, err := s.WaitFor(t.Context(), `sandbox=\[1\]`, 5*time.Second); err != nil || !ok {
		t.Fatalf("the extra environment never reached the program (ok=%v err=%v)\nscreen:\n%s", ok, err, s.Screen())
	}
}
