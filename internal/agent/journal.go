package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JournalFile is the name of a chat's journal inside its directory.
const JournalFile = "events.jsonl"

// maxLine is the longest journal line the reader accepts. A tool result can be
// large, but a line of a hundred megabytes is not an event, it is damage.
const maxLine = 64 << 20

// Journal is a chat's append-only event log, `chats/<id>/events.jsonl`: one
// JSON object per line, numbered by seq from 1 without gaps. It has exactly
// one writer, the chat's agent host; everything else reads it.
//
// Durability is in two tiers. Append writes a batch of lines in one write and
// leaves it to the page cache; Sync makes everything written so far survive a
// power loss. The host syncs before it acknowledges a command, and otherwise
// at most a quarter of a second after a write (Recorder). What a power loss
// can take is therefore never an acknowledged line, and at worst it leaves a
// torn last line, which the reader skips and the next open cuts off.
type Journal struct {
	path string

	mu    sync.Mutex
	f     *os.File
	last  int64
	dirty bool
}

// OpenJournal opens a journal for appending, creating it and its directory if
// they are not there, and returns every event it already holds.
//
// A last line that is not a whole event - what a writer killed in the middle
// of a write, or a power loss, leaves - is cut off, so that the next append
// starts on a line of its own instead of gluing itself onto the fragment. A
// whole last line that only lacks its newline gets one.
func OpenJournal(path string) (*Journal, []Event, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	events, good, size, err := readJournal(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if good < size {
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("cut the torn end off %s: %w", path, err)
		}
	}
	if good > 0 {
		// A last event that arrived without its newline is whole, but the
		// next append must not run on from it.
		var b [1]byte
		if _, err := f.ReadAt(b[:], good-1); err == nil && b[0] != '\n' {
			if _, err := f.WriteAt([]byte{'\n'}, good); err != nil {
				f.Close()
				return nil, nil, err
			}
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, nil, err
	}
	f.Close()
	if created {
		syncDir(filepath.Dir(path))
	}
	af, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	j := &Journal{path: path, f: af}
	if n := len(events); n > 0 {
		j.last = events[n-1].Seq
	}
	return j, events, nil
}

// syncDir makes a new file's directory entry durable. It is best effort: a
// file system that refuses to sync a directory has nothing better to offer.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// Path is where the journal lives.
func (j *Journal) Path() string { return j.path }

// Last is the seq of the newest event.
func (j *Journal) Last() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.last
}

// Append stamps the events with the next seqs (and the time, where an event
// has none) and writes them in one write. It returns them as stamped.
func (j *Journal) Append(events ...Event) ([]Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil, errors.New("the journal is closed")
	}
	var buf bytes.Buffer
	now := time.Now().UnixMilli()
	out := make([]Event, len(events))
	seq := j.last
	for i, ev := range events {
		seq++
		ev.Seq = seq
		if ev.At == 0 {
			ev.At = now
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		out[i] = ev
	}
	if _, err := j.f.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	j.last = seq
	j.dirty = true
	return out, nil
}

// Sync makes every line appended so far durable.
func (j *Journal) Sync() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil || !j.dirty {
		return nil
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	j.dirty = false
	return nil
}

// Close syncs and closes the journal.
func (j *Journal) Close() error {
	if err := j.Sync(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// ReadJournal reads every whole event of a journal without changing the
// file, which is how anything but the writer reads one - including while the
// writer is in the middle of a line. A journal that does not exist is empty.
func ReadJournal(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	events, _, _, err := readJournal(f)
	return events, err
}

// LastSeq is the seq of the last whole event of a journal, read from its end
// rather than its start: what a reader that holds a summary of the journal
// as of some seq checks it against, without reading the history. A journal
// that does not exist, or holds no whole event, is 0. Like readJournal it
// takes a last event that lacks its newline and skips a torn or zero-filled
// tail.
func LastSeq(path string) (int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	for window := int64(64 << 10); ; window *= 2 {
		start := max(size-window, 0)
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		lines := bytes.Split(buf, []byte("\n"))
		// The first line of a window that does not start the file may be
		// the end of a longer one, and is no event of its own.
		first := 0
		if start > 0 {
			first = 1
		}
		for i := len(lines) - 1; i >= first; i-- {
			if len(lines[i]) <= maxLine {
				if ev, ok := parseLine(lines[i], 0); ok {
					return ev.Seq, nil
				}
			}
		}
		if start == 0 || window > maxLine {
			return 0, nil
		}
	}
}

// ScanJournal hands every whole event of a journal to fn, in order, without
// holding the history: what a reader that only sums something up (the cost
// of a chat's turns) reads a long journal with. It reads by readJournal's
// rules, and a journal that does not exist has no events.
func ScanJournal(path string, fn func(Event)) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, _, err = scanJournal(f, fn)
	return err
}

// readJournal parses f from its start. good is the byte offset just past the
// last whole event, and size how far the file goes.
func readJournal(f *os.File) ([]Event, int64, int64, error) {
	var events []Event
	good, size, err := scanJournal(f, func(ev Event) { events = append(events, ev) })
	if err != nil {
		return nil, 0, 0, err
	}
	return events, good, size, nil
}

// scanJournal hands every whole event of f to fn, from its start.
//
// A line that is not an event is skipped, and so is one whose seq does not
// move forward: the journal is only ever appended to, so neither can be
// anything but damage, and refusing the whole history over one torn line
// would be worse than the line.
func scanJournal(f *os.File, fn func(Event)) (int64, int64, error) {
	r := bufio.NewReaderSize(f, 64<<10)
	var offset, good int64
	var last int64
	for {
		line, err := r.ReadBytes('\n')
		offset += int64(len(line))
		if len(line) > 0 && len(line) <= maxLine {
			if ev, ok := parseLine(line, last); ok {
				fn(ev)
				last = ev.Seq
				good = offset
			}
		}
		if errors.Is(err, io.EOF) {
			return good, offset, nil
		}
		if err != nil {
			return 0, 0, err
		}
	}
}

func parseLine(line []byte, last int64) (Event, bool) {
	line = bytes.TrimRight(line, "\n")
	if len(bytes.TrimSpace(line)) == 0 {
		return Event{}, false
	}
	var ev Event
	if json.Unmarshal(line, &ev) != nil || ev.Type == "" || ev.Seq <= last {
		return Event{}, false
	}
	return ev, true
}
