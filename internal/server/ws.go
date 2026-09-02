package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The transport's constants, all of them from §D.
const (
	// wsSubprotocol names the framing this file implements, so that a client
	// built for a later one is refused by the handshake rather than by a
	// confusing frame.
	wsSubprotocol = "socrates.term.v1"
	// wsReadLimit is generous for keystrokes and small enough that a hostile
	// client cannot make the server allocate.
	wsReadLimit = 1 << 20

	// maxViewersPerSession bounds what a grace period can cost: eight idle
	// tmux clients and eight rings, and no more.
	maxViewersPerSession = 8

	// writeTimeout is the slow-reader guard. A client that cannot take a
	// frame in thirty seconds is closed and left to its grace.
	writeTimeout = 30 * time.Second
	// ackEvery batches input acknowledgements: one per hundred milliseconds,
	// carrying the highest sequence number accepted so far.
	ackEvery = 100 * time.Millisecond

	// handshakeBurst is how many sockets one address may open per minute.
	handshakeBurst  = 20
	handshakeWindow = time.Minute
)

// The three periods measured in tens of seconds. They are fields of the Server
// rather than constants for one reason: the tests that prove the grace and the
// watchdog have to run them, and a test that waited ninety seconds and then
// forty more is a test nobody runs.
const (
	// defaultViewerGrace is how long a dropped viewer keeps its tmux client. A
	// reconnect inside it is a pure gap in a byte stream; after it, a fresh
	// attach and a full redraw.
	defaultViewerGrace = 90 * time.Second
	// defaultPingEvery and defaultPingTimeout are the server half of the
	// watchdog; two failures in a row condemn the socket. A terminal is
	// legitimately silent for hours, so only the ping is a liveness signal.
	defaultPingEvery   = 15 * time.Second
	defaultPingTimeout = 10 * time.Second
)

// Frame kinds, the first byte of every binary frame.
const (
	frameOutput = 0x01
	frameInput  = 0x02
)

// ---------------------------------------------------------------- viewers

// termViewer is what the server remembers about one browser tab between
// sockets: its tmux client, its replay ring, and how much of its input has
// been written to the pane.
//
// It outlives the WebSocket by design. A phone in a car drops several times a
// minute, and a reconnect that finds this entry is a gap to fill rather than a
// terminal to repaint.
type termViewer struct {
	sessionID string
	viewerID  string

	mu sync.Mutex
	// viewer is the tmux client. It is replaced when a reconnect asks for
	// bytes the ring no longer holds, and closed when the grace expires.
	viewer *termux.Viewer
	// lastInput is the highest input sequence number written to the pane, and
	// sawInput whether any has been. Together they are the whole of the
	// dedupe: the server is the authority, and it tells the client on every
	// hello.
	lastInput uint64
	sawInput  bool
	// conn is the socket driving this viewer now, if any, and gone is closed
	// once its writer has stopped - which is what makes a takeover
	// synchronous.
	conn  *termConn
	grace *time.Timer
	// lag is the last output byte the client said it had rendered. Nothing in
	// the transport depends on it; the Diagnostics panel reads it.
	lag uint64
}

// termHub is every viewer the server currently remembers, keyed by session and
// viewer id.
type termHub struct {
	// grace is how long a dropped viewer keeps its terminal.
	grace time.Duration

	mu      sync.Mutex
	viewers map[string]*termViewer
}

func newTermHub() *termHub {
	return &termHub{grace: defaultViewerGrace, viewers: map[string]*termViewer{}}
}

func viewerKey(sessionID, viewerID string) string { return sessionID + "\x00" + viewerID }

// forSession is every remembered viewer of one session, for the frames the
// manager asks to be broadcast.
func (h *termHub) forSession(sessionID string) []*termViewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*termViewer
	for _, tv := range h.viewers {
		if tv.sessionID == sessionID {
			out = append(out, tv)
		}
	}
	return out
}

