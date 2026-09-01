package term

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
)

// ErrGone is returned when the host process of a session is no longer there.
var ErrGone = errors.New("this terminal session is no longer running")

// Handle is Socrates' side of a session: a connection to the host process that
// owns the pseudo terminal. It mirrors the latest screen so the web UI can be
// served without a round trip, and it forwards commands to the host.
type Handle struct {
	id     string
	name   string
	chatID string
	meta   map[string]string

	conn net.Conn
	enc  *json.Encoder

	seq      atomic.Int64
	mu       sync.Mutex
	pending  map[int64]chan Response
	state    State
	watchers map[chan State]struct{}
	closed   bool
	done     chan struct{}
	// ready closes when the host has pushed its first screen, so callers
	// never see a handle whose state is still empty.
	ready     chan struct{}
	readyOnce sync.Once
}

func newHandle(id, name, chatID string, meta map[string]string, conn net.Conn) *Handle {
	if meta == nil {
		meta = map[string]string{}
	}
	h := &Handle{
		id: id, name: name, chatID: chatID, meta: meta,
		conn: conn, enc: json.NewEncoder(conn),
		pending:  map[int64]chan Response{},
		watchers: map[chan State]struct{}{},
		done:     make(chan struct{}),
		ready:    make(chan struct{}),
	}
	go h.read()
	return h
}

func (h *Handle) read() {
	scanner := bufio.NewScanner(h.conn)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		var resp Response
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.State != nil {
			h.readyOnce.Do(func() { close(h.ready) })
			h.mu.Lock()
			h.state = *resp.State
			state := h.state
			watchers := make([]chan State, 0, len(h.watchers))
			for ch := range h.watchers {
				watchers = append(watchers, ch)
			}
			h.mu.Unlock()
			for _, ch := range watchers {
				select {
				case ch <- state:
				default:
				}
			}
		}
		if resp.ID == 0 {
			continue
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

func (h *Handle) markGone() {
	h.readyOnce.Do(func() { close(h.ready) })
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.state.Running = false
	pending := h.pending
	h.pending = map[int64]chan Response{}
	watchers := make([]chan State, 0, len(h.watchers))
	for ch := range h.watchers {
		watchers = append(watchers, ch)
	}
	state := h.state
	h.mu.Unlock()

	for _, ch := range pending {
		ch <- Response{Type: TypeError, Error: ErrGone.Error()}
	}
	for _, ch := range watchers {
		select {
		case ch <- state:
		default:
		}
	}
	close(h.done)
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
		return Response{}, fmt.Errorf("the terminal session did not answer within %s", timeout)
	case <-h.done:
		return Response{}, ErrGone
	}
}

// ID, Name and ChatID identify the session.
func (h *Handle) ID() string     { return h.id }
func (h *Handle) Name() string   { return h.name }
func (h *Handle) ChatID() string { return h.chatID }

// Meta returns what the caller attached to the session when it was opened.
func (h *Handle) Meta(key string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.meta[key]
}

// setMeta is called by the manager, which also persists the change.
func (h *Handle) setMeta(key, value string) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.meta[key] = value
	out := make(map[string]string, len(h.meta))
	for k, v := range h.meta {
		out[k] = v
	}
	return out
}

// State returns the last screen the host pushed.
func (h *Handle) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// Alive reports whether the session is still usable.
func (h *Handle) Alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && h.state.Running
}

// Watch returns a channel that receives every screen change. Cancel it with
// Unwatch when the subscriber goes away.
func (h *Handle) Watch() chan State {
	ch := make(chan State, 4)
	h.mu.Lock()
	h.watchers[ch] = struct{}{}
	state := h.state
	h.mu.Unlock()
	select {
	case ch <- state:
	default:
	}
	return ch
}

// Unwatch stops a subscription started with Watch.
func (h *Handle) Unwatch(ch chan State) {
	h.mu.Lock()
	delete(h.watchers, ch)
	h.mu.Unlock()
}

