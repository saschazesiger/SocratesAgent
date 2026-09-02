package termux

import (
	"context"
	"sync"
)

// RingSize is the per viewer replay buffer. A megabyte holds several seconds
// of a busy full screen redraw and many minutes of ordinary output, which is
// the window a phone that lost its connection has to come back in.
const RingSize = 1 << 20

// Ring is a fixed size byte buffer with sequence numbers, one per viewer.
//
// It is what makes a reconnect a pure gap in a byte stream rather than a
// repaint: the reader goroutine only ever appends here, and the socket writer
// pulls from it. A viewer that stops reading falls behind in the ring and
// catches up with a bigger slice; it can never make the process grow, and it
// can never stall the pane.
type Ring struct {
	mu   sync.Mutex
	buf  []byte
	base uint64 // sequence number of buf[0]
	head uint64 // sequence number of the next byte to be appended
	wake chan struct{}
}

// NewRing returns an empty ring holding at most size bytes.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = RingSize
	}
	return &Ring{buf: make([]byte, 0, size), wake: make(chan struct{})}
}

// Append stores p and returns the sequence number of its first byte. Bytes
// older than the ring's size are forgotten.
func (r *Ring) Append(p []byte) uint64 {
	if len(p) == 0 {
		return r.Head()
	}
	r.mu.Lock()
	first := r.head
	capacity := cap(r.buf)
	if len(p) >= capacity {
		// A single write larger than the ring: only its tail can be kept.
		r.buf = append(r.buf[:0], p[len(p)-capacity:]...)
		r.base = r.head + uint64(len(p)-capacity)
	} else {
		if len(r.buf)+len(p) > capacity {
			drop := len(r.buf) + len(p) - capacity
			r.buf = append(r.buf[:0], r.buf[drop:]...)
			r.base += uint64(drop)
		}
		r.buf = append(r.buf, p...)
	}
	r.head += uint64(len(p))
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
	return first
}

// Since returns the bytes from seq onwards. The second result is false when
// seq is older than the ring still holds, which is the caller's signal to
// resync by attaching afresh rather than to send a hole.
func (r *Ring) Since(seq uint64) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq > r.head || seq < r.base {
		return nil, false
	}
	out := make([]byte, r.head-seq)
	copy(out, r.buf[seq-r.base:])
	return out, true
}

// Head is the sequence number the next appended byte will have.
func (r *Ring) Head() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.head
}

// Base is the oldest sequence number the ring can still serve.
func (r *Ring) Base() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base
}

// Wait blocks until there is something past seq, or the context ends.
func (r *Ring) Wait(ctx context.Context, seq uint64) error {
	for {
		r.mu.Lock()
		if r.head > seq {
			r.mu.Unlock()
			return nil
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
