package engine

import (
	"encoding/json"
	"sync"
)

// Bus is a tiny in-process pub/sub used to push live run events to the
// browser over SSE. Every subscriber gets its own buffered channel; a
// subscriber that cannot keep up is dropped and reconnects, at which point it
// replays the missed steps from the database by revision number.
type Bus struct {
	mu     sync.Mutex
	nextID int
	subs   map[string]map[int]chan []byte
}

// NewBus creates an empty bus.
func NewBus() *Bus {
	return &Bus{subs: map[string]map[int]chan []byte{}}
}

// Subscribe registers a listener for one chat.
func (b *Bus) Subscribe(chatID string) (int, <-chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan []byte, 256)
	if b.subs[chatID] == nil {
		b.subs[chatID] = map[int]chan []byte{}
	}
	b.subs[chatID][id] = ch
	return id, ch
}

// Unsubscribe removes a listener.
func (b *Bus) Unsubscribe(chatID string, id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := b.subs[chatID]; ok {
		if ch, ok := m[id]; ok {
			delete(m, id)
			close(ch)
		}
		if len(m) == 0 {
			delete(b.subs, chatID)
		}
	}
}

// Publish sends an event to every listener of a chat.
func (b *Bus) Publish(chatID string, event any) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.subs[chatID] {
		select {
		case ch <- payload:
		default:
			// Slow consumer: drop it, the client reconnects and catches up.
			delete(b.subs[chatID], id)
			close(ch)
		}
	}
}