// Refresh asks the host for the current screen rather than the cached one.
func (h *Handle) Refresh(ctx context.Context) (State, error) {
	resp, err := h.call(ctx, Request{Op: OpState}, 10*time.Second)
	if err != nil {
		return h.State(), err
	}
	if resp.State == nil {
		return h.State(), nil
	}
	return *resp.State, nil
}

// Type sends text as if it had been typed at the keyboard.
func (h *Handle) Type(ctx context.Context, text string) error {
	_, err := h.call(ctx, Request{Op: OpInput, Data: text}, 10*time.Second)
	return err
}

// SendKeys presses named keys in order.
func (h *Handle) SendKeys(ctx context.Context, keys []string) error {
	_, err := h.call(ctx, Request{Op: OpKeys, Keys: keys}, 10*time.Second)
	return err
}

// Resize changes the window size the program sees.
func (h *Handle) Resize(ctx context.Context, cols, rows int) error {
	_, err := h.call(ctx, Request{Op: OpResize, Cols: cols, Rows: rows}, 10*time.Second)
	return err
}

// Output returns the plain text transcript of the session.
func (h *Handle) Output(ctx context.Context, max int) (string, error) {
	resp, err := h.call(ctx, Request{Op: OpOutput, Max: max}, 20*time.Second)
	if err != nil {
		return "", err
	}
	return resp.Output, nil
}

// WaitIdle blocks until the program has been quiet for the given time.
func (h *Handle) WaitIdle(ctx context.Context, quiet, limit time.Duration) (bool, State, error) {
	resp, err := h.call(ctx, Request{
		Op: OpIdle, QuietMS: int(quiet.Milliseconds()), LimitMS: int(limit.Milliseconds()),
	}, limit+30*time.Second)
	if err != nil {
		return false, h.State(), err
	}
	state := h.State()
	if resp.State != nil {
		state = *resp.State
	}
	return resp.Matched, state, nil
}

// WaitFor blocks until the screen matches pattern.
func (h *Handle) WaitFor(ctx context.Context, pattern string, limit time.Duration) (bool, State, error) {
	resp, err := h.call(ctx, Request{Op: OpWait, Pattern: pattern, LimitMS: int(limit.Milliseconds())},
		limit+30*time.Second)
	if err != nil {
		return false, h.State(), err
	}
	state := h.State()
	if resp.State != nil {
		state = *resp.State
	}
	return resp.Matched, state, nil
}

// WaitChange blocks until the session has produced output beyond the given
// revision. It answers a different question from WaitIdle: not "has it
// finished" but "did it react at all", which is what you need straight after
// pressing a key. It returns false if nothing happened within limit.
func (h *Handle) WaitChange(ctx context.Context, since int64, limit time.Duration) (State, bool) {
	if state := h.State(); state.Revision > since {
		return state, true
	}
	updates := h.Watch()
	defer h.Unwatch(updates)

	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case state, open := <-updates:
			if !open {
				return h.State(), h.State().Revision > since
			}
			if state.Revision > since {
				return state, true
			}
		case <-ctx.Done():
			return h.State(), false
		case <-h.done:
			return h.State(), h.State().Revision > since
		case <-timer.C:
			return h.State(), false
		}
	}
}

// Interrupt presses Ctrl+C.
func (h *Handle) Interrupt(ctx context.Context) error {
	_, err := h.call(ctx, Request{Op: OpSignal}, 10*time.Second)
	return err
}

// Close ends the session and its host process.
func (h *Handle) Close(ctx context.Context, grace time.Duration) error {
	if grace <= 0 {
		grace = 5 * time.Second
	}
	_, err := h.call(ctx, Request{Op: OpClose, LimitMS: int(grace.Milliseconds())}, grace+15*time.Second)
	if errors.Is(err, ErrGone) {
		return nil
	}
	return err
}

// waitReady blocks until the host has sent the first screen.
func (h *Handle) waitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("the terminal session did not report its state within %s", timeout)
	}
}

// Detach drops the connection without touching the running program, which is
// what happens to every session when Socrates shuts down.
func (h *Handle) Detach() { _ = h.conn.Close() }
