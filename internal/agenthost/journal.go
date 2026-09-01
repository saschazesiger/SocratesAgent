package agenthost

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// journalMax is when events.jsonl is trimmed, and journalKeep is what is kept.
// A long turn is a few hundred kilobytes; it is tool output that makes a file
// big, which is why harness.ToolOutputLimit exists at all.
const (
	journalMax  = 64 << 20
	journalKeep = 4 << 20
)

// ErrReplayWindow is what a subscribe below the trimmed point gets. Only Adopt
// can reach it - a live turn's engine is already subscribed - and it ends that
// run as interrupted with a sentence saying why.
var ErrReplayWindow = errors.New("replay window exceeded")

// journal is the host's replay buffer. mu covers more than the file: it is the
// lock that makes "append + broadcast" and "register + replay + snapshot"
// mutually exclusive, which is what keeps a subscriber's stream free of gaps
// and duplicates (see the subscribe rule in host.go).
type journal struct {
	mu   sync.Mutex
	dir  string
	f    *os.File
	seq  int64
	size int64
	// trimmedTo is the highest seq that rotation threw away. A subscribe that
	// asks for anything before it cannot be answered honestly, so it is
	// refused - including a subscribe from zero, which is a first turn on this
	// host and would otherwise be handed the surviving tail as if it were the
	// whole journal.
	trimmedTo int64
}

func journalPath(dir string) string { return filepath.Join(dir, fileEvents) }

// openJournal opens (or creates) events.jsonl and recovers the sequence number
// by reading what is already there. There is no index and no header: the file
// is a replay buffer for a browser, and the CLI's own transcript is the
// durable record.
func openJournal(dir string) (*journal, error) {
	j := &journal{dir: dir}
	path := journalPath(dir)
	if info, err := os.Stat(path); err == nil {
		j.size = info.Size()
		first := int64(0)
		if err := j.scan(func(ev harness.Event) bool {
			if first == 0 {
				first = ev.Seq
			}
			if ev.Seq > j.seq {
				j.seq = ev.Seq
			}
			return true
		}); err != nil {
			return nil, err
		}
		// A file that does not begin at seq 1 was rotated by a previous run of
		// this host. Recovering where it was cut is what stops a restarted
		// host from serving the surviving tail as if it were the whole
		// journal: a subscribe from before that point is refused instead.
		if first > 1 {
			j.trimmedTo = first - 1
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	j.f = f
	return j, nil
}

func (j *journal) close() error {
	if j.f == nil {
		return nil
	}
	return j.f.Close()
}

// append stamps the event and writes it. The caller holds j.mu: appending and
// broadcasting have to be one step, which is the whole point of the lock.
func (j *journal) append(ev harness.Event) (harness.Event, error) {
	j.seq++
	ev.Seq = j.seq
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return ev, err
	}
	raw = append(raw, '\n')
	n, err := j.f.Write(raw)
	j.size += int64(n)
	if err != nil {
		return ev, err
	}
	if j.size > journalMax {
		j.rotate()
	}
	return ev, nil
}

// replay streams every event with Seq > from, in order, stopping when fn says
// so. It reads the file from the start: files are small, and a linear read is
// one thing to get wrong instead of an index that can disagree with the data.
func (j *journal) replay(from int64, fn func(harness.Event) bool) error {
	if from < j.trimmedTo {
		return ErrReplayWindow
	}
	return j.scan(func(ev harness.Event) bool {
		if ev.Seq <= from {
			return true
		}
		return fn(ev)
	})
}

func (j *journal) scan(fn func(harness.Event) bool) error {
	f, err := os.Open(journalPath(j.dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev harness.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// A half written last line is what a killed host leaves. It is not
			// a reason to refuse the whole transcript.
			continue
		}
		if !fn(ev) {
			return nil
		}
	}
	return sc.Err()
}

// rotate keeps the last journalKeep bytes at a line boundary and remembers the
// highest seq it threw away. Best effort: a rotation that fails leaves a large
// file, which is worse than a small one and better than a lost journal.
func (j *journal) rotate() {
	path := journalPath(j.dir)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= journalKeep {
		return
	}
	if _, err := f.Seek(info.Size()-journalKeep, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 64<<10)
	// The first line after the seek is a fragment of whatever was there.
	if _, err := r.ReadString('\n'); err != nil {
		return
	}
	tail, err := io.ReadAll(r)
	if err != nil {
		return
	}
	// Everything before the first surviving line is gone, so a subscribe from
	// below that point cannot be answered.
	first := harness.Event{}
	if i := indexOfNewline(tail); i >= 0 {
		_ = json.Unmarshal(tail[:i], &first)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, tail, 0o600); err != nil {
		return
	}
	if err := j.f.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
	nf, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		// Nothing can be written any more; the host will notice on the next
		// append and end with a fatal, which is the honest outcome.
		return
	}
	j.f = nf
	j.size = int64(len(tail))
	if first.Seq > 0 {
		// The last seq that is gone, not the first that survives: a subscribe
		// from exactly that number asks for everything after it, and
		// everything after it is still here.
		j.trimmedTo = first.Seq - 1
	}
}

func indexOfNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}
