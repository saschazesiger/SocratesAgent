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

	// ackEvery batches input acknowledgements: one per hundred milliseconds,
	// carrying the highest sequence number accepted so far.
	ackEvery = 100 * time.Millisecond
	// maxInputFailures is how many input frames in a row a pane may refuse
	// before the socket is ended and a new handshake is left to attach another
	// terminal. One failure is a frame, several in a row are a terminal.
	maxInputFailures = 3

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
	// defaultWriteTimeout is the slow-reader guard: a client that cannot take
	// one frame in thirty seconds is given up on and left to its grace.
	defaultWriteTimeout = 30 * time.Second
	// closeHandshakeGrace is how long a close waits for the peer's reply
	// before the socket is taken down without it.
	closeHandshakeGrace = time.Second
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
	// attaching is set while this entry's tmux client is being started, so a
	// second handshake for the same tab in that window is told to try again
	// rather than quietly starting a second terminal.
	attaching bool

	// sizeMu orders the two things that move the window on this viewer's
	// behalf: the hand-over when its socket is lost, and the re-take when one
	// comes back. It is a lock of its own because both of them end in the
	// Manager, which broadcasts a size frame to every viewer of the session -
	// and a broadcast that had to wait for mu would wait for the goroutine
	// holding it, which is this one.
	sizeMu sync.Mutex
	// generation counts the times this viewer has been claimed. A grace timer
	// that fires while a reconnect is being served carries the generation it
	// was armed for, and is ignored if the viewer has been claimed since.
	generation uint64
	// lag is the last output byte the client said it had rendered. Nothing in
	// the transport depends on it; the Diagnostics panel reads it.
	lag uint64
	// pasting is whether this tab's input frames ended inside a bracketed
	// paste, so that a paste split across frames is delivered as the text it
	// is rather than read as a terminal talking.
	pasting bool
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
	return h.sessionViewers(sessionID)
}

// all is every remembered viewer, for the one frame that goes everywhere.
func (h *termHub) all() []*termViewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*termViewer, 0, len(h.viewers))
	for _, tv := range h.viewers {
		out = append(out, tv)
	}
	return out
}

func (h *termHub) count(sessionID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessionViewers(sessionID))
}

// sessionViewers is forSession without the lock, for callers that hold it.
func (h *termHub) sessionViewers(sessionID string) []*termViewer {
	var out []*termViewer
	for _, tv := range h.viewers {
		if tv.sessionID == sessionID {
			out = append(out, tv)
		}
	}
	return out
}

// ------------------------------------------------------------ connections

// termConn is one WebSocket: the single goroutine that writes to it, and the
// state that goroutine needs.
type termConn struct {
	tv   *termViewer
	conn *websocket.Conn
	// srv is the server this socket belongs to. A socket has to reach the
	// manager for the two things the client asks of it that are not bytes: a
	// keystroke clears an unread mark, and so does a `read` frame.
	srv *Server

	ctx    context.Context
	cancel context.CancelFunc
	// wctx is the writing half's own lifetime. It exists so that a takeover
	// can stop the writer without cancelling the read side: cancelling a
	// context a Write is blocked on hard-closes the socket, and a hard close
	// is exactly what a 1012 must not become.
	wctx    context.Context
	wcancel context.CancelFunc
	// stopped is closed when the writer has returned, so that a takeover can
	// wait for the old socket to be quiet before the new one says hello.
	stopped  chan struct{}
	stopOnce sync.Once
	// quit asks the writer to stop between frames. It is the graceful half of
	// what wcancel does by force: cancelling the context a Write is blocked on
	// makes the library abort the connection, and an aborted peer sees 1006
	// rather than the status this socket was about to close with.
	quit     chan struct{}
	quitOnce sync.Once

	// ctrl carries JSON control frames to the writer. It is small and
	// deliberately so: a client that cannot take an acknowledgement is a
	// client that is going to be closed.
	ctrl chan []byte
	// data wakes the writer when the ring has moved. It is a signal and never
	// a buffer: the bytes stay in the ring, and a writer that wakes late takes
	// a bigger slice.
	data chan struct{}
	// last carries a frame that is the last thing this socket will say, with
	// the status to close on once it is out. It is how a deleted session is
	// announced rather than merely disconnected.
	last chan farewell

	ackMu   sync.Mutex
	ackSeq  uint64
	ackWant bool

	// every and timeout are this socket's watchdog periods, and slow is how
	// long one frame may take to leave before the viewer is given up on.
	every   time.Duration
	timeout time.Duration
	slow    time.Duration
	// overdue is set when a write has been stuck for that long.
	overdue atomic.Bool

	// inputFails counts input frames the pane refused in a row. Only the read
	// loop touches it, and the read loop is one goroutine.
	inputFails int

	closeOnce sync.Once
	// taken is set by a takeover, and is how the goroutines of the socket
	// being replaced know that its ending has already been decided.
	taken atomic.Bool
}