func (h *termHub) count(sessionID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, tv := range h.viewers {
		if tv.sessionID == sessionID {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------ connections

// termConn is one WebSocket: the single goroutine that writes to it, and the
// state that goroutine needs.
type termConn struct {
	tv   *termViewer
	conn *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc
	// stopped is closed when the writer has returned, so that a takeover can
	// wait for the old socket to be quiet before the new one says hello.
	stopped chan struct{}

	// ctrl carries JSON control frames to the writer. It is small and
	// deliberately so: a client that cannot take an acknowledgement is a
	// client that is going to be closed.
	ctrl chan []byte
	// data wakes the writer when the ring has moved. It is a signal and never
	// a buffer: the bytes stay in the ring, and a writer that wakes late takes
	// a bigger slice.
	data chan struct{}

	ackMu   sync.Mutex
	ackSeq  uint64
	ackWant bool

	// every and timeout are this socket's watchdog periods.
	every   time.Duration
	timeout time.Duration

	closeOnce sync.Once
	// taken is set by a takeover, and is how the goroutines of the socket
	// being replaced know that its ending has already been decided.
	taken atomic.Bool
}

func (c *termConn) wake() {
	select {
	case c.data <- struct{}{}:
	default:
	}
}

// send queues a control frame, dropping it if the socket is already going. The
// writer is the only goroutine that touches the connection.
func (c *termConn) send(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("terminal transport: could not encode a control frame: %v", err)
		return
	}
	select {
	case c.ctrl <- payload:
	case <-c.ctx.Done():
	default:
		// Thirty-two control frames the client has not taken is not a slow
		// link, it is a socket that has stopped. Blocking here would stall
		// whoever is broadcasting - the pane poll, another viewer's resize -
		// so this connection ends instead.
		c.fail(errors.New("this viewer stopped reading its control frames"))
	}
}

// noteAck records an accepted input sequence number for the batched
// acknowledgement.
func (c *termConn) noteAck(seq uint64) {
	c.ackMu.Lock()
	if seq > c.ackSeq {
		c.ackSeq = seq
	}
	c.ackWant = true
	c.ackMu.Unlock()
}

func (c *termConn) takeAck() (uint64, bool) {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	if !c.ackWant {
		return 0, false
	}
	c.ackWant = false
	return c.ackSeq, true
}

// ------------------------------------------------------------------ route

// handleSessionWS is the terminal transport: one WebSocket per browser tab,
// carrying pane output out and keystrokes in.
//
// The handshake is authenticated by the same cookie as every other call, and
// the origin is checked by Accept: cookies travel on a cross-origin WebSocket
// handshake, so without that check any page could open a terminal on this
// machine.
func (s *Server) handleSessionWS(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if err := s.throttle(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if !s.allowHandshake(ip) {
		writeError(w, http.StatusTooManyRequests, "too many terminal connections; slow down")
		return
	}

	row, ok := s.session(w, r)
	if !ok {
		return
	}
	cols, rows, err := wsSize(r, row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	viewerID := r.URL.Query().Get("viewer")
	if viewerID == "" {
		viewerID = termux.NewID()
	}
	if len(viewerID) > 64 {
		writeError(w, http.StatusBadRequest, "that viewer id is too long")
		return
	}
	if err := s.manager.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// Ensure is the whole of the recovery story: a session a reboot took away
	// is started again here, before a socket exists to be disappointed by it.
	ensureCtx, cancelEnsure := context.WithTimeout(r.Context(), createTimeout)
	ready, err := s.manager.Ensure(ensureCtx, row.ID)
	cancelEnsure()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{wsSubprotocol},
		CompressionMode: websocket.CompressionContextTakeover,
		OriginPatterns:  []string{r.Host},
	})
	if err != nil {
		// Accept has already answered - 403 for a foreign origin - and there
		// is nothing left to say.
		return
	}
	conn.SetReadLimit(wsReadLimit)

	if err := s.serveTerminal(r, conn, ready, viewerID, cols, rows); err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Printf("session %s: terminal transport: %v", ready.ID, err)
	}
}

// wsSize reads the viewer's terminal size from the query. Socrates owns the
// window size, so a size is never guessed and never zero: an absent one falls
// back to what the session is already wearing, which the store keeps above
// zero for exactly this reason.
func wsSize(r *http.Request, row *store.Session) (cols, rows int, err error) {
	read := func(name string, fallback int) (int, error) {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			return fallback, nil
		}
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 1 || n > 1000 {
			return 0, fmt.Errorf("%s must be a number between 1 and 1000", name)
		}
		return n, nil
	}
	if cols, err = read("cols", row.Cols); err != nil {
		return 0, 0, err
	}
	if rows, err = read("rows", row.Rows); err != nil {
		return 0, 0, err
	}
	if cols <= 0 {
		cols = store.DefaultCols
	}
	if rows <= 0 {
		rows = store.DefaultRows
	}
	return cols, rows, nil
}

