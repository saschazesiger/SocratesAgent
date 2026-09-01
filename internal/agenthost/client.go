package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// ErrGone is returned when the host process of a session is no longer there.
var ErrGone = errors.New("this agent session is no longer running")

// Frame is one thing the host said, in the order it said it. Exactly one of
// the two fields is set. CaughtUp carries the status frame that closes a
// replay - "everything the host had written when you subscribed is now behind
// you" - and it travels on the SAME channel as the events precisely so that
// "behind you" means applied, not merely queued.
type Frame struct {
	Event    *harness.Event
	CaughtUp *Status
}

// Handle is Socrates' side of a session: a connection to the host process that
// owns the adapter. It mirrors the last Status so the API can be served
// without a round trip, and it forwards commands to the host.
type Handle struct {
	id     string
	chatID string
	dir    string

	conn net.Conn
	enc  *json.Encoder

	seq     atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan Response
	status  Status
	closed  bool
	done    chan struct{}
	// sub is the current subscription, or nil. The read loop discards a pushed
	// frame when there is none and blocks until the subscriber takes it when
	// there is - never both, because a buffer nobody drains fills up, stops
	// the read loop and wedges the host from the client side.
	sub *subscription
	// ready closes when the host has answered anything at all, so a caller
	// never sees a handle whose status is still empty.
	ready     chan struct{}
	readyOnce sync.Once
}

func newHandle(id, chatID, dir string, conn net.Conn) *Handle {
	h := &Handle{
		id: id, chatID: chatID, dir: dir,
		conn: conn, enc: json.NewEncoder(conn),
		pending: map[int64]chan Response{},
		done:    make(chan struct{}),
		ready:   make(chan struct{}),
	}
	go h.read()
	return h
}

func (h *Handle) read() {
	scanner := bufio.NewScanner(h.conn)
	scanner.Buffer(make([]byte, 0, 64<<10), 32<<20)
	for scanner.Scan() {
		var resp Response
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.Status != nil {
			h.readyOnce.Do(func() { close(h.ready) })
			h.mu.Lock()
			h.status = *resp.Status
			h.mu.Unlock()
		}
		if resp.ID == 0 {
			// A pushed journal event. There is no unsubscribe op on the wire,
			// so the host goes on pushing to a connection that had one.
			if resp.Event != nil {
				h.deliver(Frame{Event: resp.Event})
			}
			continue
		}
		h.readyOnce.Do(func() { close(h.ready) })
		if resp.Type == TypeStatus && resp.Status != nil {
			// The reply that closes a replay travels on the frame channel as
			// well as to the caller of Subscribe, because reaching it is the
			// proof that everything before it has been applied.
			st := *resp.Status
			h.deliver(Frame{CaughtUp: &st})
		}
		h.mu.Lock()
		ch := h.pending[resp.ID]
		delete(h.pending, resp.ID)
		h.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	h.markGone()
}

// subscription is one reader of the frame stream. Its channel is never closed
// - a closer racing a blocked sender is exactly the panic that costs an
// evening - so ending a subscription closes `done` instead, and the forwarder
// goroutine started in Subscribe is the only thing that ever closes what the
// caller ranges over.
type subscription struct {
	ch   chan Frame
	done chan struct{}
	once sync.Once
}

func (s *subscription) stop() { s.once.Do(func() { close(s.done) }) }

// deliver hands a frame to the current subscriber, blocking until it is taken.
// Blocking is the point: it is how backpressure reaches the host. With no
// subscriber the frame is discarded rather than buffered.
func (h *Handle) deliver(f Frame) {
	h.mu.Lock()
	s := h.sub
	h.mu.Unlock()
	if s == nil {
		return
	}
	select {
	case s.ch <- f:
	case <-s.done:
	case <-h.done:
	}
}

func (h *Handle) markGone() {
	h.readyOnce.Do(func() { close(h.ready) })
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.status.Running = false
	h.status.Busy = false
	pending := h.pending
	h.pending = map[int64]chan Response{}
	sub := h.sub
	h.sub = nil
	h.mu.Unlock()

	for _, ch := range pending {
		ch <- Response{Type: TypeError, Error: ErrGone.Error()}
	}
	close(h.done)
	if sub != nil {
		sub.stop()
	}
	_ = h.conn.Close()
}

// call sends one request and waits for its reply.
func (h *Handle) call(ctx context.Context, req Request, timeout time.Duration) (Response, error) {
	req.ID = h.seq.Add(1)
	ch := make(chan Response, 1)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return Response{}, ErrGone
	}
	h.pending[req.ID] = ch
	err := h.enc.Encode(req)
	h.mu.Unlock()
	if err != nil {
		h.mu.Lock()
		delete(h.pending, req.ID)
		h.mu.Unlock()
		return Response{}, err
	}

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case resp := <-ch:
		if resp.Type == TypeError {
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.pending, req.ID)
		h.mu.Unlock()
		return Response{}, ctx.Err()
	case <-timer:
		h.mu.Lock()
		delete(h.pending, req.ID)
		h.mu.Unlock()
		return Response{}, fmt.Errorf("the agent session did not answer within %s", timeout)
	case <-h.done:
		return Response{}, ErrGone
	}
}

