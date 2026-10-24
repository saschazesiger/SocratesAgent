package server

// The event stream: the one live channel of a page that has no terminal
// attached. Home, a prepared session's panel and the empty stage have no
// session socket to hear anything on, and before this existed they were a
// poll every fifteen seconds - which is a Home page that says "two sessions
// are running" a quarter of a minute after one of them asked a question. Every
// page of index.html holds one of these while it is visible, so the sidebar,
// Home and the question dialog are live everywhere.
//
// It is a WebSocket rather than an event source for the reason the terminal
// is one: a browser allows six connections per host over plain HTTP/1.1, and
// every tab's event source would hold one of them for good.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The stream's constants, from HOME.md §1.2.
const (
	// eventsSubprotocol names the framing, so that a client built for a later
	// one is refused by the handshake rather than by a confusing frame.
	eventsSubprotocol = "socrates.events.v1"
	// eventsReadLimit is generous for the one frame a client sends, a ping.
	eventsReadLimit = 64 << 10
	// eventsBuffer is how many frames one subscriber may be behind, before
	// eventsPerSession for each session there is. A list, a handful of
	// activity changes and a digest are a dozen; sixty-four more than a burst
	// of every session at once that have not gone out is a socket that has
	// stopped, not a slow one.
	eventsBuffer = 64
	// eventsPerSession is the burst one session adds: the first detector tick
	// after a restart commits every running session in one loop, each an
	// activity frame and a digest frame going pending, with the unread mark
	// and the digest itself behind them - and the outcome of its last turn,
	// taken away when the session is no longer at the idle it was kept for
	// (outcome.go). A hundred sessions are five hundred frames inside a
	// millisecond, which no writer sends that fast, and a subscriber that
	// cannot hold them is dropped and downloads the whole state again for
	// nothing.
	eventsPerSession = 5
	// eventsRepliesBuffer is the pongs waiting for the writer. A client that
	// pings faster than it reads its answers loses answers, not the socket.
	eventsRepliesBuffer = 8

	// defaultEventsWatchEvery is how often the store's revision is looked at
	// while somebody is listening. It is a counter in memory, so the look is
	// free; the list is only read when the counter has moved.
	defaultEventsWatchEvery = 300 * time.Millisecond
	// defaultEventsListGap coalesces the lists: a create is a row, a title, a
	// size and a last-attached in quick succession, and one list a quarter of
	// a second later says all of it.
	defaultEventsListGap = 250 * time.Millisecond
	// defaultEventsTickEvery is the heartbeat a client tells a dead
	// connection from a quiet one by. It is also how often a stream's login
	// is checked again, which is what ends one whose login simply expired.
	defaultEventsTickEvery = 15 * time.Second

	// eventsHandshakeBurst is how many streams one login may open per
	// minute. A page closes its stream whenever it is hidden and opens one
	// when it is shown, so a person switching between three tabs and an app
	// opens dozens a minute; the ceiling is for a client that is broken, not
	// for one that is used. It is counted per login rather than per address:
	// the handshake is signed in, and behind one home router a phone, a
	// laptop and a car are one address.
	eventsHandshakeBurst = 240
)

// eventsSignedOut is the close code of a stream whose login is no longer
// valid - signed out, every login revoked by a new password, or expired. It is
// in the range the protocol leaves to applications, and it says "go to the
// sign-in page" rather than "reconnect", which every other close means.
const eventsSignedOut websocket.StatusCode = 4401

// eventHub is every open event stream, and the loop that tells them when the
// session list has changed.
type eventHub struct {
	srv *Server

	mu   sync.Mutex
	subs map[*eventSub]struct{}
	// stopWatch ends the list watcher. It is set while the watcher runs, which
	// is exactly while there is anybody to tell.
	stopWatch context.CancelFunc

	// poked skips the rest of the watcher's wait: a handler that has just
	// written a row says so, and the list goes out now rather than within
	// the next three hundred milliseconds.
	poked chan struct{}

	// listMu orders a hello against a sessions frame. A list read before a
	// hello and queued after it would take a browser back to a state the
	// hello had already moved past; under this lock a list is either queued
	// before the subscriber exists or read after its hello was.
	listMu  sync.Mutex
	sentRev int64
	sentAt  time.Time

	// watchEvery, listGap and tickEvery are fields rather than constants so
	// that a test can run them in milliseconds.
	watchEvery time.Duration
	listGap    time.Duration
	tickEvery  time.Duration
}