// allowHandshake is the per-address ceiling on new sockets. It is separate
// from the password throttle above it - which this handler also honours - and
// it exists because a reconnect loop in a broken client must not be able to
// start a tmux client twenty times a second.
func (s *Server) allowHandshake(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	if s.wsRate == nil {
		s.wsRate = map[string]*attempt{}
	}
	a := s.wsRate[ip]
	if a == nil || now.After(a.until) {
		s.wsRate[ip] = &attempt{count: 1, until: now.Add(handshakeWindow)}
		return true
	}
	a.count++
	return a.count <= handshakeBurst
}

// ------------------------------------------------------------------ serve

// serveTerminal runs one socket from hello to close.
func (s *Server) serveTerminal(r *http.Request, conn *websocket.Conn, row *store.Session,
	viewerID string, cols, rows int) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tv, fresh, err := s.hub.acquire(ctx, s.manager, row, viewerID, cols, rows)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, truncateReason(err.Error()))
		return err
	}

	c := &termConn{
		tv: tv, conn: conn,
		ctx: ctx, cancel: cancel,
		stopped: make(chan struct{}),
		ctrl:    make(chan []byte, 32),
		data:    make(chan struct{}, 1),
		every:   s.pingEvery,
		timeout: s.pingTimeout,
	}

	// Taking over is synchronous: the previous writer has stopped before this
	// socket says hello, so no two goroutines ever write the same viewer's
	// stream. Half-open sockets are the ordinary mobile failure, and waiting
	// for the watchdog to notice would cost the user forty seconds of a
	// terminal that looks connected and does nothing.
	tv.takeOver(c)

	if !fresh {
		// A tab that comes back re-takes the window the way any attaching
		// viewer does, and at whatever size it is now: it may have been
		// rotated or had a keyboard opened while it was away.
		if previous := tv.current(); previous != nil {
			resizeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			if err := previous.Retake(resizeCtx, cols, rows); err != nil && !errors.Is(err, termux.ErrClosed) {
				log.Printf("session %s: viewer %s could not re-take its size: %v", row.ID, viewerID, err)
			}
			stop()
		}
	}

	since := parseSince(r)
	if fresh {
		since = 0
	}
	sent, replayed := tv.replayPoint(ctx, s.manager, row, since, cols, rows)

	viewer := tv.current()
	if viewer == nil {
		_ = conn.Close(websocket.StatusInternalError, "the terminal could not be attached")
		return errors.New("the viewer lost its terminal during the handshake")
	}

	go c.writeLoop(sent)
	go c.ringLoop(viewer)
	go c.pingLoop()

	c.send(helloFrame(row, viewer, tv, replayed, fresh))
	c.send(map[string]any{"t": "state", "state": row.State})
	if row.Resumed {
		c.send(resumeNotice(row))
	}
	if row.State == store.StateExited {
		c.send(map[string]any{"t": "exit", "status": row.ExitStatus, "at": row.UpdatedAt})
	}

	bye, err := c.readLoop()
	if !c.taken.Load() {
		c.shutdown(websocket.StatusNormalClosure, "")
	}
	cancel()
	<-c.stopped

	if bye {
		// A clean detach is the one case that does not deserve a grace: the
		// tab said it was leaving, so its tmux client goes with it.
		s.hub.closeViewer(tv)
		return nil
	}
	// A takeover has already handed this viewer to somebody else; only the
	// socket that still owns it starts the grace.
	s.hub.release(tv, c)
	return err
}

