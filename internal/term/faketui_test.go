package term

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The tests need a program that behaves like a small interactive CLI: it
// paints a screen, it has a prompt, it reacts to keys. Rather than depending
// on a real agent CLI being installed, the test binary re-executes itself in
// "fake TUI" mode and drives that.

const fakeTUIEnv = "SOCRATES_FAKE_TUI"

func TestMain(m *testing.M) {
	if os.Getenv(fakeTUIEnv) == "1" {
		fakeTUI()
		return
	}
	// The manager starts session hosts by re-executing the Socrates binary.
	// Under test that binary is this test binary, so it has to answer to the
	// same subcommand.
	if len(os.Args) > 2 && os.Args[1] == "term-host" {
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

// fakeTUISpec returns a Spec that starts the fake CLI.
func fakeTUISpec(dir string) Spec {
	return Spec{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestNothingAtAll"},
		Dir:     dir,
		Env:     []string{fakeTUIEnv + "=1"},
		Cols:    80,
		Rows:    20,
	}
}

// TestNothingAtAll exists so the re-executed test binary has a run filter that
// matches nothing; the fake CLI takes over before any test would run.
func TestNothingAtAll(t *testing.T) {}

// fakeTUI is the child process: a prompt driven, screen painting program.
func fakeTUI() {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	// A banner with colour, a cursor move and a title, so that the escape
	// handling in the screen and in the journal both get exercised.
	fmt.Fprint(out, "\x1b]0;fake agent\x07")
	fmt.Fprint(out, "\x1b[1;32mFake Agent\x1b[0m v1\r\n")
	fmt.Fprint(out, "type 'help' for commands\r\n")
	prompt := func() { fmt.Fprint(out, "ready> "); out.Flush() }
	prompt()

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 4096), 1<<20)
	for in.Scan() {
		line := strings.TrimRight(in.Text(), "\r\n")
		switch {
		case line == "exit":
			fmt.Fprint(out, "bye\r\n")
			out.Flush()
			os.Exit(0)
		case line == "fail":
			fmt.Fprint(out, "giving up\r\n")
			out.Flush()
			os.Exit(3)
		case line == "hello":
			fmt.Fprint(out, "world\r\n")
		case line == "slow":
			for i := 1; i <= 4; i++ {
				fmt.Fprintf(out, "step %d\r\n", i)
				out.Flush()
				time.Sleep(250 * time.Millisecond)
			}
			fmt.Fprint(out, "finished\r\n")
		case line == "progress":
			for i := 0; i <= 100; i += 25 {
				fmt.Fprintf(out, "\rdownloading %d%%", i)
				out.Flush()
			}
			fmt.Fprint(out, "\rdownloaded       \r\n")
		case line == "repaint":
			// Clear the screen and paint a box at a fixed position, the way a
			// full screen program does.
			fmt.Fprint(out, "\x1b[2J\x1b[H")
			fmt.Fprint(out, "\x1b[3;5HAllow this action?\r\n")
			fmt.Fprint(out, "\x1b[5;5H  1. Yes\r\n")
			fmt.Fprint(out, "\x1b[6;5H  2. No\r\n")
			fmt.Fprint(out, "\x1b[8;1H")
		case strings.HasPrefix(line, "keys "):
			// Echo the raw bytes that arrived, so key encoding can be checked.
			fmt.Fprintf(out, "got %q\r\n", strings.TrimPrefix(line, "keys "))
		default:
			fmt.Fprintf(out, "you said: %s\r\n", line)
		}
		prompt()
	}
	os.Exit(0)
}

// startFake is the helper every test uses.
func startFake(t *testing.T) *Session {
	t.Helper()
	s, err := Start("t_"+t.Name(), "fake", "chat", fakeTUISpec(t.TempDir()))
	if err != nil {
		t.Fatalf("start fake CLI: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(2 * time.Second) })
	if ok, err := s.WaitFor(t.Context(), `ready>`, 10*time.Second); err != nil || !ok {
		t.Fatalf("fake CLI never showed its prompt (ok=%v err=%v)\nscreen:\n%s", ok, err, s.Screen())
	}
	return s
}

// ensure exec is referenced even on builds where the helper is unused.
var _ = exec.Command
