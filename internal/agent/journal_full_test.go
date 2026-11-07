package agent

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fillDisk makes the journal's writes fail the way a full disk does, and
// returns what gives it its file back.
func fillDisk(t *testing.T, j *Journal) (restore func()) {
	t.Helper()
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skip("no /dev/full:", err)
	}
	j.mu.Lock()
	real := j.f
	j.f = full
	j.mu.Unlock()
	return func() {
		j.mu.Lock()
		j.f = real
		j.mu.Unlock()
		full.Close()
	}
}

// A journal whose disk was full goes on once there is room again, after the
// last whole line and with no seq skipped or written twice.
func TestJournalGoesOnAfterTheDiskWasFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	j, _, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.Append(Event{Type: TypeNotice, Text: "one"}); err != nil {
		t.Fatal(err)
	}
	restore := fillDisk(t, j)
	if _, err := j.Append(Event{Type: TypeNotice, Text: "lost"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("a write to a full disk: %v", err)
	}
	restore()
	// What a write that ran out of room half way leaves on the file, and
	// could not be cut off at the time.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":2,"type":"notice","te`)
	f.Close()
	j.torn = true

	out, err := j.Append(Event{Type: TypeNotice, Text: "two"})
	if err != nil {
		t.Fatalf("the write after the disk has room again: %v", err)
	}
	if out[0].Seq != 2 {
		t.Fatalf("seq %d after a failed write", out[0].Seq)
	}
	if err := j.Sync(); err != nil {
		t.Fatal(err)
	}
	events, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Text != "one" || events[1].Text != "two" || events[1].Seq != 2 {
		t.Fatalf("journal after the full disk %+v", events)
	}
}

// A recorder does not give up after a failed write: the command that failed
// says so, the deltas waiting are kept, and the next write records again.
func TestRecorderGoesOnAfterTheDiskWasFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), JournalFile)
	j, _, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(j, nil, 20*time.Millisecond)
	if _, err := rec.Emit(Event{Type: TypePart, MessageID: "m", PartID: "p", Kind: PartText, State: PartStreaming}); err != nil {
		t.Fatal(err)
	}
	restore := fillDisk(t, j)
	rec.Delta("m", "p", "hel")
	time.Sleep(60 * time.Millisecond) // the delta's window fails at least once
	if _, err := rec.EmitDurable(Event{Type: TypeNotice, Text: "lost"}); err == nil {
		t.Fatal("an event journaled to a full disk")
	}
	rec.Delta("m", "p", "lo")
	restore()

	if _, err := rec.EmitDurable(Event{Type: TypeNotice, Text: "back"}); err != nil {
		t.Fatalf("the write after the disk has room again: %v", err)
	}
	rec.Delta("m", "p", "!")
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seqs %+v", events)
		}
		if ev.Text == "lost" {
			t.Fatalf("the failed event was journaled: %+v", events)
		}
	}
	if got := Fold(events).Part("p").Text; got != "hello!" {
		t.Fatalf("the part's text %q from %+v", got, events)
	}
	if last := events[len(events)-1]; last.Type != TypePartDelta || last.Offset != 5 {
		t.Fatalf("last line %+v", last)
	}
}