func parseSince(r *http.Request) uint64 {
	seq, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		return 0
	}
	return seq
}

// helloFrame is the first thing every socket hears, and the anchor for
// everything the client does afterwards.
func helloFrame(row *store.Session, viewer *termux.Viewer, tv *termViewer, replayFrom uint64, fresh bool) map[string]any {
	cols, rows := viewer.Size()
	tv.mu.Lock()
	ack := tv.lastInput
	tv.mu.Unlock()
	return map[string]any{
		"t":            "hello",
		"session":      row,
		"size":         map[string]int{"cols": cols, "rows": rows},
		"replay_from":  replayFrom,
		"input_ack":    ack,
		"viewer_fresh": fresh,
	}
}

// resumeNotice is the banner a session that came back from a reboot carries.
// fresh says whether the conversation itself survived: a verified id was
// resumed, anything else started a new one.
func resumeNotice(row *store.Session) map[string]any {
	return map[string]any{
		"t": "notice", "kind": "resumed",
		"fresh":        row.CLISessionState != store.CLIVerified,
		"resumed_from": row.CLISessionID,
		"cli":          row.Harness,
	}
}

func truncateReason(msg string) string {
	// A close reason is capped at 123 bytes by the protocol.
	if len(msg) > 120 {
		return msg[:120]
	}
	return msg
}

// ------------------------------------------------------------------- hub

// acquire finds this tab's viewer or makes one. The boolean is `viewer_fresh`:
// whether the server has no memory of this tab, and therefore cannot tell a
// resend from new input.
func (h *termHub) acquire(ctx context.Context, m *termux.Manager, row *store.Session,
	viewerID string, cols, rows int) (*termViewer, bool, error) {
	key := viewerKey(row.ID, viewerID)

	h.mu.Lock()
	if tv := h.viewers[key]; tv != nil {
		tv.mu.Lock()
		if tv.grace != nil {
			tv.grace.Stop()
			tv.grace = nil
		}
		alive := tv.viewer != nil
		tv.mu.Unlock()
		if alive {
			h.mu.Unlock()
			return tv, false, nil
		}
		delete(h.viewers, key)
	}
	h.mu.Unlock()

	if h.count(row.ID) >= maxViewersPerSession {
		return nil, false, fmt.Errorf("this session already has %d viewers", maxViewersPerSession)
	}

	viewer, err := m.Attach(ctx, row.ID, viewerID, cols, rows)
	if err != nil {
		return nil, false, err
	}
	tv := &termViewer{sessionID: row.ID, viewerID: viewerID, viewer: viewer}

	h.mu.Lock()
	existing := h.viewers[key]
	if existing == nil {
		h.viewers[key] = tv
	}
	h.mu.Unlock()
	if existing != nil {
		// Two handshakes for the same tab raced. The one that got there first
		// keeps the entry; this attach is undone rather than leaked.
		_ = viewer.Close()
		return existing, false, nil
	}
	return tv, true, nil
}

// release starts the grace once the socket that owned this viewer has gone.
//
// The tmux client stays for ninety seconds so that a reconnect is a gap and
// not a repaint, but the viewer stops owning the window size at once: a phone
// that drove out of coverage must not pin a laptop's terminal to its own size
// for a minute and a half.
func (h *termHub) release(tv *termViewer, c *termConn) {
	tv.mu.Lock()
	if tv.conn != c {
		tv.mu.Unlock()
		return // Somebody else took this viewer over.
	}
	tv.conn = nil
	viewer := tv.viewer
	if viewer == nil {
		tv.mu.Unlock()
		h.drop(tv)
		return
	}
	if tv.grace != nil {
		tv.grace.Stop()
	}
	tv.grace = time.AfterFunc(h.grace, func() { h.expire(tv) })
	tv.mu.Unlock()

	viewer.Idle()
}

