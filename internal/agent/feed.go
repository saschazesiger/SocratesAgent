package agent

import (
	"sort"
	"sync"
)

// subDeltaLimit is how many live deltas one subscriber may be behind before
// the newest are dropped. Dropping one costs nothing that matters: the
// journal carries the same text, coalesced, a quarter of a second later.
const subDeltaLimit = 4096

// Feed is a chat's events in memory, in seq order, with the live deltas that
// fall between them. It is what every reader of a chat pulls from: the host's
// socket clients, and the server's browser streams.
//
// A reader is a pull cursor over the events and nothing more. Replay and the
// live tail are the same loop (Sub.Pull from the last seq it has sent), so no
// event can fall between the two or be sent twice, and a slow reader only
// slows itself: nothing is buffered per subscriber except the deltas.
type Feed struct {
	mu     sync.Mutex
	events []Event
	subs   map[*Sub]struct{}
}

// NewFeed holds events, which must be in seq order.
func NewFeed(events []Event) *Feed {
	return &Feed{events: events, subs: map[*Sub]struct{}{}}
}

// Append adds events after the last one and wakes every subscriber. Events
// are immutable once appended: readers keep slices of them.
func (f *Feed) Append(events ...Event) {
	if len(events) == 0 {
		return
	}
	f.mu.Lock()
	f.events = append(f.events, events...)
	for sub := range f.subs {
		sub.signal()
	}
	f.mu.Unlock()
}

// Last is the seq of the newest event, 0 for none.
func (f *Feed) Last() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastLocked()
}

func (f *Feed) lastLocked() int64 {
	if len(f.events) == 0 {
		return 0
	}
	return f.events[len(f.events)-1].Seq
}

// After is every event with a seq above seq. The slice shares the feed's
// storage and must not be written to.
func (f *Feed) After(seq int64) []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.afterLocked(seq)
}

func (f *Feed) afterLocked(seq int64) []Event {
	i := sort.Search(len(f.events), func(i int) bool { return f.events[i].Seq > seq })
	return f.events[i:len(f.events):len(f.events)]
}

// Delta hands a live delta to every subscriber. It is stamped with the last
// seq, so that Pull never hands it out ahead of the events before it.
func (f *Feed) Delta(d Delta) {
	f.mu.Lock()
	d.After = f.lastLocked()
	for sub := range f.subs {
		sub.push(d)
	}
	f.mu.Unlock()
}

// Subscribe registers a reader.
func (f *Feed) Subscribe() *Sub {
	sub := &Sub{feed: f, wake: make(chan struct{}, 1)}
	f.mu.Lock()
	f.subs[sub] = struct{}{}
	f.mu.Unlock()
	return sub
}

// Subscribers is how many readers are registered.
func (f *Feed) Subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// Sub is one reader of a Feed.
type Sub struct {
	feed *Feed
	wake chan struct{}

	mu     sync.Mutex
	deltas []Delta
}

// Wake is signalled whenever there may be something new to Pull.
func (s *Sub) Wake() <-chan struct{} { return s.wake }

// Pull is everything after seq: the events, then the live deltas that arrived
// since the last Pull. The deltas are taken before the events are read, and
// every delta was made after the events before it were appended, so sending
// the events first and the deltas second never puts a delta ahead of the part
// it belongs to.
func (s *Sub) Pull(seq int64) ([]Event, []Delta) {
	s.mu.Lock()
	deltas := s.deltas
	s.deltas = nil
	s.mu.Unlock()
	return s.feed.After(seq), deltas
}

// Close unregisters the reader.
func (s *Sub) Close() {
	s.feed.mu.Lock()
	delete(s.feed.subs, s)
	s.feed.mu.Unlock()
}

func (s *Sub) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Sub) push(d Delta) {
	s.mu.Lock()
	if len(s.deltas) < subDeltaLimit {
		s.deltas = append(s.deltas, d)
	}
	s.mu.Unlock()
	s.signal()
}