// ID, ChatID and Dir identify the session.
func (h *Handle) ID() string     { return h.id }
func (h *Handle) ChatID() string { return h.chatID }
func (h *Handle) Dir() string    { return h.dir }

// Status returns the last status the host reported.
func (h *Handle) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Alive reports whether the session is still usable.
func (h *Handle) Alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && h.status.Running
}

// Refresh asks the host for a fresh status rather than the cached one.
func (h *Handle) Refresh(ctx context.Context) (Status, error) {
	resp, err := h.call(ctx, Request{Op: OpStatus}, 10*time.Second)
	if err != nil {
		return h.Status(), err
	}
	if resp.Status == nil {
		return h.Status(), nil
	}
	return *resp.Status, nil
}

// Send delivers one user message and returns the journal position the turn
// starts after. That number is the floor the engine consumes from, and it is
// written to the chat row before a single event of the turn is applied.
func (h *Handle) Send(ctx context.Context, turnID, text string) (int64, error) {
	resp, err := h.call(ctx, Request{Op: OpSend, TurnID: turnID, Text: text}, 90*time.Second)
	if err != nil {
		return 0, err
	}
	return resp.Seq, nil
}

// Interrupt cancels the turn in flight.
func (h *Handle) Interrupt(ctx context.Context, timeout time.Duration) error {
	_, err := h.call(ctx, Request{Op: OpInterrupt}, timeout)
	if errors.Is(err, ErrGone) {
		return nil
	}
	return err
}

// Subscribe streams the journal from after `from`, then the caught-up frame,
// then the live events - one ordered channel, wire order preserved.
func (h *Handle) Subscribe(from int64) (<-chan Frame, func()) {
	out := make(chan Frame)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(out)
		return out, func() {}
	}
	// One subscription at a time: the engine runs one turn per chat, and a
	// second reader would take half the frames.
	if h.sub != nil {
		h.sub.stop()
	}
	s := &subscription{ch: make(chan Frame), done: make(chan struct{})}
	h.sub = s
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if h.sub == s {
			h.sub = nil
		}
		h.mu.Unlock()
		s.stop()
	}
	// The forwarder owns `out` and is the only thing that closes it, so a
	// caller ranging over it learns that the stream ended without anybody
	// having to close a channel a sender might still be writing to.
	go func() {
		defer close(out)
		for {
			select {
			case f := <-s.ch:
				select {
				case out <- f:
				case <-s.done:
					return
				}
			case <-s.done:
				return
			case <-h.done:
				return
			}
		}
	}()
	go func() {
		if _, err := h.call(context.Background(), Request{Op: OpSubscribe, FromSeq: from}, 5*time.Minute); err != nil {
			// The reply is also delivered as a frame, so a failure here means
			// there will be no caught-up frame either: end the subscription so
			// the consumer's closed-channel path runs.
			unsubscribe()
		}
	}()
	return out, unsubscribe
}

// Close ends the session and its host process.
func (h *Handle) Close(ctx context.Context, grace time.Duration) error {
	if grace <= 0 {
		grace = 5 * time.Second
	}
	_, err := h.call(ctx, Request{Op: OpClose, GraceMS: int(grace.Milliseconds())}, grace+15*time.Second)
	if errors.Is(err, ErrGone) {
		return nil
	}
	return err
}

// waitReady blocks until the host has answered anything at all.
func (h *Handle) waitReady(ctx context.Context, timeout time.Duration) error {
	// A status request is what proves the host is serving; its reply is also
	// what fills the mirrored Status in.
	if _, err := h.call(ctx, Request{Op: OpStatus}, timeout); err != nil {
		return err
	}
	return nil
}

// Detach drops the connection without touching the running session, which is
// what happens to every host when Socrates shuts down.
func (h *Handle) Detach() { _ = h.conn.Close() }