// farewell is a final control frame and the status that follows it.
type farewell struct {
	payload []byte
	code    websocket.StatusCode
	reason  string
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
		// so this connection ends instead, and it ends on a goroutine of its
		// own: no Manager goroutine ever waits on a socket.
		go c.fail(errors.New("this viewer stopped reading its control frames"))
	}
}

// sendLast queues the last frame this socket will send, and the status to
// close with once it has gone out.
func (c *termConn) sendLast(v any, code websocket.StatusCode, reason string) {
	payload, err := json.Marshal(v)
	if err != nil {
		go c.shutdown(code, reason)
		return
	}
	select {
	case c.last <- farewell{payload: payload, code: code, reason: reason}:
	case <-c.ctx.Done():
	default:
		go c.shutdown(code, reason)
	}
}

// writeNow puts a control frame on the wire before the writer goroutine
// exists. hello is written this way, and so is everything that has to follow
// it immediately: the anchor a client resets and renumbers from cannot arrive
// behind the output it is the anchor for.
func (c *termConn) writeNow(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(websocket.MessageText, payload)
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
	if !s.allowHandshake(ip, s.hub.remembers(row.ID, viewerID)) {
		writeError(w, http.StatusTooManyRequests, "too many terminal connections; slow down")
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

// allowHandshake is the per-address ceiling on sockets that start a new
// terminal. It is separate from the password throttle above it - which this
// handler also honours - and it exists because a broken client must not be
// able to start a tmux client twenty times a second.
//
// A reconnect of a viewer the hub still remembers does not count against it.
// That is the whole design: a phone in a car drops several times a minute, and
// a ceiling that locked the terminal for a minute after a bad stretch of road
// would punish exactly the user this transport is built for. Only handshakes
// that would attach something new are counted.
func (s *Server) allowHandshake(ip string, known bool) bool {
	if known {
		return true
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	if s.wsRate == nil {
		s.wsRate = map[string]*attempt{}
	}
	for addr, a := range s.wsRate {
		if now.After(a.until) {
			delete(s.wsRate, addr)
		}
	}
	a := s.wsRate[ip]
	if a == nil {
		s.wsRate[ip] = &attempt{count: 1, until: now.Add(handshakeWindow)}
		return true
	}
	a.count++
	return a.count <= handshakeBurst
}

// remembers reports whether the hub already holds this tab's viewer, which is
// what makes a handshake a reconnect rather than a new terminal.
func (h *termHub) remembers(sessionID, viewerID string) bool {
	if viewerID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.viewers[viewerKey(sessionID, viewerID)] != nil
}

// ------------------------------------------------------------------ serve

// serveTerminal runs one socket from hello to close.
func (s *Server) serveTerminal(r *http.Request, conn *websocket.Conn, row *store.Session,
	viewerID string, cols, rows int) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tv, fresh, redrawn, err := s.hub.acquire(ctx, s.manager, row, viewerID, cols, rows)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, truncateReason(err.Error()))
		return err
	}

	wctx, wcancel := context.WithCancel(ctx)
	c := &termConn{
		tv: tv, conn: conn, srv: s,
		ctx: ctx, cancel: cancel,
		wctx: wctx, wcancel: wcancel,
		stopped: make(chan struct{}),
		quit:    make(chan struct{}),
		ctrl:    make(chan []byte, 32),
		data:    make(chan struct{}, 1),
		last:    make(chan farewell, 1),
		every:   s.pingEvery,
		timeout: s.pingTimeout,
		slow:    s.writeTimeout,
	}
	defer wcancel()

	// Taking over is synchronous: the previous writer has stopped before this
	// socket says hello, so no two goroutines ever write the same viewer's
	// stream. Half-open sockets are the ordinary mobile failure, and waiting
	// for the watchdog to notice would cost the user forty seconds of a
	// terminal that looks connected and does nothing.
	tv.takeOver(c)

	since := parseSince(r)
	// A terminal that was attached a moment ago - because this tab's own one
	// had ended - is this client's redraw, exactly as a first attach is.
	if fresh || redrawn {
		since = 0
	}
	sent, replayed := tv.replayPoint(ctx, s.manager, row, since, cols, rows, fresh || redrawn)

	// abandon is how every handshake that ends before the writer exists has to
	// end: the writer that was never started is marked stopped, and the viewer
	// is released into its grace. Returning without either is what strands a
	// tab - the entry keeps a connection nothing is reading, and the next
	// handshake for it waits for that connection's writer for ever.
	abandon := func(err error, code websocket.StatusCode, reason string) error {
		c.writerStopped()
		c.shutdown(code, reason)
		s.hub.release(tv, c)
		return err
	}

	viewer := tv.current()
	if viewer == nil {
		return abandon(errors.New("the viewer lost its terminal during the handshake"),
			websocket.StatusInternalError, "the terminal could not be attached")
	}

	// hello goes out before anything else can: it is the anchor the client
	// resets its terminal and renumbers its held keystrokes from, and output
	// that overtook it would be rendered into a screen that is about to be
	// cleared. Nothing writes to this socket yet, so these four are simply
	// written where they stand.
	if err := c.writeNow(helloFrame(s, row, viewer, tv, replayed, fresh)); err != nil {
		return abandon(err, websocket.StatusTryAgainLater, "this terminal could not be started")
	}
	if err := c.writeNow(map[string]any{"t": "state", "state": row.State}); err != nil {
		return abandon(err, websocket.StatusTryAgainLater, "this terminal could not be started")
	}
	if row.Resumed {
		if err := c.writeNow(s.resumeNotice(row)); err != nil {
			return abandon(err, websocket.StatusTryAgainLater, "this terminal could not be started")
		}
	}
	if row.State == store.StateExited {
		if err := c.writeNow(map[string]any{"t": "exit", "status": row.ExitStatus, "at": row.UpdatedAt}); err != nil {
			return abandon(err, websocket.StatusTryAgainLater, "this terminal could not be started")
		}
	}

	if !fresh && !redrawn {
		// A tab that comes back re-takes the window the way any attaching
		// viewer does, and at whatever size it is now: it may have been
		// rotated or had a keyboard opened while it was away. It happens after
		// hello so that the size frame it may broadcast is in order behind it.
		resizeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		if err := tv.retake(resizeCtx, cols, rows); err != nil && !errors.Is(err, termux.ErrClosed) {
			log.Printf("session %s: viewer %s could not re-take its size: %v", row.ID, viewerID, err)
		}
		stop()
	}

	go c.writeLoop(sent)
	go c.ringLoop(viewer)
	go c.pingLoop()

	bye, err := c.readLoop()
	var protocol *protocolError
	switch {
	case c.taken.Load():
		// The status was decided by whoever took this socket over, and its
		// writer was stopped there.
	case errors.As(err, &protocol):
		c.finish(websocket.StatusProtocolError, protocol.Error())
	default:
		c.finish(websocket.StatusNormalClosure, "")
	}
	<-c.stopped
	wcancel()
	cancel()

	if (bye || errors.As(err, &protocol)) && !c.taken.Load() {
		// A clean detach is the one case that does not deserve a grace: the
		// tab said it was leaving, so its tmux client goes with it. A client
		// that broke the framing gets the same treatment for a different
		// reason: it must not be able to fill the eight viewer slots with
		// eight bad frames.
		s.hub.closeViewer(tv)
		return err
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
func helloFrame(s *Server, row *store.Session, viewer *termux.Viewer, tv *termViewer, replayFrom uint64, fresh bool) map[string]any {
	cols, rows := viewer.Size()
	tv.mu.Lock()
	ack := tv.lastInput
	tv.mu.Unlock()
	return map[string]any{
		"t": "hello",
		// The session is the envelope every other endpoint answers with, so
		// that the browser has one shape for a session and not two.
		"session":      s.view(row),
		"size":         map[string]int{"cols": cols, "rows": rows},
		"replay_from":  replayFrom,
		"input_ack":    ack,
		"viewer_fresh": fresh,
		// Every running session's activity, not just this one's: the sidebar
		// is drawn from a reconnect without a request of its own.
		"activity": s.manager.Activities(),
	}
}

// resumeNotice is the banner a session that came back from a reboot carries.
// fresh says whether the conversation itself survived: what the resume
// actually did is recorded by the Manager, so the frame and the session list
// cannot disagree about it.
func (s *Server) resumeNotice(row *store.Session) map[string]any {
	frame := map[string]any{
		"t": "notice", "kind": "resumed",
		"fresh": true,
		"cli":   row.Harness,
	}
	if note, ok := s.manager.ResumeNoteOf(row.ID); ok {
		frame["fresh"] = note.Fresh
		frame["resumed_from"] = note.From
	}
	return frame
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
	viewerID string, cols, rows int) (tv *termViewer, fresh, redrawn bool, err error) {
	key := viewerKey(row.ID, viewerID)

	h.mu.Lock()
	if tv := h.viewers[key]; tv != nil {
		tv.mu.Lock()
		if tv.grace != nil {
			tv.grace.Stop()
			tv.grace = nil
		}
		// The generation moves whether or not the timer was stopped in time:
		// one already running and blocked on this lock must not close a
		// terminal the reconnect is about to be handed.
		tv.generation++
		viewer, attaching := tv.viewer, tv.attaching
		// A remembered tmux client that has ended is not a terminal: it is a
		// ring nobody will ever add to and a file no keystroke will ever reach
		// again. Treating it as alive is what left a tab watching a screen
		// that still looked right while everything it typed failed - for ever,
		// because the entry survived every reconnect and every reconnect chose
		// it again. So the entry is kept, because it holds the input counter
		// that makes the tab's held keystrokes deliverable exactly once, and
		// the terminal under it is replaced.
		dead := viewer != nil && viewer.Ended()
		if dead {
			tv.viewer = nil
			tv.attaching = true
		}
		alive := viewer != nil && !dead
		tv.mu.Unlock()
		if alive {
			h.mu.Unlock()
			return tv, false, false, nil
		}
		if dead {
			h.mu.Unlock()
			replacement, err := m.Attach(ctx, row.ID, viewerID, cols, rows)
			tv.mu.Lock()
			tv.attaching = false
			tv.viewer = replacement
			// The paste state belonged to the terminal that is gone.
			tv.pasting = false
			tv.mu.Unlock()
			go func() { _ = viewer.Close() }()
			if err != nil {
				h.drop(tv)
				return nil, false, false, err
			}
			// Not fresh - the server still knows exactly how much of this
			// tab's input it has written - but everything on screen came from
			// a terminal that no longer exists, so it is redrawn.
			return tv, false, true, nil
		}
		if attaching {
			h.mu.Unlock()
			return nil, false, false, errors.New("this viewer is still being attached; try again")
		}
		delete(h.viewers, key)
	}
	if len(h.sessionViewers(row.ID)) >= maxViewersPerSession {
		h.mu.Unlock()
		return nil, false, false, fmt.Errorf("this session already has %d viewers", maxViewersPerSession)
	}
	// The slot is claimed before the attach, which takes tmux commands and a
	// pseudo terminal: two handshakes racing must make eight viewers, not
	// nine.
	tv = &termViewer{sessionID: row.ID, viewerID: viewerID, attaching: true}
	h.viewers[key] = tv
	h.mu.Unlock()

	viewer, err := m.Attach(ctx, row.ID, viewerID, cols, rows)
	tv.mu.Lock()
	tv.attaching = false
	tv.viewer = viewer
	tv.mu.Unlock()
	if err != nil {
		h.drop(tv)
		return nil, false, false, err
	}
	return tv, true, false, nil
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
	generation := tv.generation
	tv.grace = time.AfterFunc(h.grace, func() { h.expire(tv, generation) })
	tv.mu.Unlock()

	// The window is handed on under sizeMu, so that it cannot cross with the
	// re-take of a socket that is coming back: whichever gets there first, the
	// other sees it. A reconnect that has already claimed this viewer keeps
	// what it took.
	tv.sizeMu.Lock()
	defer tv.sizeMu.Unlock()
	tv.mu.Lock()
	claimed := tv.conn != nil || tv.generation != generation
	tv.mu.Unlock()
	if !claimed {
		viewer.Idle()
	}
}

// expire is the end of the grace: the tmux client goes, and with it every
// memory of this tab. A reconnect afterwards is a stranger, and its hello says
// so.
func (h *termHub) expire(tv *termViewer, generation uint64) {
	tv.mu.Lock()
	if tv.conn != nil || tv.generation != generation {
		tv.mu.Unlock()
		return // It came back, or is coming back as this timer fires.
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

// endSession is what a deleted session does to the browsers watching it: they
// are told, in a frame, and then closed - rather than left holding a terminal
// that no longer exists until the next keystroke fails.
//
// It also takes the grace entries with it, because those are the viewers
// nothing else can reach: their terminals were closed with the session, but
// their timers, rings and entries would otherwise sit here for ninety seconds
// (§A.10 step 1).
func (h *termHub) endSession(sessionID, why string) {
	for _, tv := range h.forSession(sessionID) {
		tv.mu.Lock()
		c := tv.conn
		tv.mu.Unlock()
		if c != nil {
			c.sendLast(map[string]any{"t": "error", "message": why, "fatal": true},
				websocket.StatusGoingAway, why)
		}
		h.closeViewer(tv)
	}
}

// endingViewers wraps the delete route: a session that has just been killed
// must not leave sockets open on a terminal that is gone. It is a wrapper
// rather than a line in the handler so that the transport owns its own
// clean-up, and it acts only on a delete that actually succeeded.
func (s *Server) endingViewers(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next(recorder, r)
		if recorder.code >= 200 && recorder.code < 300 {
			s.hub.endSession(r.PathValue("id"), "this session was deleted")
		}
	}
}

// statusWriter remembers the status code a handler wrote.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
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
	// A bracketed paste that was cut in half by a lost socket has no second
	// half: the client sends a paste as one frame, so anything arriving on a
	// new connection is a new frame. Carrying the flag over would leave this
	// viewer reading every terminal report as pasted text for as long as the
	// tab lived.
	tv.pasting = false
	tv.mu.Unlock()
	if previous == nil {
		return
	}
	// The writer of the socket being replaced stops before this one says
	// hello, so no two goroutines ever write the same viewer's stream. The
	// close frame goes out on its own, because the close handshake waits for
	// a peer that is, in the ordinary case, a phone that is no longer there.
	previous.taken.Store(true)
	// Only the writer is stopped synchronously, and the read side is left
	// alone: cancelling it would tear the socket down before the 1012 could
	// be written, and waiting for the close handshake would cost this
	// handshake the two seconds the half-open case is all about. The writer
	// is asked rather than cancelled for the same reason (see finish).
	previous.finish(websocket.StatusServiceRestart, "this terminal was taken over by a newer connection")
}

// retake re-takes the window for a returning socket, under the lock that
// orders it against the hand-over of the socket it is replacing.
func (tv *termViewer) retake(ctx context.Context, cols, rows int) error {
	tv.sizeMu.Lock()
	defer tv.sizeMu.Unlock()
	viewer := tv.current()
	if viewer == nil {
		return nil
	}
	return viewer.Retake(ctx, cols, rows)
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
//
// A returning viewer that says `since=0` is in the same position, and it is
// the ordinary page reload: the tab kept its viewer id but its terminal is
// empty, so it needs the screen and not the ring. Replaying the ring to it
// would be wrong twice over - it is the whole session rather than the current
// screen, and it re-delivers the device-attribute queries tmux wrote on the
// first attach, which the terminal would answer a second time into a pane
// that never asked.
func (tv *termViewer) replayPoint(ctx context.Context, m *termux.Manager, row *store.Session,
	since uint64, cols, rows int, brandNew bool) (sent, replayFrom uint64) {
	viewer := tv.current()
	if viewer == nil {
		return 0, 0
	}
	if brandNew {
		// The attach that just happened is this client's redraw.
		return 0, 0
	}
	if since != 0 {
		if _, ok := viewer.Ring().Since(since); ok {
			return since, since
		}
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
		if err := ring.Wait(c.wctx, seen); err != nil {
			return
		}
		seen = ring.Head()
		c.wake()
		select {
		case <-c.wctx.Done():
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
	defer c.writerStopped()
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
		// Control frames first, always. They are small, they are ordered
		// among themselves, and a client that is behind on output must still
		// hear that the pane died or that the window moved.
		select {
		case <-c.wctx.Done():
			return
		case <-c.quit:
			return
		case payload := <-c.ctrl:
			if err := c.writeFrame(websocket.MessageText, payload); err != nil {
				c.fail(err)
				return
			}
			continue
		default:
		}

		select {
		case <-c.wctx.Done():
			return
		case <-c.quit:
			return
		case payload := <-c.ctrl:
			if err := c.writeFrame(websocket.MessageText, payload); err != nil {
				c.fail(err)
				return
			}
		case bye := <-c.last:
			// The last thing this socket says, and then the status it says it
			// with: a session that was deleted is announced, not merely
			// disconnected.
			if err := c.writeFrame(websocket.MessageText, bye.payload); err != nil {
				c.fail(err)
				return
			}
			go c.shutdown(bye.code, bye.reason)
			return
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
//
// The guard is a timer rather than a deadline on the write's context: a
// cancelled context makes the library tear the connection down, and a viewer
// that is merely slow deserves to be told 1013 first. Whether it hears it
// depends on whether it starts reading again - a socket whose buffer is full
// has no room for a close frame either, and that is a fact about TCP rather
// than a choice this code makes.
func (c *termConn) writeFrame(typ websocket.MessageType, payload []byte) error {
	guard := time.AfterFunc(c.slow, c.tooSlow)
	defer guard.Stop()
	err := c.conn.Write(c.wctx, typ, payload)
	if c.overdue.Load() {
		return errors.New("this viewer could not keep up")
	}
	return err
}

// tooSlow gives up on a viewer whose frame has been stuck for too long.
//
// It does not close the socket there and then, and that is the whole point: a
// stuck data frame holds the connection's write lock, so a close frame written
// now could not go out and the client would see a bare disconnection. Marking
// the connection overdue makes the writer close it as soon as the frame is
// away - which is the moment the client starts reading again, and the only
// moment at which it can be told 1013 at all. A peer that never reads again is
// given the same interval once more and then dropped without a word, because
// a socket whose buffer is full has no room for a close frame either: that is
// a fact about TCP, and the client's own ping watchdog is what notices it.
func (c *termConn) tooSlow() {
	if !c.overdue.CompareAndSwap(false, true) {
		return
	}
	log.Printf("session %s: viewer %s has not taken a frame in %s", c.tv.sessionID, c.tv.viewerID, c.slow)
	time.AfterFunc(c.slow, func() {
		if c.ctx.Err() != nil {
			return
		}
		log.Printf("session %s: viewer %s never took it; the socket is dropped", c.tv.sessionID, c.tv.viewerID)
		_ = c.conn.CloseNow()
		c.wcancel()
		c.cancel()
	})
}

// stopWriter asks the writer to return between frames, without touching the
// connection.
func (c *termConn) stopWriter() { c.quitOnce.Do(func() { close(c.quit) }) }

// writerStopped says that nothing will write to this socket again.
//
// The writer closes it on its way out, and a handshake that ends before the
// writer was ever started closes it too. That second caller is the whole
// reason it is a helper: a takeover waits here for the socket it is replacing,
// and a socket whose hello could not be written would otherwise leave every
// later handshake for that tab waiting for a goroutine that does not exist -
// which is a viewer that can never be reached again.
func (c *termConn) writerStopped() { c.stopOnce.Do(func() { close(c.stopped) }) }

// finish ends this socket with a definite status, and is the only way a status
// is ever definite.
//
// The writer is asked to stop and waited for before the close frame is
// written, because cancelling the context a Write is blocked on makes the
// library abort the connection: the peer would then see 1006 instead of the
// 1002 or the 1012 this call exists to give it. Only a writer that cannot be
// woken inside the close grace - one whose frame is genuinely stuck in a
// socket nobody is reading - is cancelled, and that peer was being given up on
// anyway.
func (c *termConn) finish(code websocket.StatusCode, reason string) {
	c.stopWriter()
	select {
	case <-c.stopped:
	case <-time.After(closeHandshakeGrace):
		c.wcancel()
		<-c.stopped
	}
	c.shutdown(code, reason)
}

// shutdown ends this socket with a definite status. The first caller decides
// the code, so that a takeover is seen as 1012 and not as whatever the losing
// goroutine noticed a moment later.
//
// The close itself is done on a goroutine of its own and the contexts are
// cancelled only once it is finished, because a close handshake waits for a
// peer that is very often no longer there, and nothing - least of all a
// Manager goroutine broadcasting to five other viewers - may wait for that.
func (c *termConn) shutdown(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		go func() {
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = c.conn.Close(code, truncateReason(reason))
			}()
			select {
			case <-done:
			case <-time.After(closeHandshakeGrace):
				// The close frame is written first, so the peer has already
				// been told why. What is left is a handshake reply from a
				// phone that is not there; the read side is let go so that the
				// connection can finish closing.
				c.cancel()
				<-done
			}
			c.wcancel()
			c.cancel()
		}()
	})
}

// fail closes a socket the writer gave up on. 1013 is the honest code: come
// back later, your terminal is still here for ninety seconds.
func (c *termConn) fail(err error) {
	if c.taken.Load() || c.wctx.Err() != nil {
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
		case <-c.wctx.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(c.wctx, c.timeout)
		err := c.conn.Ping(ctx)
		cancel()
		if err == nil {
			misses = 0
			continue
		}
		if c.wctx.Err() != nil {
			return
		}
		misses++
		if misses >= 2 {
			if !c.taken.Load() {
				// Going away rather than a policy violation: the peer did
				// nothing wrong, it stopped being there.
				c.shutdown(websocket.StatusGoingAway, "this viewer stopped answering the ping")
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

// protocolError is a client that broke the framing rather than a client that
// went away, and it is answered differently: 1002, and no ninety second grace
// for a socket that could fill the viewer table with eight bad frames.
type protocolError struct{ why string }

func (e *protocolError) Error() string { return e.why }

// onInput writes a keystroke frame to the pane, exactly once.
//
// The server is the authority on what it has seen. A fresh viewer takes the
// first frame at whatever number it carries, a repeat is discarded, and a gap
// is refused with the last number that was accepted rather than written out of
// order into somebody's shell.
//
// The check, the write and the advance happen under one lock, and only for the
// socket that currently owns this viewer. During a takeover two read loops
// exist for a moment, and without both of those rules a frame that passed the
// check on the socket being replaced could be written a second time by the one
// replacing it - which is the one half of the promise that is absolute.
func (c *termConn) onInput(payload []byte) error {
	typed, err := c.writeInput(payload)
	if typed {
		// Typing at a session is the whole of having seen it. This is outside
		// writeInput because clearing the mark ends in a broadcast, and a
		// broadcast may not run under the viewer's own lock.
		c.srv.manager.NoteInput(c.tv.sessionID)
	}
	return err
}

// The two focus reports, which a terminal with mode 1004 on sends by itself
// when the tab gains or loses focus. tmux asks for them and reads them, so
// they are written to the pane like anything else.
const (
	focusIn  = "\x1b[I"
	focusOut = "\x1b[O"
)

// onlyFocusReports is a frame with nothing in it but those reports.
//
// The browser sends one on its own - a tab waking up, a socket reconnecting
// after a server restart - and a program answering a question nobody asked is
// not a person looking at the session. Counting it as input silently cleared
// the unread mark of a session that had finished while the tab was in the
// background. Anything typed alongside a report still counts, and every
// keystroke counts.
func onlyFocusReports(keys []byte) bool {
	if len(keys) == 0 {
		return false
	}
	for len(keys) > 0 {
		if len(keys) < 3 {
			return false
		}
		if head := string(keys[:3]); head != focusIn && head != focusOut {
			return false
		}
		keys = keys[3:]
	}
	return true
}

// writeInput is onInput's body, under the viewer's lock. The boolean is
// whether somebody typed, which is not the same as whether anything reached
// the pane.
func (c *termConn) writeInput(payload []byte) (bool, error) {
	if len(payload) < 9 || payload[0] != frameInput {
		return false, &protocolError{why: "that is not an input frame"}
	}
	seq := binary.BigEndian.Uint64(payload[1:9])
	body := payload[9:]

	tv := c.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if tv.conn != c {
		return false, nil // This socket has been taken over; its input is stale.
	}
	if !tv.sawInput {
		if seq == 0 {
			// Sequence numbers start at one. Answering the last accepted
			// number - none - tells the client where to start rather than
			// leaving it wondering why nothing is typed.
			c.send(map[string]any{"t": "input_ack", "seq": uint64(0)})
			return false, nil
		}
		tv.sawInput = true
		tv.lastInput = seq - 1
	}
	last := tv.lastInput
	switch {
	case seq <= last:
		return false, nil // Already written; this is a resend.
	case seq > last+1:
		c.send(map[string]any{"t": "input_ack", "seq": last})
		return false, nil
	}
	if tv.viewer == nil {
		// The socket is not at fault and neither is the frame: this tab's
		// terminal is being replaced. Ending the connection is what gets the
		// keystroke written, because the next handshake attaches one - and it
		// is not acknowledged, so the client still holds it.
		c.send(map[string]any{"t": "error", "message": "this terminal is being re-attached", "fatal": false})
		return false, errors.New("input arrived for a viewer with no terminal")
	}
	// A terminal report is not a keystroke. The browser answers questions
	// nothing asked it any more - a reply held across an outage, a tab woken
	// from sleep, a page from an older Socrates - and tmux, no longer waiting
	// for one, would type it into the pane in front of the next command.
	keys, pasting := termux.StripReplies(body, tv.pasting)
	tv.pasting = pasting
	if len(keys) > 0 {
		if _, err := tv.viewer.Write(keys); err != nil {
			// Nothing was written, so nothing is acknowledged: the client is
			// told the last number that did reach the pane, which is the same
			// "start again from here" a gap is answered with, and it renumbers
			// and resends what it is holding.
			c.send(map[string]any{"t": "input_ack", "seq": last})
			if errors.Is(err, termux.ErrClosed) {
				// The tmux client is gone. Only a new handshake can attach
				// another one, so this socket ends - and the keystroke is
				// still held by a client that never heard an ack for it.
				c.send(map[string]any{"t": "error", "message": "this terminal is being re-attached", "fatal": false})
				return false, err
			}
			// Anything else is one frame that failed, and one frame that
			// failed is not a connection that failed: the socket stays up for
			// every other frame, and only a pane that refuses several in a row
			// is given up on.
			c.inputFails++
			log.Printf("session %s: viewer %s could not be written to: %v", tv.sessionID, tv.viewerID, err)
			if c.inputFails >= maxInputFailures {
				c.send(map[string]any{"t": "error", "message": "this terminal is being re-attached", "fatal": false})
				return false, err
			}
			c.send(map[string]any{"t": "error", "message": "That keystroke did not reach the session.", "fatal": false})
			return false, nil
		}
		c.inputFails = 0
	}
	tv.lastInput = seq
	c.noteAck(seq)
	return len(keys) > 0 && !onlyFocusReports(keys), nil
}

// onControl handles the JSON half of the client's side. The result is whether
// the client said goodbye.
func (c *termConn) onControl(payload []byte) bool {
	var frame struct {
		T    string `json:"t"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
		// ID is a number on a ping and a session id on a read, so it is kept
		// as it arrived and read by the case that wants it. Decoding it as one
		// type would make the other frame unparseable rather than ignored.
		ID json.RawMessage `json:"id"`
		// Seq is the last output byte the client says it has rendered. It is
		// diagnostics only, and it is a JSON number, so it is exact up to 2^53
		// - a bound this counter would need years of a busy terminal to reach.
		Seq uint64 `json:"seq"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		c.send(map[string]any{"t": "error", "message": "that control frame is not JSON", "fatal": false})
		return false
	}
	if c.taken.Load() {
		// This socket has been replaced; acting on its frames would be acting
		// twice on a tab that is now somewhere else.
		return false
	}
	switch frame.T {
	case "ping":
		id := any(0)
		if len(frame.ID) > 0 {
			id = frame.ID
		}
		c.send(map[string]any{"t": "pong", "id": id})
	case "lag":
		c.tv.mu.Lock()
		c.tv.lag = frame.Seq
		c.tv.mu.Unlock()
	case "resize":
		c.onResize(frame.Cols, frame.Rows)
	case "read":
		// The id is explicit because the sidebar clears the unread mark of
		// rows that are not the session this socket is attached to.
		var id string
		if err := json.Unmarshal(frame.ID, &id); err == nil && id != "" {
			c.srv.manager.MarkRead(id)
		}
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

// broadcastAll sends a frame to every open socket, whatever session it is
// attached to.
//
// It exists for exactly one frame: the activity of a session nobody is
// watching. The sidebar shows every session, and a browser has one socket -
// the one for the terminal it is looking at - so a busy mark on a row nobody
// has open can only arrive on somebody else's socket. Without this the
// sidebar would need a poll, and the product's own constraint is a phone on a
// bad connection.
func (s *Server) broadcastAll(frame func(*termViewer) any) {
	for _, tv := range s.hub.all() {
		tv.mu.Lock()
		c := tv.conn
		tv.mu.Unlock()
		if c != nil {
			c.send(frame(tv))
		}
	}
}

// onSessionActivity turns a committed busy/idle/waiting change into the frame
// every open socket hears. It is the Manager's OnActivity, wired here for the
// same reason OnExit is: the pane is the substrate's business and the frame is
// the transport's.
func (s *Server) onSessionActivity(sessionID string, a termux.Activity) {
	frame := map[string]any{
		"t":        "activity",
		"sessions": map[string]termux.Activity{sessionID: a},
	}
	s.broadcastAll(func(*termViewer) any { return frame })
	// The first turn that finishes is what gives a session its name.
	s.titles.observe(sessionID, a.State)
}

// onSessionTitle tells every open socket that a session has a new name.
//
// It goes to every socket rather than to the session's own viewers because
// the name is in the sidebar of every browser, and the session that has just
// named itself is usually not the one being looked at. It is the smallest
// frame that could carry it: the row and the header both take a string.
func (s *Server) onSessionTitle(sessionID, title string) {
	frame := map[string]any{"t": "title", "id": sessionID, "title": title}
	s.broadcastAll(func(*termViewer) any { return frame })
}

// emitStatus carries one phase of a spoken status to the viewers of its
// session.
//
// Pressing Status asks a model a question over a network, and until this frame
// existed the only evidence of that was a button that had gone quiet. It goes
// to the session's viewers rather than to every socket because it is about the
// screen somebody is looking at, and because two phones on one session should
// both see it.
func (s *Server) emitStatus(sessionID, phase, text string) {
	frame := map[string]any{"t": "status", "id": sessionID, "phase": phase, "text": text}
	s.broadcast(sessionID, func(*termViewer) any { return frame })
}
