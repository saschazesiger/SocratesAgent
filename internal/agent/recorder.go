package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// CoalesceWindow is how long text deltas of one part are gathered before they
// become one journal line: four lines a second per streaming part, not one
// per token.
const CoalesceWindow = 250 * time.Millisecond

// Recorder is the one place a chat's events are written: it numbers them into
// the journal, keeps the in-memory feed and the projection current, and
// coalesces text deltas. The agent host owns exactly one.
//
// Every event goes through it in one order, under one lock, so the journal,
// the feed and the projection never disagree about what happened when.
type Recorder struct {
	window time.Duration

	mu      sync.Mutex
	journal *Journal
	feed    *Feed
	proj    *Projection
	pending map[string]*pendingDelta
	// order is the pending parts in the order they started, which is the
	// order their lines are written in.
	order []string
	// lengths is the UTF-16 length of each part's text, counting the live
	// deltas not yet journaled: the offset the next delta starts at.
	lengths map[string]int
	err     error
	closed  bool

	stop chan struct{}
	done chan struct{}
}

// pendingDelta is the text of one part that is still to be journaled.
type pendingDelta struct {
	messageID string
	offset    int
	text      strings.Builder
	timer     *time.Timer
}

// NewRecorder records into journal, whose existing events are given, and
// syncs what it writes at most window after it was written. A window of zero
// is CoalesceWindow.
func NewRecorder(journal *Journal, events []Event, window time.Duration) *Recorder {
	if window <= 0 {
		window = CoalesceWindow
	}
	r := &Recorder{
		window:  window,
		journal: journal,
		feed:    NewFeed(events),
		proj:    Fold(events),
		pending: map[string]*pendingDelta{},
		lengths: map[string]int{},
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	for id, part := range r.proj.parts {
		r.lengths[id] = part.textLen
	}
	go r.syncLoop()
	return r
}

// errClosed is what a write after Close gets.
var errClosed = errors.New("the recorder is closed")

// Feed is the recorder's events in memory, for readers.
func (r *Recorder) Feed() *Feed { return r.feed }

// View runs fn with the projection, under the recorder's lock. fn must not
// call back into the recorder.
func (r *Recorder) View(fn func(*Projection)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.proj)
}

// Err is the first write error, after which nothing more is written.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Emit records events. The coalesced deltas waiting to be written go first, so
// the journal is always the order things happened in; a part event replaces
// its part whole, so deltas of that same part still waiting are dropped
// instead.
func (r *Recorder) Emit(events ...Event) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.emitLocked(events)
}

// EmitDurable is Emit followed by a sync, for the events a command is
// acknowledged on: once it returns, a power loss cannot take them.
func (r *Recorder) EmitDurable(events ...Event) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.emitLocked(events)
	if err != nil {
		return nil, err
	}
	if err := r.journal.Sync(); err != nil {
		r.err = err
		return nil, err
	}
	return out, nil
}

func (r *Recorder) emitLocked(events []Event) ([]Event, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.closed {
		return nil, errClosed
	}
	events = r.timeTodosLocked(events)
	for _, ev := range events {
		if ev.Type == TypePart {
			r.dropLocked(ev.PartID)
		}
	}
	r.flushAllLocked()
	return r.appendLocked(events)
}

// timeTodosLocked stamps the tasks of the todo parts among events
// (TimeTodos), each against the list as it stood before it: the part's own
// last version, else the chat's newest list.
func (r *Recorder) timeTodosLocked(events []Event) []Event {
	var out []Event
	var last json.RawMessage
	for i, ev := range events {
		if ev.Type != TypePart || ev.Kind != PartTodo || len(ev.Content) == 0 {
			continue
		}
		if out == nil {
			out = append([]Event(nil), events...)
		}
		prev := last
		if prev == nil {
			prev = r.proj.todoBefore(ev.PartID)
		}
		out[i].Content = TimeTodos(ev.Content, prev, time.Now())
		last = out[i].Content
	}
	if out == nil {
		return events
	}
	return out
}

func (r *Recorder) appendLocked(events []Event) ([]Event, error) {
	if r.err != nil {
		return nil, r.err
	}
	out, err := r.journal.Append(events...)
	if err != nil {
		r.err = err
		return nil, err
	}
	for _, ev := range out {
		r.proj.Apply(ev)
		if ev.Type == TypePart {
			r.lengths[ev.PartID] = UTF16Len(ev.Text)
		}
	}
	r.feed.Append(out...)
	return out, nil
}

// Delta streams text onto the end of a part: at once to every live reader,
// and into the journal coalesced, one line per window per part.
func (r *Recorder) Delta(messageID, partID, text string) {
	if text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil || r.closed {
		return
	}
	offset := r.lengths[partID]
	r.lengths[partID] = offset + UTF16Len(text)
	r.feed.Delta(Delta{MessageID: messageID, PartID: partID, Offset: offset, Text: text})
	p := r.pending[partID]
	if p == nil {
		p = &pendingDelta{messageID: messageID, offset: offset}
		p.timer = time.AfterFunc(r.window, func() { r.flushPart(partID) })
		r.pending[partID] = p
		r.order = append(r.order, partID)
	}
	p.text.WriteString(text)
}

// Flush writes every pending delta now.
func (r *Recorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushAllLocked()
}

func (r *Recorder) flushPart(partID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.pending[partID]; p != nil {
		r.forgetLocked(partID)
		_, _ = r.appendLocked([]Event{p.event(partID)})
	}
}

func (r *Recorder) flushAllLocked() {
	if len(r.pending) == 0 {
		return
	}
	events := make([]Event, 0, len(r.order))
	for _, id := range r.order {
		p := r.pending[id]
		p.timer.Stop()
		events = append(events, p.event(id))
	}
	r.pending, r.order = map[string]*pendingDelta{}, nil
	_, _ = r.appendLocked(events)
}

func (r *Recorder) dropLocked(partID string) {
	if p := r.pending[partID]; p != nil {
		p.timer.Stop()
		r.forgetLocked(partID)
	}
}

func (r *Recorder) forgetLocked(partID string) {
	delete(r.pending, partID)
	for i, id := range r.order {
		if id == partID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

func (p *pendingDelta) event(partID string) Event {
	return Event{Type: TypePartDelta, MessageID: p.messageID, PartID: partID, Offset: p.offset, Text: p.text.String()}
}

// syncLoop makes what was written durable within a window, so that a stream
// of deltas costs four syncs a second rather than one per line.
func (r *Recorder) syncLoop() {
	defer close(r.done)
	tick := time.NewTicker(r.window)
	defer tick.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-tick.C:
			if err := r.journal.Sync(); err != nil {
				r.mu.Lock()
				if r.err == nil {
					r.err = err
				}
				r.mu.Unlock()
			}
		}
	}
}

// Close writes what is pending, syncs and closes the journal.
func (r *Recorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.flushAllLocked()
	r.closed = true
	r.mu.Unlock()
	close(r.stop)
	<-r.done
	return r.journal.Close()
}