// expire is the end of the grace: the tmux client goes, and with it every
// memory of this tab. A reconnect afterwards is a stranger, and its hello says
// so.
func (h *termHub) expire(tv *termViewer) {
	tv.mu.Lock()
	if tv.conn != nil {
		tv.mu.Unlock()
		return // It came back.
	}
	viewer := tv.viewer
	tv.viewer = nil
	tv.grace = nil
	tv.mu.Unlock()

	if viewer != nil {
		_ = viewer.Close()
	}
	h.drop(tv)
}

func (h *termHub) drop(tv *termViewer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.viewers[viewerKey(tv.sessionID, tv.viewerID)] == tv {
		delete(h.viewers, viewerKey(tv.sessionID, tv.viewerID))
	}
}

// closeViewer ends a viewer for good, which is what a clean detach asks for.
func (h *termHub) closeViewer(tv *termViewer) {
	tv.mu.Lock()
	viewer := tv.viewer
	tv.viewer = nil
	if tv.grace != nil {
		tv.grace.Stop()
		tv.grace = nil
	}
	tv.mu.Unlock()
	if viewer != nil {
		_ = viewer.Close()
	}
	h.drop(tv)
}

// takeOver hands this viewer to a new socket and waits for the old one to be
// quiet.
func (tv *termViewer) takeOver(c *termConn) {
	tv.mu.Lock()
	previous := tv.conn
	tv.conn = c
	tv.mu.Unlock()
	if previous == nil {
		return
	}
	// The writer of the socket being replaced stops before this one says
	// hello, so no two goroutines ever write the same viewer's stream. The
	// close frame goes out on its own, because the close handshake waits for
	// a peer that is, in the ordinary case, a phone that is no longer there.
	previous.taken.Store(true)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		previous.shutdown(websocket.StatusServiceRestart, "this terminal was taken over by a newer connection")
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		// The peer never answered the close handshake, which is exactly the
		// half-open socket this takeover exists for.
	}
	previous.cancel()
	<-previous.stopped
}

func (tv *termViewer) current() *termux.Viewer {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return tv.viewer
}

// replayPoint decides where this socket's byte stream starts. Sequence numbers
// are one-based - the first byte after an attach is 1 - so the ring offset the
// writer works from is exactly the number of bytes the client says it has.
//
// When the client is behind what the ring still holds there is no honest gap
// to send: the tmux client is replaced, which is a full redraw, and the hello
// says replay_from 0 so the terminal is reset before it.
func (tv *termViewer) replayPoint(ctx context.Context, m *termux.Manager, row *store.Session,
	since uint64, cols, rows int) (sent, replayFrom uint64) {
	viewer := tv.current()
	if viewer == nil {
		return 0, 0
	}
	if since == 0 {
		return 0, 0
	}
	if _, ok := viewer.Ring().Since(since); ok {
		return since, since
	}
	fresh, err := m.Attach(ctx, row.ID, tv.viewerID, cols, rows)
	if err != nil {
		log.Printf("session %s: could not re-attach viewer %s: %v", row.ID, tv.viewerID, err)
		return 0, 0
	}
	tv.mu.Lock()
	old := tv.viewer
	tv.viewer = fresh
	tv.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return 0, 0
}

// ---------------------------------------------------------------- writing

// ringLoop turns "the ring moved" into a wake-up for the writer. It never
// holds bytes of its own: the ring is the only buffer in the transport, and a
// client that falls behind falls behind in it.
func (c *termConn) ringLoop(viewer *termux.Viewer) {
	ring := viewer.Ring()
	seen := uint64(0)
	for {
		if err := ring.Wait(c.ctx, seen); err != nil {
			return
		}
		seen = ring.Head()
		c.wake()
		select {
		case <-c.ctx.Done():
			return
		case <-viewer.Done():
			// The tmux client ended: everything it produced is in the ring,
			// and the writer will take the rest of it before the socket goes.
			c.wake()
			return
		default:
		}
	}
}