// eventSub is one open stream as the hub sees it: the frames waiting for its
// writer, the login it was opened with, and the news that it has been dropped
// and why.
type eventSub struct {
	frames chan []byte
	gone   chan struct{}
	once   sync.Once
	token  string

	// code and reason are what the socket is closed with, set once, before
	// gone is closed.
	code   websocket.StatusCode
	reason string
}

func (sub *eventSub) drop() { sub.dropWith(websocket.StatusPolicyViolation, "too slow") }

func (sub *eventSub) dropWith(code websocket.StatusCode, reason string) {
	sub.once.Do(func() {
		sub.code, sub.reason = code, reason
		close(sub.gone)
	})
}

func newEventHub(s *Server) *eventHub {
	return &eventHub{
		srv:        s,
		subs:       map[*eventSub]struct{}{},
		poked:      make(chan struct{}, 1),
		watchEvery: defaultEventsWatchEvery,
		listGap:    defaultEventsListGap,
		tickEvery:  defaultEventsTickEvery,
	}
}

// publish hands one frame to every open stream and returns at once.
//
// It is called from the activity detector's tick, so it never waits for a
// socket: a subscriber whose queue is full is dropped there and then, and its
// own goroutine closes the socket. A phone that cannot keep up reconnects and
// is resynchronised by its hello, which costs it nothing it could have read.
func (h *eventHub) publish(frame any) {
	if h == nil {
		return
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		log.Printf("events: could not encode a frame: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.frames <- payload:
		default:
			delete(h.subs, sub)
			sub.drop()
		}
	}
	if len(h.subs) == 0 {
		h.stopWatchingLocked()
	}
}

// poke says that a row has just been written, so that the list goes out
// without waiting for the watcher's next look.
func (h *eventHub) poke() {
	if h == nil {
		return
	}
	select {
	case h.poked <- struct{}{}:
	default:
	}
}

// count is how many streams are open. Home's summaries of busy sessions are
// only kept fresh while it is above zero: nobody is reading them otherwise.
func (h *eventHub) count() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// subscribe registers a stream and answers it with the state it starts from.
// The subscriber exists before the state is read, so nothing that changes in
// between is lost: it is in the hello, in the queue behind it, or both.
func (h *eventHub) subscribe(token string) (*eventSub, map[string]any, error) {
	h.listMu.Lock()
	defer h.listMu.Unlock()
	sub := &eventSub{frames: make(chan []byte, h.bufferSize()), gone: make(chan struct{}), token: token}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	if h.stopWatch == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.stopWatch = cancel
		// Everybody listening is about to be told this revision by a hello,
		// so the watcher starts from it rather than repeating it.
		h.sentRev = h.srv.store.Rev()
		go h.watch(ctx)
	}
	h.mu.Unlock()
	state, err := h.srv.homeState()
	if err != nil {
		h.unsubscribe(sub)
		return nil, nil, err
	}
	return sub, state, nil
}

// bufferSize is how many frames a new subscriber may be behind: the steady
// state and a burst of every session there is (eventsPerSession).
func (h *eventHub) bufferSize() int {
	rows, err := h.srv.store.ListSessions()
	if err != nil {
		return eventsBuffer
	}
	return eventsBuffer + eventsPerSession*len(rows)
}

// unsubscribe forgets a stream, and stops the watcher with the last one.
func (h *eventHub) unsubscribe(sub *eventSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub)
	if len(h.subs) == 0 {
		h.stopWatchingLocked()
	}
}

