//go:build !windows

package termux

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// TestMain lets the test binary stand in for the Socrates executable.
//
// Two of the things this package does are only real when another process does
// them: the journal sink tmux pipes a pane into, and the hook tmux runs when a
// pane dies. Pointing both at this binary tests the shipped code paths rather
// than a stub of them, and gives the probe programs the panes need a home.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "journal-sink":
			os.Exit(runSinkHelper(os.Args[2:]))
		case "tmux-hook":
			os.Exit(runHookHelper(os.Args[2:]))
		case "osc-probe":
			os.Exit(runOSCProbe(os.Args[2:]))
		case "spawn-session":
			os.Exit(runSpawnHelper(os.Args[2:]))
		}
	}
	os.Exit(m.Run())
}

func runSinkHelper(args []string) int {
	fs := flag.NewFlagSet("journal-sink", flag.ContinueOnError)
	path := fs.String("path", "", "")
	maxBytes := fs.Int64("max-bytes", JournalMaxBytes, "")
	keep := fs.Int("keep", JournalKeep, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := RunJournalSink(*path, *maxBytes, *keep); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runHookHelper(args []string) int {
	fs := flag.NewFlagSet("tmux-hook", flag.ContinueOnError)
	sock := fs.String("sock", "", "")
	event := fs.String("event", "", "")
	session := fs.String("session", "", "")
	status := fs.String("status", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := SendHook(*sock, Hook{Event: *event, Session: *session, Status: *status}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runOSCProbe is the program a pane runs when a test wants to know what tmux
// answers a pane's colour questions with. Its terminal has already been put in
// raw mode by the shell that exec'd it, so a reply that carries no newline
// still arrives.
func runOSCProbe(args []string) int {
	fs := flag.NewFlagSet("osc-probe", flag.ContinueOnError)
	out := fs.String("out", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chunks := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	fmt.Fprint(os.Stdout, "\x1b]11;?\x1b\\\x1b]10;?\x1b\\\x1b[?996n")

	// Collect for a fixed window: tmux answers in milliseconds, and a version
	// that does not answer one of the three must not hang the pane.
	var collected []byte
	deadline := time.After(1500 * time.Millisecond)
	for done := false; !done; {
		select {
		case c := <-chunks:
			collected = append(collected, c...)
		case <-deadline:
			done = true
		}
	}
	if *out != "" {
		_ = os.WriteFile(*out, collected, 0o600)
	}
	return 0
}

// runSpawnHelper creates a session in a data directory and then waits to be
// killed, so that a test can prove the session outlives the process that made
// it.
func runSpawnHelper(args []string) int {
	fs := flag.NewFlagSet("spawn-session", flag.ContinueOnError)
	dir := fs.String("data", "", "")
	tmuxBin := fs.String("tmux", "tmux", "")
	id := fs.String("id", "", "")
	ready := fs.String("ready", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := store.Open(filepath.Join(*dir, "socrates.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	m, err := New(st, Config{
		DataDir: *dir, TmuxBin: *tmuxBin, WindowSize: "manual",
		Logf: func(string, ...any) {},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := m.Start(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	spec := shellSpec(*dir, "/bin/sh")
	spec.ID = *id
	if _, err := m.Create(context.Background(), spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *ready != "" {
		_ = os.WriteFile(*ready, []byte("ready"), 0o600)
	}
	select {}
}

// oscProbeArgv runs the probe in a pane, with the tty in raw mode so that a
// reply without a newline is not swallowed by line discipline.
func oscProbeArgv(bin, outPath string) []string {
	return []string{"/bin/sh", "-c", `stty raw -echo; exec "$0" osc-probe --out "$1"`, bin, outPath}
}