// writeLoop is the one goroutine that writes to this socket. It pulls from the
// ring rather than being pushed to, which is what makes a stalled phone cost a
// fixed megabyte instead of an unbounded queue.
func (c *termConn) writeLoop(sent uint64) {
	defer close(c.stopped)
	acks := time.NewTicker(ackEvery)
	defer acks.Stop()

	flush := func() error {
		viewer := c.tv.current()
		if viewer == nil {
			return nil
		}
		for {
			chunk, ok := viewer.Ring().Since(sent)
			if !ok {
				// The client fell out of the ring after the handshake. There
				// is no correct frame to send; the socket goes and the next
				// one gets a fresh attach.
				return errors.New("this viewer fell behind its replay ring")
			}
			if len(chunk) == 0 {
				return nil
			}
			frame := make([]byte, 9+len(chunk))
			frame[0] = frameOutput
			binary.BigEndian.PutUint64(frame[1:9], sent+1)
			copy(frame[9:], chunk)
			if err := c.writeFrame(websocket.MessageBinary, frame); err != nil {
				return err
			}
			sent += uint64(len(chunk))
		}
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		case payload := <-c.ctrl:
			if err := c.writeFrame(websocket.MessageText, payload); err != nil {
				c.fail(err)
				return
			}
		case <-c.data:
			if err := flush(); err != nil {
				c.fail(err)
				return
			}
		case <-acks.C:
			seq, want := c.takeAck()
			if !want {
				continue
			}
			payload, err := json.Marshal(map[string]any{"t": "input_ack", "seq": seq})
			if err != nil {
				continue
			}
			if err := c.writeFrame(websocket.MessageText, payload); err != nil {
				c.fail(err)
				return
			}
		}
	}
}

// writeFrame writes one message under the slow-reader guard.
func (c *termConn) writeFrame(typ websocket.MessageType, payload []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, typ, payload)
}

// shutdown ends this socket with a definite status. The first caller decides
// the code, so that a takeover is seen as 1012 and not as whatever the losing
// goroutine noticed a moment later.
func (c *termConn) shutdown(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		_ = c.conn.Close(code, truncateReason(reason))
		c.cancel()
	})
}

// fail closes a socket the writer gave up on. 1013 is the honest code: come
// back later, your terminal is still here for ninety seconds.
func (c *termConn) fail(err error) {
	if c.taken.Load() || c.ctx.Err() != nil {
		return // How this socket ends has already been decided.
	}
	log.Printf("session %s: viewer %s dropped: %v", c.tv.sessionID, c.tv.viewerID, err)
	c.shutdown(websocket.StatusTryAgainLater, "this viewer could not keep up")
}

// pingLoop is the server half of the watchdog. Two consecutive failures close
// the socket; one is a phone going through a tunnel.
func (c *termConn) pingLoop() {
	ticker := time.NewTicker(c.every)
	defer ticker.Stop()
	misses := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
		err := c.conn.Ping(ctx)
		cancel()
		if err == nil {
			misses = 0
			continue
		}
		if c.ctx.Err() != nil {
			return
		}
		misses++
		if misses >= 2 {
			if !c.taken.Load() {
				c.shutdown(websocket.StatusPolicyViolation, "this viewer stopped answering")
			}
			return
		}
	}
}

// ---------------------------------------------------------------- reading

// readLoop is the socket's other half: keystrokes in, control frames in, and
// the end of the connection.
func (c *termConn) readLoop() (bye bool, err error) {
	for {
		typ, payload, err := c.conn.Read(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				return false, nil
			}
			if ordinaryEnd(err) {
				// A socket that simply vanished is the ordinary mobile case
				// and not a fault: the viewer keeps its terminal, and the
				// reconnect fills the gap.
				return false, nil
			}
			return false, err
		}
		if typ == websocket.MessageBinary {
			if err := c.onInput(payload); err != nil {
				return false, err
			}
			continue
		}
		if done := c.onControl(payload); done {
			return true, nil
		}
	}
}