// revoke closes every stream whose login is no longer valid. Signing out and
// changing the password call it: the stream carries every session's title,
// summary and question, and a phone that was lost while it showed Home must
// stop receiving them the moment its owner signs everybody out - not when its
// socket happens to drop.
func (h *eventHub) revoke() {
	if h == nil {
		return
	}
	h.mu.Lock()
	subs := make([]*eventSub, 0, len(h.subs))
	for sub := range h.subs {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	// The store is read outside the lock: publish takes it from the
	// detector's tick, and must never wait for a query.
	for _, sub := range subs {
		if !h.srv.store.ValidLogin(sub.token) {
			h.unsubscribe(sub)
			sub.dropWith(eventsSignedOut, "signed out")
		}
	}
}

func (h *eventHub) stopWatchingLocked() {
	if h.stopWatch != nil {
		h.stopWatch()
		h.stopWatch = nil
	}
}

// watch sends the whole list again whenever the store's revision has moved.
//
// The revision is the one thing every write path moves - a handler, the
// detector filing a session, the scheduler starting one, the sweep deleting
// one, a restore from the bucket - so watching it covers all of them without
// any of them having to know there is a Home page. The list is the whole list
// every time: it is tens of rows, and a browser that applies diffs is a
// browser that drifts.
func (h *eventHub) watch(ctx context.Context) {
	ticker := time.NewTicker(h.watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.poked:
		}
		if h.srv.store.Rev() == h.lastRev() {
			continue
		}
		if wait := h.listGap - time.Since(h.lastSent()); wait > 0 {
			if sleepFor(ctx, wait) != nil {
				return
			}
		}
		h.sendList()
	}
}

func (h *eventHub) lastRev() int64 {
	h.listMu.Lock()
	defer h.listMu.Unlock()
	return h.sentRev
}

func (h *eventHub) lastSent() time.Time {
	h.listMu.Lock()
	defer h.listMu.Unlock()
	return h.sentAt
}

// sendList publishes the list as it is now. The revision is read before the
// rows, so a write that lands between the two is sent again on the next look
// rather than never.
func (h *eventHub) sendList() {
	h.listMu.Lock()
	defer h.listMu.Unlock()
	rev := h.srv.store.Rev()
	if rev == h.sentRev {
		return
	}
	rows, err := h.srv.store.ListSessions()
	if err != nil {
		log.Printf("events: could not read the sessions: %v", err)
		return
	}
	h.publish(map[string]any{"t": "sessions", "sessions": h.srv.views(rows), "rev": rev})
	h.sentRev, h.sentAt = rev, time.Now()
}

// homeState is everything a page needs to draw Home and the sidebar from
// nothing: the list, every chat's reading and every verdict there is. It is
// the hello of the stream and the answer of GET /api/home, so a page whose
// socket is down polls the same thing it would have been told.
func (s *Server) homeState() (map[string]any, error) {
	// The revision before the rows, for the reason sendList gives.
	rev := s.store.Rev()
	rows, err := s.store.ListSessions()
	if err != nil {
		return nil, err
	}
	// A chat's reading is made from its journal (chatbrief.go), and is where
	// a page looks for a session's line.
	return map[string]any{
		"sessions": s.views(rows),
		"rev":      rev,
		"digests":  s.chats.readings(),
		"outcomes": s.digests.outcomesAll(),
		"now":      time.Now().UnixMilli(),
	}, nil
}

/* ------------------------------------------------------------- the routes */

// handleHome is the stream's hello as a plain request: the fallback a page
// polls while its socket is down.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	state, err := s.homeState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONCompressed(w, r, http.StatusOK, state)
}

// handleEvents is the event stream.
//
// The cookie is checked by the auth wrapper and the origin by Accept, for the
// reason the terminal socket gives: cookies travel on a cross-origin
// handshake, and this stream carries every session's title and question.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if err := s.throttle(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	// The auth wrapper has checked the cookie, so it is there and valid.
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	token := cookie.Value
	// A ceiling like the terminal's handshakes, counted apart from them - a
	// page reconnecting its stream must not use up the handshakes that open
	// terminals, and the other way round - and per login, with room for a
	// person who switches tabs (eventsHandshakeBurst).
	if !s.allowRate("events "+loginKey(token), eventsHandshakeBurst) {
		writeError(w, http.StatusTooManyRequests, "too many event connections; slow down")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{eventsSubprotocol},
		CompressionMode: websocket.CompressionContextTakeover,
		OriginPatterns:  []string{r.Host},
	})
	if err != nil {
		// Accept has already answered - 403 for a foreign origin.
		return
	}
	conn.SetReadLimit(eventsReadLimit)
	if err := s.serveEvents(conn, token); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("events: %v", err)
	}
}

