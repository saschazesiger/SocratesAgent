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

// Refused is a subscription the host declined, in its own words. It is the one
// error class a caller must not retry: the host has looked at what was asked
// and said no, and asking again gets the same answer. Everything else that can
// end a subscription - a dropped connection, a host that went away mid-replay -
// is a connection losing its footing, and that is worth exactly one redial.
type Refused struct{ Reason string }

func (e *Refused) Error() string { return e.Reason }

// Is lets errors.Is see through to the sentinel a refusal names, so a caller
// can ask "was this the replay window?" without matching on text.
func (e *Refused) Is(target error) bool {
	return target == ErrReplayWindow && e.Reason == ErrReplayWindow.Error()
}

// sendTimeout is how long the engine waits for a send to be accepted. It has
// to cover the worst the host may spend inside one - restarting a crashed
// adapter and then handing it the message - or a slow resume fails the run
// here while the host goes on to open the turn anyway.
const sendTimeout = 4 * time.Minute

// Frame is one thing the host said, in the order it said it. Exactly one of
// the three fields is set. CaughtUp carries the status frame that closes a
// replay - "everything the host had written when you subscribed is now behind
// you" - and it travels on the SAME channel as the events precisely so that
// "behind you" means applied, not merely queued.
//
// Err is how a subscription fails in-band: the host refusing the subscribe
// ("replay window exceeded", when rotation has passed our floor) or the
// connection failing mid-replay. It is delivered as the LAST frame,
// immediately before the channel closes, so a consumer that reads to the end
// always learns why the stream ended and can tell a failure apart from a host
// that simply went away. Without it the consumer sees only a closed channel,
// redials a perfectly healthy host, gets the same refusal, and does it again.
type Frame struct {
	Event    *harness.Event
	CaughtUp *Status
	Err      error
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
	// subReqID is the id of the subscribe request whose reply closes the
	// replay. Only that one reply becomes a CaughtUp frame: a plain status
	// call - a Refresh from the API layer, say - would otherwise inject a
	// second one into a live subscription, and in adopt mode a stray CaughtUp
	// before the replay has reached the turn's end interrupts a healthy run.
	subReqID int64
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
		h.mu.Lock()
		isSubscribe := h.subReqID != 0 && resp.ID == h.subReqID
		sub := h.sub
		h.mu.Unlock()
		if isSubscribe && resp.Type == TypeOK {
			// The marker that opens a subscription. It is not the reply the
			// caller of Subscribe is waiting for - the status at the end of
			// the replay is - so it is not routed to the pending call.
			if sub != nil {
				sub.arm()
			}
			continue
		}
		if isSubscribe && resp.Type == TypeStatus && resp.Status != nil {
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
	// armed is closed when the host has begun answering this subscription.
	// Everything pushed before that belongs to the connection's past - the
	// host goes on broadcasting to a connection whose previous subscription
	// has ended - and delivering it would put an arbitrary prefix of some
	// other turn in front of this one's replay.
	armed     chan struct{}
	armedOnce sync.Once

	mu  sync.Mutex
	err error
}

func (s *subscription) stop() { s.once.Do(func() { close(s.done) }) }

func (s *subscription) arm() { s.armedOnce.Do(func() { close(s.armed) }) }

// ready reports whether the host has begun answering this subscription.
func (s *subscription) ready() bool {
	select {
	case <-s.armed:
		return true
	default:
		return false
	}
}

// fail records why this subscription is ending. The forwarder emits it as the
// last frame; it is not pushed through ch, because ch is taken by the read
// loop and the frame would race the close that follows it.
func (s *subscription) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *subscription) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// deliver hands a frame to the current subscriber, blocking until it is taken.
// Blocking is the point: it is how backpressure reaches the host. With no
// subscriber the frame is discarded rather than buffered.
//
// A subscription that ends while a frame is in flight hands it on to whatever
// subscribes next rather than dropping it. There is no unsubscribe op on the
// wire, so the host goes on pushing to a connection whose turn is over, and
// the next turn subscribes on that same connection: a frame dropped in the
// gap between the two is a journal event the new subscriber sees neither live
// nor in its replay, because its replay is already behind it on the wire. That
// is how the notice explaining a restart went missing once in twenty runs.
func (h *Handle) deliver(f Frame) {
	for {
		h.mu.Lock()
		s := h.sub
		h.mu.Unlock()
		if s == nil {
			return // nobody is listening, and nobody was told to expect it
		}
		if !s.ready() {
			return // this frame is from before the subscription began
		}
		select {
		case s.ch <- f:
			return
		case <-s.done:
			// That subscriber is gone. Look again: the next one may already
			// be waiting for exactly this.
		case <-h.done:
			return
		}
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
	h.pending = map[int64]chan Response{}
	sub := h.sub
	h.sub = nil
	h.mu.Unlock()

	// Closing done is what releases every call still waiting, and it is the
	// only thing that does. An earlier version also pushed a synthetic error
	// response into each pending channel, which cost nothing but a lie: a
	// caller cannot then tell the host having refused something from the
	// connection having gone, and those two deserve opposite answers - one is
	// final, the other is worth a redial.
	close(h.done)
	if sub != nil {
		sub.stop()
	}
	_ = h.conn.Close()
}

// call sends one request and waits for its reply. A caller that has to know
// the id up front - Subscribe, which has to recognise its own reply - assigns
// it with nextRequestID and passes it in.
func (h *Handle) call(ctx context.Context, req Request, timeout time.Duration) (Response, error) {
	if req.ID == 0 {
		req.ID = h.nextRequestID()
	}
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

func (h *Handle) nextRequestID() int64 { return h.seq.Add(1) }

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

// Alive reports whether the host process still answers on its socket. It is
// deliberately not "the adapter is up" - that is Status().Running, and the two
// are different things.
//
// A host whose CLI died keeps serving: the next send restarts the adapter
// against the session id spec.json now carries, which is the whole point of
// the design's crash story. Reading Alive as "the adapter is up" makes that
// path unreachable from Socrates - the engine skips the host, opens a second
// one beside it, and the first leaks forever with nothing left to close it.
func (h *Handle) Alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed
}

// Working reports whether the adapter behind this host can take a turn right
// now. Nothing decides a lifecycle on it; it is for describing a host.
func (h *Handle) Working() bool {
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
	resp, err := h.call(ctx, Request{Op: OpSend, TurnID: turnID, Text: text}, sendTimeout)
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
	s := &subscription{ch: make(chan Frame), done: make(chan struct{}), armed: make(chan struct{})}
	h.sub = s
	h.mu.Unlock()

	reqID := h.nextRequestID()
	h.mu.Lock()
	h.subReqID = reqID
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if h.sub == s {
			h.sub = nil
		}
		if h.subReqID == reqID {
			h.subReqID = 0
		}
		h.mu.Unlock()
		s.stop()
	}
	// The forwarder owns `out` and is the only thing that closes it, so a
	// caller ranging over it learns that the stream ended without anybody
	// having to close a channel a sender might still be writing to.
	go func() {
		defer close(out)
		// last delivers the reason this subscription ended, if there is one,
		// as the frame immediately before the channel closes. The timeout is
		// only for the case where the consumer has already stopped reading -
		// then nobody is waiting for the reason anyway.
		last := func() {
			err := s.failure()
			if err == nil {
				return
			}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case out <- Frame{Err: err}:
			case <-timer.C:
			}
		}
		for {
			select {
			case f := <-s.ch:
				select {
				case out <- f:
				case <-s.done:
					last()
					return
				}
			case <-s.done:
				last()
				return
			case <-h.done:
				// The host went away with nothing said about why. That is the
				// hiccup case - a dropped connection behind a healthy turn -
				// and the engine redials once. Only a refused subscribe
				// carries an Err, and that one is not retried.
				last()
				return
			}
		}
	}()
	go func() {
		resp, err := h.call(context.Background(), Request{ID: reqID, Op: OpSubscribe, FromSeq: from}, 5*time.Minute)
		if err == nil {
			return
		}
		// Either way there will be no caught-up frame, so the reason is handed
		// over as the last frame before the channel closes: a consumer that
		// saw only a closed channel would redial a healthy host and be refused
		// again. The two cases are kept apart, because they deserve opposite
		// answers - a refusal is final, a lost connection is worth one redial.
		if resp.Type == TypeError {
			s.fail(&Refused{Reason: resp.Error})
		} else {
			s.fail(err)
		}
		unsubscribe()
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
//
// The socket exists before the adapter has started, so a connection is not by
// itself proof of anything - and an adapter that fails to start would
// otherwise cost the whole timeout before its reason surfaced. So the wait
// also watches for final.json, which the host writes within milliseconds of
// giving up and which says why in a sentence written for a person.
func (h *Handle) waitReady(ctx context.Context, dir string, timeout time.Duration) error {
	// A status request is what proves the host is serving; its reply is also
	// what fills the mirrored Status in.
	result := make(chan error, 1)
	go func() {
		_, err := h.call(ctx, Request{Op: OpStatus}, timeout)
		result <- err
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ticker.C:
			if final, ok := readFinal(dir); ok && final.Status.Error != "" {
				return errors.New(final.Status.Error)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Detach drops the connection without touching the running session, which is
// what happens to every host when Socrates shuts down.
func (h *Handle) Detach() { _ = h.conn.Close() }