// ordinaryEnd reports whether a read error is a connection ending rather than
// something going wrong.
func ordinaryEnd(err error) bool {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway,
		websocket.StatusServiceRestart, websocket.StatusTryAgainLater,
		websocket.StatusPolicyViolation:
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

// onInput writes a keystroke frame to the pane, exactly once.
//
// The server is the authority on what it has seen. A fresh viewer takes the
// first frame at whatever number it carries, a repeat is discarded, and a gap
// is refused with the last number that was accepted rather than written out of
// order into somebody's shell.
func (c *termConn) onInput(payload []byte) error {
	if len(payload) < 9 || payload[0] != frameInput {
		return errors.New("a binary frame that is not input")
	}
	seq := binary.BigEndian.Uint64(payload[1:9])
	body := payload[9:]

	tv := c.tv
	tv.mu.Lock()
	if !tv.sawInput {
		tv.sawInput = true
		if seq > 0 {
			tv.lastInput = seq - 1
		}
	}
	last := tv.lastInput
	viewer := tv.viewer
	switch {
	case seq <= last:
		tv.mu.Unlock()
		return nil // Already written; this is a resend.
	case seq > last+1:
		tv.mu.Unlock()
		c.send(map[string]any{"t": "input_ack", "seq": last})
		return nil
	}
	tv.mu.Unlock()

	if viewer == nil {
		c.send(map[string]any{"t": "error", "message": "this terminal is not attached", "fatal": true})
		return errors.New("input arrived for a viewer with no terminal")
	}
	if len(body) > 0 {
		if _, err := viewer.Write(body); err != nil {
			c.send(map[string]any{"t": "error", "message": err.Error(), "fatal": true})
			return err
		}
	}
	tv.mu.Lock()
	if seq > tv.lastInput {
		tv.lastInput = seq
	}
	tv.mu.Unlock()
	c.noteAck(seq)
	return nil
}

// onControl handles the JSON half of the client's side. The result is whether
// the client said goodbye.
func (c *termConn) onControl(payload []byte) bool {
	var frame struct {
		T          string `json:"t"`
		Cols, Rows int
		ID         int    `json:"id"`
		Seq        uint64 `json:"seq"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		c.send(map[string]any{"t": "error", "message": "that control frame is not JSON", "fatal": false})
		return false
	}
	switch frame.T {
	case "ping":
		c.send(map[string]any{"t": "pong", "id": frame.ID})
	case "lag":
		c.tv.mu.Lock()
		c.tv.lag = frame.Seq
		c.tv.mu.Unlock()
	case "resize":
		c.onResize(frame.Cols, frame.Rows)
	case "bye":
		return true
	}
	return false
}

// onResize moves the window, which under the manual policy is the only thing
// that ever does. A nonsense size is refused rather than passed on: tmux would
// take 0x0 and the store would not.
func (c *termConn) onResize(cols, rows int) {
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		c.send(map[string]any{"t": "error", "message": "a terminal size must be between 1 and 1000", "fatal": false})
		return
	}
	viewer := c.tv.current()
	if viewer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := viewer.Resize(ctx, cols, rows); err != nil && !errors.Is(err, termux.ErrClosed) {
		log.Printf("session %s: could not resize to %dx%d: %v", c.tv.sessionID, cols, rows, err)
	}
}

// --------------------------------------------------------------- broadcast

// onSessionExit turns a dead pane into the overlay every viewer of that
// session draws. It is the Manager's OnExit, wired here because the frame is
// the transport's business and the pane is not.
func (s *Server) onSessionExit(sessionID string, status int) {
	at := time.Now().UnixMilli()
	s.broadcast(sessionID, func(*termViewer) any {
		return map[string]any{"t": "exit", "status": status, "at": at}
	})
}

// onSessionSize tells every viewer that the window moved, and which of them
// asked for it. Socrates issues every resize, so this fires once per real
// change and there is no size poll anywhere.
func (s *Server) onSessionSize(sessionID string, cols, rows int, owner string) {
	s.broadcast(sessionID, func(tv *termViewer) any {
		by := "other"
		if tv.viewerID == owner {
			by = "self"
		}
		return map[string]any{"t": "size", "cols": cols, "rows": rows, "by": by}
	})
}

func (s *Server) broadcast(sessionID string, frame func(*termViewer) any) {
	for _, tv := range s.hub.forSession(sessionID) {
		tv.mu.Lock()
		c := tv.conn
		tv.mu.Unlock()
		if c != nil {
			c.send(frame(tv))
		}
	}
}