// loginKey is a login's name in the throttle's table: a hash, so that the
// table never holds a token that signs somebody in.
func loginKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

// serveEvents runs one stream from hello to close: one goroutine reads (the
// pings, and the end of the connection), one pings, and this one writes.
//
// The login it was opened with is checked again on every tick, and revoke
// ends it at once when somebody signs out: the stream is read only, but what
// it reads is every session's title, summary and question.
func (s *Server) serveEvents(conn *websocket.Conn, token string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, hello, err := s.events.subscribe(token)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, truncateReason(err.Error()))
		return err
	}
	defer s.events.unsubscribe(sub)

	write := func(payload []byte) error {
		wctx, stop := context.WithTimeout(ctx, s.writeTimeout)
		defer stop()
		return conn.Write(wctx, websocket.MessageText, payload)
	}
	hello["t"] = "hello"
	payload, err := json.Marshal(hello)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "the state could not be encoded")
		return err
	}
	if err := write(payload); err != nil {
		_ = conn.CloseNow()
		return nil
	}
	replies := make(chan []byte, eventsRepliesBuffer)
	go s.readEvents(ctx, cancel, conn, replies)
	go s.pingEvents(ctx, cancel, conn)

	tick := time.NewTicker(s.events.tickEvery)
	defer tick.Stop()
	for {
		var next []byte
		select {
		case <-ctx.Done():
			_ = conn.CloseNow()
			return nil
		case <-sub.gone:
			// Dropped by publish for not keeping up, or by revoke for a login
			// that is gone. The reason is said, so a client can tell either
			// from a server going away.
			_ = conn.Close(sub.code, sub.reason)
			return nil
		case next = <-sub.frames:
		case next = <-replies:
			// A pong says that every frame published before its ping has
			// arrived, and the page judges a question link against its state
			// by it. A writer that was busy with a long write comes back to
			// both queues ready, and either may be picked first: the frames
			// that were waiting go out ahead of the pong.
			for queued := len(sub.frames); queued > 0; queued-- {
				if err := write(<-sub.frames); err != nil {
					_ = conn.CloseNow()
					return nil
				}
			}
		case <-tick.C:
			if !s.store.ValidLogin(token) {
				_ = conn.Close(eventsSignedOut, "signed out")
				return nil
			}
			next, _ = json.Marshal(map[string]any{"t": "tick", "now": time.Now().UnixMilli()})
		}
		if err := write(next); err != nil {
			_ = conn.CloseNow()
			return nil
		}
	}
}

// readEvents answers the client's pings and ends the stream when the client
// goes. Anything else a client sends is ignored: this stream is the server's
// to talk on.
func (s *Server) readEvents(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, replies chan<- []byte) {
	defer cancel()
	for {
		typ, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var frame struct {
			T  string          `json:"t"`
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(payload, &frame) != nil || frame.T != "ping" {
			continue
		}
		id := frame.ID
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		pong, err := json.Marshal(map[string]any{"t": "pong", "id": id})
		if err != nil {
			continue
		}
		select {
		case replies <- pong:
		default:
		}
	}
}

// pingEvents is the server half of the watchdog, as the terminal socket runs
// it: two missed pings in a row and the stream is given up on.
func (s *Server) pingEvents(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	ticker := time.NewTicker(s.pingEvery)
	defer ticker.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pctx, stop := context.WithTimeout(ctx, s.pingTimeout)
		err := conn.Ping(pctx)
		stop()
		if err == nil {
			misses = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		misses++
		if misses >= 2 {
			cancel()
			return
		}
	}
}
