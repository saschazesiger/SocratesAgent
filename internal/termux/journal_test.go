//go:build !windows

package termux

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestJournalRotatesWhileRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.raw")

	// Twenty numbered lines through a sink that may hold ten of them: the
	// point is that rotation happens while the session runs, because the user
	// this is built for never restarts one.
	var input bytes.Buffer
	var lines []string
	for i := 0; i < 20; i++ {
		line := strings.Repeat(string(rune('a'+i%26)), 9) + "\n"
		lines = append(lines, line)
		input.WriteString(line)
	}
	if err := journalSink(&input, path, 100, 1); err != nil {
		t.Fatalf("the sink failed: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no current journal: %v", err)
	}
	older, err := os.ReadFile(filepath.Join(dir, "journal.1.raw"))
	if err != nil {
		t.Fatalf("no rotated journal: %v", err)
	}
	if len(current) > 100 {
		t.Fatalf("the current journal is %d bytes, past the limit of 100", len(current))
	}
	if _, err := os.Stat(filepath.Join(dir, "journal.2.raw")); err == nil {
		t.Fatal("keep=1 should leave exactly one older file")
	}

	// Nothing may be lost across the boundary: the two files together are the
	// tail of the stream, in order.
	whole := string(older) + string(current)
	if !strings.HasSuffix(strings.Join(lines, ""), whole) {
		t.Fatalf("the journal is not a suffix of what was written:\n%q", whole)
	}
	for _, line := range lines[len(lines)-3:] {
		if !strings.Contains(whole, line) {
			t.Fatalf("the journal lost the recent line %q", line)
		}
	}
}

// TestJournalSinkRestartAppends covers the consequence of issuing pipe-pane
// without -o: a re-issued pipe replaces a running sink, so the new one starts
// against a file that already has content and must not truncate it.
func TestJournalSinkRestartAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.raw")
	if err := journalSink(strings.NewReader("first half\n"), path, 1<<20, 1); err != nil {
		t.Fatalf("the first sink failed: %v", err)
	}
	if err := journalSink(strings.NewReader("second half\n"), path, 1<<20, 1); err != nil {
		t.Fatalf("the second sink failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first half\nsecond half\n" {
		t.Fatalf("a restarted sink truncated the journal: %q", got)
	}
}

func TestTailJournalReachesIntoTheRotatedFile(t *testing.T) {
	dir := t.TempDir()
	id := "abc"
	if err := os.MkdirAll(SessionDir(dir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	path := JournalPath(dir, id)
	if err := os.WriteFile(rotatedPath(path, 1), []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("newer"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := TailJournal(dir, id, 100)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "oldernewer" {
		t.Fatalf("TailJournal = %q", got)
	}
	if got, err = TailJournal(dir, id, 3); err != nil || string(got) != "wer" {
		t.Fatalf("TailJournal(3) = %q, %v", got, err)
	}
	if got, err := TailJournal(dir, "missing", 100); err != nil || len(got) != 0 {
		t.Fatalf("a session with no journal should read as empty: %q, %v", got, err)
	}
}

func TestPipeCommandQuotesBothPaths(t *testing.T) {
	got := PipeCommand("/a b/it's/socrates", "/data/it's/journal.raw", 42, 1)
	want := `exec '/a b/it'\''s/socrates' journal-sink --path '/data/it'\''s/journal.raw' --max-bytes 42 --keep 1`
	if got != want {
		t.Fatalf("PipeCommand = %s\nwant %s", got, want)
	}
}

// TestWatchParentProcessNoticesReparenting is the mechanism the sink leans on
// when its pipe never closes: the parent it was started by is gone.
func TestWatchParentProcessNoticesReparenting(t *testing.T) {
	var ppid atomic.Int64
	ppid.Store(4242)
	fired := make(chan struct{})
	stop := watchParent(time.Millisecond, func() int { return int(ppid.Load()) },
		func() { close(fired) })
	defer close(stop)

	select {
	case <-fired:
		t.Fatal("the watch fired while the parent was still there")
	case <-time.After(30 * time.Millisecond):
	}
	ppid.Store(1)
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never noticed that the parent had gone")
	}
}

// TestJournalSinkDiesWithItsSession and its neighbour below are here because a
// sink that outlives its session is not a theory: a run that left twenty-two
// of them behind held twenty-two journals open and a copy of the binary
// resident in each. Both ends of a session's life are covered - the deliberate
// one and the violent one - and both are measured on the process table rather
// than on our idea of what tmux does with the pipe.
func TestJournalSinkDiesWithItsSession(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	journal := JournalPath(l.dataDir, row.ID)
	waitFor(t, 10*time.Second, "the journal sink to start", func() bool {
		return len(sinkPIDs(t, journal)) > 0
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.Delete(ctx, row.ID); err != nil {
		t.Fatalf("could not delete the session: %v", err)
	}
	waitFor(t, 10*time.Second, "the journal sink to stop after the delete", func() bool {
		return len(sinkPIDs(t, journal)) == 0
	})
}

func TestJournalSinkDiesWithTheTmuxServer(t *testing.T) {
	l := newLab(t)
	row := l.create(shellSpec(t.TempDir()))
	journal := JournalPath(l.dataDir, row.ID)
	waitFor(t, 10*time.Second, "the journal sink to start", func() bool {
		return len(sinkPIDs(t, journal)) > 0
	})

	// A reboot, or an operator with a big hammer: the server is killed
	// outright and never gets to close anything.
	serverPID, err := strconv.Atoi(l.tmuxOut("display-message", "-p", "#{pid}"))
	if err != nil {
		t.Fatalf("could not read the tmux server pid: %v", err)
	}
	if err := syscall.Kill(serverPID, syscall.SIGKILL); err != nil {
		t.Fatalf("could not kill the tmux server: %v", err)
	}
	waitFor(t, 15*time.Second, "the journal sink to stop with its server", func() bool {
		return len(sinkPIDs(t, journal)) == 0
	})
}

// sinkPIDs is every process on this machine whose command line names this
// journal. `ps` is the portable way to ask; /proc is not on macOS.
func sinkPIDs(t *testing.T, journal string) []int {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err != nil {
		t.Fatalf("could not read the process table: %v", err)
	}
	var pids []int
	for _, line := range Lines(string(out)) {
		if !strings.Contains(line, journal) || !strings.Contains(line, "journal-sink") {
			continue
		}
		pid, err := strconv.Atoi(strings.Fields(line)[0])
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
