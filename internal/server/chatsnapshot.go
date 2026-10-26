package server

// A chat's history as the server holds it is read from a snapshot and the
// journal after it: the journal compacted (agent.Compact) up to some event,
// kept in the cache with where in the journal that event ends, so that a
// chat whose journal is hundreds of megabytes - a workflow that journaled
// its whole state for hours - is read in a fraction of the time and held in
// a fraction of the memory. The journal is never changed; the snapshot is
// something that can be made again, and one that is not the journal's any
// more (a restore) is let go.
//
// A history read from a snapshot is compacted up to its seq (base): what a
// browser holds of the chat up to a seq below it may lack what was moved
// back to where a part first appeared, so a browser that holds less than the
// base starts the chat again from nothing (the stream's hello says the base;
// chatview.js), which a light replay makes cheap.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// snapshotFrom is how much journal read beyond a snapshot (or without one)
// makes a new snapshot worth writing.
const snapshotFrom = 8 << 20

// snapshotVersion is the shape of a snapshot; another one is let go.
const snapshotVersion = 1

// snapshotHead is a snapshot's first line: the seq of the last event it
// holds, which is the journal's event that ends at offset.
type snapshotHead struct {
	V      int   `json:"v"`
	Seq    int64 `json:"seq"`
	Offset int64 `json:"offset"`
}

// snapshotPath is where a chat's snapshot is kept.
func snapshotPath(dataDir, id string) string {
	return filepath.Join(dataDir, filepath.FromSlash(paths.Cache), "chats", id+".jsonl")
}

// history is a chat's events as the server reads them.
type history struct {
	events []agent.Event
	// base is the seq up to which the events are compacted, 0 for none.
	base int64
	// good is where in the journal the last event ends, and tail how much
	// of the journal was read beyond the snapshot.
	good, tail int64
}

// readHistory is a chat's events: its snapshot and the journal after it, or
// the whole journal when there is no snapshot that is still the journal's.
func readHistory(dataDir, id string) (history, error) {
	journal := agenthost.JournalPath(dataDir, id)
	if h, ok := readSnapshot(dataDir, id); ok {
		rest, good, err := agent.ReadJournalFrom(journal, h.good, h.base)
		if err != nil {
			return history{}, err
		}
		h.events = append(h.events, rest...)
		h.tail = good - h.good
		h.good = good
		return h, nil
	}
	events, good, err := agent.ReadJournalFrom(journal, 0, 0)
	if err != nil {
		return history{}, err
	}
	return history{events: events, good: good, tail: good}, nil
}

// readSnapshot is a chat's snapshot, if it has one that is still its
// journal's: the event it ends on ends where it says in the journal.
func readSnapshot(dataDir, id string) (history, bool) {
	f, err := os.Open(snapshotPath(dataDir, id))
	if err != nil {
		return history{}, false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return history{}, false
	}
	var head snapshotHead
	if json.Unmarshal(line, &head) != nil || head.V != snapshotVersion || head.Seq <= 0 || head.Offset <= 0 {
		return history{}, false
	}
	if seq, err := agent.SeqAt(agenthost.JournalPath(dataDir, id), head.Offset); err != nil || seq != head.Seq {
		return history{}, false
	}
	var events []agent.Event
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var ev agent.Event
			if json.Unmarshal(line, &ev) != nil {
				return history{}, false
			}
			events = append(events, ev)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return history{}, false
		}
	}
	if len(events) == 0 || events[len(events)-1].Seq != head.Seq {
		return history{}, false
	}
	return history{events: events, base: head.Seq, good: head.Offset}, true
}

// writeSnapshot keeps events - a chat's whole history, whose last event ends
// at offset in its journal - compacted, atomically. The last event is kept
// as it is, whatever compacting would make of it, so that the snapshot ends
// where the journal it was read from did.
func writeSnapshot(dataDir, id string, events []agent.Event, offset int64) error {
	n := len(events)
	if n == 0 || offset <= 0 {
		return nil
	}
	path := snapshotPath(dataDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 256<<10)
	enc := json.NewEncoder(w)
	err = enc.Encode(snapshotHead{V: snapshotVersion, Seq: events[n-1].Seq, Offset: offset})
	for _, ev := range append(agent.Compact(events[:n-1]), events[n-1]) {
		if err != nil {
			break
		}
		err = enc.Encode(ev)
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write the snapshot of %s: %w", id, err)
	}
	return os.Rename(tmp, path)
}
