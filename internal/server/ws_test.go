//go:build !windows

package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The transport is tested the way a browser uses it: a real WebSocket client
// against httptest, a real tmux session on a socket under t.TempDir(), and the
// e2e suite's fake TUI where a real CLI would be. Nothing here starts a paid
// session, and nothing touches the machine's own tmux server.

// wsClient is a browser tab: a socket, a reader goroutine, and the two things
// a tab has to remember - how far it has rendered, and which of its keystrokes
// the server has acknowledged.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu       sync.Mutex
	output   []byte
	rendered uint64 // the last output byte sequence number received
	frames   []uint64
	// holes counts frames that did not begin where the previous one ended.
	// The transport's promise is that there are none.
	holes   int
	ctrl    []map[string]any
	readErr error
	closed  chan struct{}

	woke chan struct{}
}

// dialWS opens a terminal socket carrying the signed-in cookie, and reads it
// in the background the way a browser does.
func (e *sessionEnv) dialWS(sessionID string, query string) *wsClient {
	e.t.Helper()
	c := e.dialWSRaw(sessionID, query, nil)
	c.readInBackground()
	return c
}

// dialWSRaw opens the socket and leaves it unread, which is what the slow
// reader and the watchdog need.
func (e *sessionEnv) dialWSRaw(sessionID, query string, opts *websocket.DialOptions) *wsClient {
	e.t.Helper()
	conn, res, err := websocket.Dial(context.Background(), e.wsURL(sessionID, query), e.dialOptions(opts))
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		e.t.Fatalf("dial %s: %v (status %d)", query, err, status)
	}
	conn.SetReadLimit(8 << 20)
	c := &wsClient{t: e.t, conn: conn, closed: make(chan struct{}), woke: make(chan struct{}, 1)}
	e.t.Cleanup(func() { _ = conn.CloseNow() })
	return c
}

func (e *sessionEnv) wsURL(sessionID, query string) string {
	base := "ws" + strings.TrimPrefix(e.server.URL, "http")
	u := base + "/api/sessions/" + sessionID + "/ws"
	if query != "" {
		u += "?" + query
	}
	return u
}

// dialOptions carries the session cookie and the subprotocol, which is what
// the browser's own client will send.
func (e *sessionEnv) dialOptions(opts *websocket.DialOptions) *websocket.DialOptions {
	e.t.Helper()
	if opts == nil {
		opts = &websocket.DialOptions{}
	}
	if opts.HTTPHeader == nil {
		opts.HTTPHeader = http.Header{}
	}
	u, err := url.Parse(e.server.URL)
	if err != nil {
		e.t.Fatal(err)
	}
	var pairs []string
	for _, cookie := range e.client.Jar.Cookies(u) {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	opts.HTTPHeader.Set("Cookie", strings.Join(pairs, "; "))
	opts.Subprotocols = []string{wsSubprotocol}
	return opts
}

func (c *wsClient) readInBackground() {
	go func() {
		defer close(c.closed)
		for {
			typ, payload, err := c.conn.Read(context.Background())
			if err != nil {
				c.mu.Lock()
				c.readErr = err
				c.mu.Unlock()
				c.wake()
				return
			}
			c.mu.Lock()
			if typ == websocket.MessageBinary {
				if len(payload) >= 9 && payload[0] == frameOutput {
					seq := binary.BigEndian.Uint64(payload[1:9])
					if len(c.frames) > 0 && seq != c.rendered+1 {
						c.holes++
					}
					c.frames = append(c.frames, seq)
					c.output = append(c.output, payload[9:]...)
					c.rendered = seq + uint64(len(payload)-9) - 1
				}
			} else {
				var frame map[string]any
				if err := json.Unmarshal(payload, &frame); err == nil {
					c.ctrl = append(c.ctrl, frame)
				}
			}
			c.mu.Unlock()
			c.wake()
		}
	}()
}

func (c *wsClient) wake() {
	select {
	case c.woke <- struct{}{}:
	default:
	}
}

// await polls the client's state until the condition holds, and fails the test
// with what it saw when it does not.
func (c *wsClient) await(what string, within time.Duration, cond func() bool) {
	c.t.Helper()
	deadline := time.After(within)
	for {
		c.mu.Lock()
		ok := cond()
		c.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-c.woke:
		case <-deadline:
			c.mu.Lock()
			defer c.mu.Unlock()
			c.t.Fatalf("waiting for %s timed out; screen so far:\n%q\ncontrol frames: %v\nerror: %v",
				what, c.output, c.ctrl, c.readErr)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// hello waits for the first control frame, which every socket sends before
// anything else.
func (c *wsClient) hello() map[string]any {
	c.t.Helper()
	c.await("hello", 20*time.Second, func() bool {
		return len(c.ctrl) > 0
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctrl[0]["t"] != "hello" {
		c.t.Fatalf("the first frame was %v, not hello", c.ctrl[0])
	}
	return c.ctrl[0]
}

// waitFor waits until the terminal stream contains a string.
func (c *wsClient) waitFor(want string) {
	c.t.Helper()
	c.await(fmt.Sprintf("%q on screen", want), 20*time.Second, func() bool {
		return strings.Contains(string(c.output), want)
	})
}

// waitCtrl waits for a control frame of a kind and returns it.
func (c *wsClient) waitCtrl(kind string) map[string]any {
	c.t.Helper()
	var found map[string]any
	c.await("a "+kind+" frame", 20*time.Second, func() bool {
		for _, frame := range c.ctrl {
			if frame["t"] == kind {
				found = frame
				return true
			}
		}
		return false
	})
	return found
}

// helloOrClose waits for whichever comes first: the socket's first control
// frame, or the socket ending. It is how a test asks the question a client
// asks - was I served, or was I told to come back - without failing on either.
func (c *wsClient) helloOrClose(within time.Duration) (map[string]any, websocket.StatusCode, bool) {
	deadline := time.After(within)
	for {
		c.mu.Lock()
		if len(c.ctrl) > 0 {
			frame := c.ctrl[0]
			c.mu.Unlock()
			return frame, 0, false
		}
		err := c.readErr
		c.mu.Unlock()
		if err != nil {
			return nil, websocket.CloseStatus(err), false
		}
		select {
		case <-c.woke:
		case <-deadline:
			return nil, 0, true
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *wsClient) screen() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.output)
}

// settle waits until the stream has been quiet for a moment, so that a test
// can record how far it has rendered without the next frame overtaking it.
func (c *wsClient) settle() uint64 {
	c.t.Helper()
	last := uint64(0)
	for i := 0; i < 100; i++ {
		at := c.renderedTo()
		if at == last && at > 0 {
			return at
		}
		last = at
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatal("the terminal never stopped producing output")
	return 0
}

func (c *wsClient) renderedTo() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rendered
}

// type sends one input frame at the sequence number given, which is how a test
// says "this is a resend" or "this one has a gap in front of it".
func (c *wsClient) send(seq uint64, keys string) {
	c.t.Helper()
	frame := make([]byte, 9+len(keys))
	frame[0] = frameInput
	binary.BigEndian.PutUint64(frame[1:9], seq)
	copy(frame[9:], keys)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		c.t.Fatalf("sending input %d: %v", seq, err)
	}
}

func (c *wsClient) sendJSON(v any) {
	c.t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		c.t.Fatalf("sending %v: %v", v, err)
	}
}

// drop is the mobile failure this whole design is about: the socket goes
// without a close handshake and the server does not hear about it.
func (c *wsClient) drop() { _ = c.conn.CloseNow() }

// closeStatus waits for the reader to end and reports why.
func (c *wsClient) closeStatus(within time.Duration) websocket.StatusCode {
	c.t.Helper()
	select {
	case <-c.closed:
	case <-time.After(within):
		c.t.Fatalf("the socket was still open after %s", within)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return websocket.CloseStatus(c.readErr)
}

// shellSession creates one shell session and returns its id.
func (e *sessionEnv) shellSession(cols, rows int) string {
	e.t.Helper()
	res, session := e.create(fmt.Sprintf(
		`{"harness":"shell","workdir_mode":"custom","workdir":%q,"cols":%d,"rows":%d}`,
		e.work, cols, rows))
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	return sessionID(e.t, session)
}

func floatOf(t *testing.T, frame map[string]any, key string) float64 {
	t.Helper()
	v, ok := frame[key].(float64)
	if !ok {
		t.Fatalf("%s is %#v in %#v", key, frame[key], frame)
	}
	return v
}

// TestTerminalHelloAndReplayAfterDrop is the ordinary phone case: a socket
// dies mid-session, output happens while it is gone, and the reconnect fills
// the gap without repainting anything.
func TestTerminalHelloAndReplayAfterDrop(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	hello := first.hello()
	if hello["viewer_fresh"] != true {
		t.Fatalf("the first hello says the viewer is known: %#v", hello)
	}
	if got := floatOf(t, hello, "replay_from"); got != 0 {
		t.Fatalf("replay_from = %v on a first connect", got)
	}
	if got := floatOf(t, hello, "input_ack"); got != 0 {
		t.Fatalf("input_ack = %v on a first connect", got)
	}

	first.send(1, "echo mark-one\r")
	first.waitFor("mark-one")
	rendered := first.settle()
	first.drop()

	// Output the tab could not have seen, produced while it was away.
	if _, err := e.tmux("send-keys", "-t", "soc_"+id, "echo mark-two", "Enter"); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	e.waitForPane(id, "mark-two")

	second := e.dialWS(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	hello = second.hello()
	if hello["viewer_fresh"] != false {
		t.Fatalf("the reconnect was not recognised: %#v", hello)
	}
	if got := floatOf(t, hello, "replay_from"); uint64(got) != rendered {
		t.Fatalf("replay_from = %v, want %d", got, rendered)
	}
	second.waitFor("mark-two")

	screen := second.screen()
	// Two occurrences: the line tmux echoed and the line the shell printed.
	// Anything the tab had already rendered would be a third.
	if strings.Count(screen, "mark-two") != 2 {
		t.Fatalf("the gap was not delivered exactly once:\n%q", screen)
	}
	if strings.Contains(screen, "mark-one") {
		t.Fatalf("the reconnect replayed what the tab had already seen:\n%q", screen)
	}
	second.mu.Lock()
	holes := second.holes
	second.mu.Unlock()
	if holes != 0 {
		t.Fatalf("%d frames did not follow on from the one before", holes)
	}
	if strings.Contains(screen, "\x1b[2J") || strings.Contains(screen, "\x1b[?1049h") {
		t.Fatalf("the reconnect repainted the screen:\n%q", screen)
	}
	second.mu.Lock()
	firstSeq := second.frames[0]
	second.mu.Unlock()
	if firstSeq != rendered+1 {
		t.Fatalf("the stream resumed at %d, want %d", firstSeq, rendered+1)
	}
}

// TestTerminalReconnectAfterGraceIsFresh is the other half: a tab that stayed
// away too long is a stranger, its hello says so, and it can still type -
// which is the case a persisted input counter would dead-lock.
func TestTerminalReconnectAfterGraceIsFresh(t *testing.T) {
	e := newSessionEnv(t)
	e.srv.hub.grace = 150 * time.Millisecond

	id := e.shellSession(100, 30)
	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()
	first.send(1, "echo before\r")
	first.waitFor("before")
	first.drop()

	// The grace expires and the tmux client goes with it.
	deadline := time.Now().Add(10 * time.Second)
	for e.srv.hub.count(id) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := e.srv.hub.count(id); n != 0 {
		t.Fatalf("%d viewers survived the grace", n)
	}

	second := e.dialWS(id, "viewer=tab-a&cols=100&rows=30&since=4000")
	hello := second.hello()
	if hello["viewer_fresh"] != true {
		t.Fatalf("a viewer whose grace expired was not fresh: %#v", hello)
	}
	if got := floatOf(t, hello, "input_ack"); got != 0 {
		t.Fatalf("input_ack = %v after the grace", got)
	}
	if got := floatOf(t, hello, "replay_from"); got != 0 {
		t.Fatalf("replay_from = %v after the grace", got)
	}
	// A client that remembered a counter of its own comes back at 501, and the
	// server takes it: a fresh viewer accepts the first frame at any number.
	second.send(501, "echo after-the-grace\r")
	second.waitFor("after-the-grace")
	e.waitForPane(id, "after-the-grace")
}

// TestTerminalInputIsExactlyOnce pins the promise: a resend writes nothing
// twice, and a gap is refused rather than typed out of order.
func TestTerminalInputIsExactlyOnce(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	c := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	c.hello()

	c.send(1, "echo mark-alpha\r")
	c.waitFor("mark-alpha")
	ack := c.waitCtrl("input_ack")
	if got := floatOf(t, ack, "seq"); got != 1 {
		t.Fatalf("input_ack = %v, want 1", got)
	}

	// The same frame again, exactly as a reconnecting client would resend it.
	c.send(1, "echo mark-alpha\r")
	// And one with a hole in front of it.
	c.send(9, "echo mark-never\r")
	c.await("the rejection of the gap", 10*time.Second, func() bool {
		acks := 0
		for _, frame := range c.ctrl {
			if frame["t"] == "input_ack" {
				acks++
			}
		}
		return acks >= 2
	})

	// Give the shell time to run anything it was wrongly given.
	time.Sleep(500 * time.Millisecond)
	screen, err := e.tmux("capture-pane", "-p", "-t", "soc_"+id)
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	// The command line the user typed, and the line the shell printed: two.
	if n := strings.Count(screen, "mark-alpha"); n != 2 {
		t.Fatalf("mark-alpha appears %d times, want 2:\n%s", n, screen)
	}
	if strings.Contains(screen, "mark-never") {
		t.Fatalf("a frame with a gap in front of it was written:\n%s", screen)
	}
	last := c.waitCtrl("input_ack")
	if got := floatOf(t, last, "seq"); got != 1 {
		t.Fatalf("the rejection acknowledged %v, want the last accepted, 1", got)
	}
}

// TestTerminalReloadWithFramesInFlight is the case the naive design dead-locks:
// the tab is killed with keystrokes unacknowledged, comes back with an empty
// memory, and has to be able to type.
func TestTerminalReloadWithFramesInFlight(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()

	first.send(1, "echo one-in-flight\r")
	first.send(2, "echo two-in-flight\r")
	first.waitFor("two-in-flight")
	rendered := first.settle()
	// iOS kills the tab. The frames were written; the acknowledgement may
	// never have arrived.
	first.drop()

	second := e.dialWS(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	hello := second.hello()
	if got := floatOf(t, hello, "input_ack"); got != 2 {
		t.Fatalf("input_ack = %v, want 2 - the client has nothing else to anchor to", got)
	}
	// The client renumbers from input_ack + 1, which is what the next
	// keystroke is.
	second.send(3, "echo after-reload\r")
	second.waitFor("after-reload")
	time.Sleep(300 * time.Millisecond)
	screen, err := e.tmux("capture-pane", "-p", "-t", "soc_"+id)
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	for _, marker := range []string{"one-in-flight", "two-in-flight", "after-reload"} {
		if n := strings.Count(screen, marker); n != 2 {
			t.Fatalf("%s appears %d times, want 2:\n%s", marker, n, screen)
		}
	}
}

// TestTerminalTakeover: a second handshake from the same tab replaces the
// first at once, rather than leaving a half-open socket looking connected
// until the watchdog condemns it.
func TestTerminalTakeover(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()

	second := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	second.hello()
	if got := first.closeStatus(time.Second); got != websocket.StatusServiceRestart {
		t.Fatalf("the first socket closed with %v, want 1012", got)
	}
	// The terminal itself carried on: it is the same tmux client.
	second.send(1, "echo after-takeover\r")
	second.waitFor("after-takeover")
	if n := e.srv.hub.count(id); n != 1 {
		t.Fatalf("a takeover left %d viewers", n)
	}
}

// TestTerminalTwoViewers: two tabs on one session each get the whole stream on
// a sequence of their own, because tmux renders per client.
func TestTerminalTwoViewers(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	a := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	a.hello()
	b := e.dialWS(id, "viewer=tab-b&cols=100&rows=30")
	b.hello()

	a.send(1, "echo from-a\r")
	a.waitFor("from-a")
	b.waitFor("from-a")
	b.send(1, "echo from-b\r")
	b.waitFor("from-b")
	a.waitFor("from-b")
	if n := e.srv.hub.count(id); n != 2 {
		t.Fatalf("the session has %d viewers, want 2", n)
	}
}

// TestTerminalSizeOwnership pins §A.7 as the browser sees it: the window
// follows the viewer that last connected or resized, everybody is told, and
// ownership moves when a socket is lost rather than when a grace expires.
func TestTerminalSizeOwnership(t *testing.T) {
	e := newSessionEnv(t)
	e.srv.hub.grace = time.Hour // Ownership must move without waiting for it.

	id := e.shellSession(100, 30)
	a := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	a.hello()
	e.waitForWindow(id, "100x30")

	b := e.dialWS(id, "viewer=tab-b&cols=80&rows=24")
	b.hello()
	e.waitForWindow(id, "80x24")
	frame := a.waitCtrl("size")
	if frame["by"] != "other" || floatOf(t, frame, "cols") != 80 {
		t.Fatalf("the other viewer's resize reached tab-a as %#v", frame)
	}

	// An explicit resize takes ownership back, the way rotating a phone does.
	a.sendJSON(map[string]any{"t": "resize", "cols": 110, "rows": 32})
	e.waitForWindow(id, "110x32")
	frame = b.waitCtrl("size")
	if frame["by"] != "other" {
		t.Fatalf("tab-b was told the resize was its own: %#v", frame)
	}

	// The owner drops. Ownership passes on the socket, not on the grace, so
	// the window is tab-b's size within moments rather than within an hour.
	a.drop()
	e.waitForWindow(id, "80x24")
}

// waitForWindow waits until tmux reports the window at a size.
func (e *sessionEnv) waitForWindow(id, want string) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		got, _ = e.tmux("display-message", "-p", "-t", "soc_"+id, "#{window_width}x#{window_height}")
		if got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("the window of %s is %s, want %s", id, got, want)
}

// TestTerminalPingPong covers both halves of the watchdog: the client's ping is
// answered, and a client that stops answering the server's is closed.
func TestTerminalPingPong(t *testing.T) {
	e := newSessionEnv(t)
	e.srv.pingEvery, e.srv.pingTimeout = 150*time.Millisecond, 100*time.Millisecond

	id := e.shellSession(100, 30)
	answering := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	answering.hello()
	answering.sendJSON(map[string]any{"t": "ping", "id": 9})
	pong := answering.waitCtrl("pong")
	if got := floatOf(t, pong, "id"); got != 9 {
		t.Fatalf("pong carried id %v, want 9", got)
	}

	// A client whose pongs are suppressed is condemned after two misses.
	silent := e.dialWSRaw(id, "viewer=tab-b&cols=100&rows=30", &websocket.DialOptions{
		OnPingReceived: func(context.Context, []byte) bool { return false },
	})
	silent.readInBackground()
	silent.hello()
	if got := silent.closeStatus(5 * time.Second); got != websocket.StatusGoingAway {
		t.Fatalf("the silent viewer closed with %v, want 1001", got)
	}
	// The one that answers is untouched.
	answering.send(1, "echo still-here\r")
	answering.waitFor("still-here")
}

// TestTerminalForeignOriginIsRefused: cookies travel on a WebSocket handshake,
// so without this check any page could open a terminal on this machine.
func TestTerminalForeignOriginIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	opts := e.dialOptions(&websocket.DialOptions{HTTPHeader: http.Header{}})
	opts.HTTPHeader.Set("Origin", "https://evil.example")
	conn, res, err := websocket.Dial(context.Background(), e.wsURL(id, "viewer=tab-a&cols=80&rows=24"), opts)
	if err == nil {
		conn.CloseNow()
		t.Fatal("a foreign origin was allowed to open a terminal")
	}
	if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("a foreign origin was refused with %v, want 403", res)
	}
}

// TestTerminalExitNotice: a program that ends reaches every viewer as an exit
// frame with its status, which is what the overlay is drawn from.
func TestTerminalExitNotice(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q,"cols":100,"rows":30}`, e.work))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	c := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	c.hello()
	c.waitFor("FAKE claude")

	c.send(1, "/exit 7\r")
	exit := c.waitCtrl("exit")
	if got := floatOf(t, exit, "status"); got != 7 {
		t.Fatalf("the exit frame carried status %v, want 7", got)
	}
	e.waitForState(id, 20*time.Second, store.StateExited)
}

// TestTerminalResumeNotice: opening a session a reboot took away resumes it on
// the way in, and the socket says so - which is what WP4 could not do, having
// no transport to say it on.
func TestTerminalResumeNotice(t *testing.T) {
	e := newSessionEnv(t)
	res, session := e.create(fmt.Sprintf(
		`{"harness":"claude","workdir_mode":"custom","workdir":%q,"cols":100,"rows":30}`, e.work))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", res.StatusCode, session)
	}
	id := sessionID(t, session)
	e.waitForPane(id, "FAKE claude")

	// The reboot, as far as Socrates can tell it apart from one.
	if _, err := e.tmux("kill-server"); err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	ctx := context.Background()
	e.srv.Sessions().Poll(ctx)
	e.srv.Sessions().Poll(ctx)
	e.waitForState(id, 10*time.Second, store.StateNeedsResume)

	c := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	hello := c.hello()
	got, _ := hello["session"].(map[string]any)
	if got["state"] != store.StateRunning {
		t.Fatalf("the socket opened on a session that was not resumed: %#v", got)
	}
	notice := c.waitCtrl("notice")
	if notice["kind"] != "resumed" {
		t.Fatalf("the notice was %#v", notice)
	}
	c.waitFor("FAKE claude")
}

// blackHole is a connection that stops carrying anything the moment the test
// says so, without closing: it is a phone driving into a tunnel, which is not
// the same event as a socket being closed and is the one the server cannot see
// coming.
type blackHole struct {
	net.Conn
	mu      sync.Mutex
	blocked bool
	release chan struct{}
}

func (c *blackHole) Read(p []byte) (int, error) {
	c.mu.Lock()
	blocked, release := c.blocked, c.release
	c.mu.Unlock()
	if blocked {
		<-release
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}

func (c *blackHole) Write(p []byte) (int, error) {
	c.mu.Lock()
	blocked := c.blocked
	c.mu.Unlock()
	if blocked {
		return len(p), nil // Swallowed, like a packet in a tunnel.
	}
	return c.Conn.Write(p)
}

func (c *blackHole) Close() error {
	c.offline(false)
	return c.Conn.Close()
}

func (c *blackHole) offline(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked = on
	if !on && c.release != nil {
		close(c.release)
		c.release = nil
	}
	if on && c.release == nil {
		c.release = make(chan struct{})
	}
}

// blackHoleClient is an HTTP client whose one connection can be cut off.
func blackHoleClient() (*http.Client, func(bool)) {
	var (
		mu    sync.Mutex
		conns []*blackHole
	)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			hole := &blackHole{Conn: conn}
			mu.Lock()
			conns = append(conns, hole)
			mu.Unlock()
			return hole, nil
		},
	}}
	return client, func(on bool) {
		mu.Lock()
		defer mu.Unlock()
		for _, hole := range conns {
			hole.offline(on)
		}
	}
}

// TestTerminalOfflineThenOnline is the drive through a tunnel: the socket is
// not closed, it simply stops carrying anything, and the browser comes back on
// a new one with the same viewer id. The server has not noticed the old socket
// is dead, so this is a takeover, and everything the transport promises has to
// hold across it - the gap arrives, once, and the keys typed afterwards are
// acknowledged and reach the pane.
func TestTerminalOfflineThenOnline(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	client, setOffline := blackHoleClient()
	first := e.dialWSRaw(id, "viewer=tab-a&cols=100&rows=30",
		&websocket.DialOptions{HTTPClient: client})
	first.readInBackground()
	first.hello()
	first.send(1, "echo before-the-tunnel\r")
	first.waitFor("before-the-tunnel")
	rendered := first.settle()

	// Into the tunnel. Nothing is closed; the server still believes in this
	// socket.
	setOffline(true)
	t.Cleanup(func() { setOffline(false) })
	if _, err := e.tmux("send-keys", "-t", "soc_"+id, "echo while-offline", "Enter"); err != nil {
		t.Fatal(err)
	}
	e.waitForPane(id, "while-offline")

	// Out of the tunnel, on a new socket with the same viewer id.
	back := e.dialWS(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	hello := back.hello()
	if hello["viewer_fresh"] != false {
		t.Fatalf("the tab lost its place by going offline: %#v", hello)
	}
	ack := uint64(floatOf(t, hello, "input_ack"))
	if ack != 1 {
		t.Fatalf("input_ack = %d after the tunnel, want 1", ack)
	}
	back.waitFor("while-offline")
	if strings.Count(back.screen(), "while-offline") != 2 {
		t.Fatalf("the gap was not delivered exactly once:\n%q", back.screen())
	}

	// And the terminal takes input again, which is the half that wedged.
	back.send(ack+1, "echo after-the-tunnel\r")
	back.waitFor("after-the-tunnel")
	acked := back.waitCtrl("input_ack")
	if got := uint64(floatOf(t, acked, "seq")); got != ack+1 {
		t.Fatalf("the keystroke after the tunnel was acknowledged as %d, want %d", got, ack+1)
	}
	e.waitForPane(id, "after-the-tunnel")
}

// TestTerminalSlowReaderStaysFlat: a viewer that never reads must not grow the
// server and must not stall the pane. The ring is the only buffer there is, so
// the bytes it cannot hold are simply forgotten.
func TestTerminalSlowReaderStaysFlat(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	slow := e.dialWSRaw(id, "viewer=tab-slow&cols=100&rows=30", nil) // Never read.
	_ = slow
	fast := e.dialWS(id, "viewer=tab-fast&cols=100&rows=30")
	fast.hello()

	// Rather more than the ring holds, as fast as the shell can print it.
	// The marker is split in the command line so that the shell's echo of what
	// was typed cannot satisfy the wait: only the loop's own output can.
	fast.send(1, "i=0; while [ $i -lt 40000 ]; do echo \"line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"; i=$((i+1)); done; echo spin-d''one\r")
	fast.waitFor("spin-done")
	e.waitForPane(id, "spin-done")

	// The slow viewer's terminal is still attached and its ring is still one
	// megabyte: it fell behind in a fixed allocation instead of in the heap.
	var ring *termViewer
	for _, tv := range e.srv.hub.forSession(id) {
		if tv.viewerID == "tab-slow" {
			ring = tv
		}
	}
	if ring == nil {
		t.Fatal("the slow viewer was forgotten")
	}
	viewer := ring.current()
	if viewer == nil {
		t.Fatal("the slow viewer lost its terminal")
	}
	head, base := viewer.Ring().Head(), viewer.Ring().Base()
	if head-base > 1<<20 {
		t.Fatalf("the ring holds %d bytes, which is more than it was given", head-base)
	}
	// The pane went on producing while nobody read that socket, which is the
	// point: the reader's only job is to append, and it never waits for a
	// viewer. How much tmux renders for a client that is behind is tmux's
	// business, so the assertion is that it kept going and stayed bounded.
	if head < 100000 {
		t.Fatalf("the pane produced only %d bytes; the stalled viewer held it up", head)
	}
}

// TestTerminalCleanDetachEndsTheViewer: a tab that says goodbye does not
// deserve a ninety second grace, and its tmux client goes with it.
func TestTerminalCleanDetach(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	c := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	c.hello()
	c.sendJSON(map[string]any{"t": "bye"})

	deadline := time.Now().Add(10 * time.Second)
	for e.srv.hub.count(id) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := e.srv.hub.count(id); n != 0 {
		t.Fatalf("%d viewers survived a clean detach", n)
	}
	if !e.hasTmuxSession(id) {
		t.Fatal("a detach took the session with it")
	}
}

// ---------------------------------------------------------------- ordering

// orderedClient records the order frames arrive in, which the client above
// cannot: it keeps control and output frames in separate lists, and the one
// thing the protocol is strict about is that hello comes first.
type orderedClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu    sync.Mutex
	order []string
	out   []byte
	err   error
	done  chan struct{}
}

func (e *sessionEnv) dialOrdered(sessionID, query string) *orderedClient {
	e.t.Helper()
	conn, res, err := websocket.Dial(context.Background(), e.wsURL(sessionID, query), e.dialOptions(nil))
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		e.t.Fatalf("dial %s: %v (status %d)", query, err, status)
	}
	conn.SetReadLimit(8 << 20)
	c := &orderedClient{t: e.t, conn: conn, done: make(chan struct{})}
	e.t.Cleanup(func() { _ = conn.CloseNow() })
	go func() {
		defer close(c.done)
		for {
			typ, payload, err := conn.Read(context.Background())
			if err != nil {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
				return
			}
			c.mu.Lock()
			switch {
			case typ == websocket.MessageBinary && len(payload) >= 9:
				c.order = append(c.order, fmt.Sprintf("out:%d", binary.BigEndian.Uint64(payload[1:9])))
				c.out = append(c.out, payload[9:]...)
			case typ == websocket.MessageBinary:
				c.order = append(c.order, "bin")
			default:
				var frame map[string]any
				_ = json.Unmarshal(payload, &frame)
				kind, _ := frame["t"].(string)
				c.order = append(c.order, kind)
			}
			c.mu.Unlock()
		}
	}()
	return c
}

// firstFrame is the kind of the first frame this socket received.
func (c *orderedClient) firstFrame(within time.Duration) string {
	c.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.order) > 0 {
			first := c.order[0]
			c.mu.Unlock()
			return first
		}
		c.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return "<nothing at all>"
}

func (c *orderedClient) frames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

func (c *orderedClient) rendered() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return uint64(len(c.out))
}

func (c *orderedClient) screen() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.out)
}

func (c *orderedClient) close() {
	_ = c.conn.CloseNow()
	<-c.done
}

// clientCount is how many tmux clients are attached to a session, which is how
// a test tells a reused terminal from a fresh attach.
func (e *sessionEnv) clientCount(id string) int {
	out, _ := e.tmux("list-clients", "-t", "soc_"+id, "-F", "#{client_tty}")
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// TestTerminalHelloIsAlwaysFirst pins the one ordering rule the protocol has.
// hello carries replay_from, which decides whether the client resets its
// terminal, and input_ack, which decides how it renumbers what it holds:
// output or a size frame arriving in front of it is rendered into a screen
// that is about to be cleared, or numbered against an anchor that has not
// arrived.
func TestTerminalHelloIsAlwaysFirst(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)

	// A fresh connect, where tmux paints the moment it attaches.
	fresh := e.dialOrdered(id, "viewer=tab-a&cols=100&rows=30")
	if got := fresh.firstFrame(20 * time.Second); got != "hello" {
		t.Fatalf("a fresh connect began with %q: %v", got, fresh.frames())
	}
	fresh.close()

	// A reconnect that has a gap waiting for it in the ring.
	for i := 0; i < 10; i++ {
		if _, err := e.tmux("send-keys", "-t", "soc_"+id, fmt.Sprintf("echo gap-%d", i), "Enter"); err != nil {
			t.Fatal(err)
		}
		e.waitForPane(id, fmt.Sprintf("gap-%d", i))
		c := e.dialOrdered(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", fresh.rendered()))
		if got := c.firstFrame(20 * time.Second); got != "hello" {
			t.Fatalf("reconnect %d began with %q: %v", i, got, c.frames())
		}
		time.Sleep(100 * time.Millisecond)
		c.close()
	}

	// A reconnect at a size the window is not wearing: the size frame the
	// resize broadcasts must be behind hello, not in front of it.
	rotated := e.dialOrdered(id, "viewer=tab-a&cols=80&rows=24&since=1")
	if got := rotated.firstFrame(20 * time.Second); got != "hello" {
		t.Fatalf("a reconnect at a new size began with %q: %v", got, rotated.frames())
	}
	e.waitForWindow(id, "80x24")
	rotated.close()
}

// TestTerminalResyncKeepsHelloFirst is the same rule on the path where it
// matters most: the client is behind what the ring can serve, so the terminal
// is replaced and hello says replay_from 0 - a reset - which must not arrive
// after the repaint it is meant to precede.
func TestTerminalResyncKeepsHelloFirst(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()
	first.send(1, "echo ready\r")
	first.waitFor("ready")
	rendered := first.settle()
	first.drop()
	time.Sleep(200 * time.Millisecond)
	before := e.clientCount(id)

	// More than the ring holds, while the tab is away.
	if _, err := e.tmux("send-keys", "-t", "soc_"+id,
		"i=0; while [ $i -lt 30000 ]; do echo \"line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"; i=$((i+1)); done; echo over-d''one",
		"Enter"); err != nil {
		t.Fatal(err)
	}
	e.waitForPane(id, "over-done")
	time.Sleep(300 * time.Millisecond)

	c := e.dialOrdered(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	if got := c.firstFrame(20 * time.Second); got != "hello" {
		t.Fatalf("the resync began with %q: %v", got, c.frames())
	}
	back := e.dialWS(id, fmt.Sprintf("viewer=tab-b&cols=100&rows=30&since=%d", rendered))
	back.hello()
	if after := e.clientCount(id); after != before+1 {
		t.Fatalf("the resync left %d tmux clients, want %d", after, before+1)
	}
}

// TestTerminalReloadWithoutSinceResyncs: a tab that comes back with an empty
// terminal - the ordinary page reload - is told to start from nothing and is
// given a fresh attach. Replaying the ring to it would repaint the whole
// session, including the device attribute queries tmux wrote at the first
// attach, which the terminal would answer a second time into a pane that never
// asked.
func TestTerminalReloadWithoutSinceResyncs(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()
	first.send(1, "echo before-the-reload\r")
	first.waitFor("before-the-reload")
	first.settle()
	first.drop()
	time.Sleep(200 * time.Millisecond)

	second := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	hello := second.hello()
	if hello["viewer_fresh"] != false {
		t.Fatalf("the reload was not recognised as the same tab: %#v", hello)
	}
	if got := floatOf(t, hello, "replay_from"); got != 0 {
		t.Fatalf("replay_from = %v on a reload, want 0", got)
	}
	second.send(2, "echo after-the-reload\r")
	second.waitFor("after-the-reload")
	// The proof that this was a fresh attach and not a replay: the stream
	// starts at byte one of a new ring. A reused terminal would have resumed
	// at the old ring's base, hundreds of bytes in, and delivered the whole
	// session - including the device attribute queries tmux wrote when it
	// first attached, which the terminal would answer a second time into a
	// pane that never asked.
	second.mu.Lock()
	firstSeq := second.frames[0]
	second.mu.Unlock()
	if firstSeq != 1 {
		t.Fatalf("the reload was served from the old ring, at %d", firstSeq)
	}
	if n := e.clientCount(id); n != 1 {
		t.Fatalf("the reload left %d tmux clients, want 1", n)
	}
	if !strings.Contains(second.screen(), "before-the-reload") {
		t.Fatalf("the reload did not repaint the screen:\n%q", second.screen())
	}
}

// TestTerminalTakeoverOfAHalfOpenSocket is the case the takeover exists for:
// the old socket is not closed, it is simply not there any more. hello must
// not wait for a close handshake with a phone that has gone.
func TestTerminalTakeoverOfAHalfOpenSocket(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	halfOpen := e.dialWSRaw(id, "viewer=tab-a&cols=100&rows=30", nil) // Never read.
	_ = halfOpen
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	c := e.dialOrdered(id, "viewer=tab-a&cols=100&rows=30")
	if got := c.firstFrame(20 * time.Second); got != "hello" {
		t.Fatalf("the takeover began with %q: %v", got, c.frames())
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("hello took %s over a half-open socket", took)
	}
}

// narrowListener hands out sockets that can hold almost nothing, so that a
// client which stops reading blocks the server's writer within a frame or two
// rather than after the several megabytes a loopback connection will otherwise
// buffer on its own.
type narrowListener struct{ net.Listener }

func (l *narrowListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetWriteBuffer(4096)
			_ = tcp.SetReadBuffer(4096)
		}
	}
	return conn, err
}

// narrowServer is the same handler on a listener like that.
func (e *sessionEnv) narrowServer() *httptest.Server {
	e.t.Helper()
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.Listener = &narrowListener{Listener: ts.Listener}
	ts.Start()
	e.t.Cleanup(ts.Close)
	return ts
}

// TestTerminalSlowReaderIsGivenUpOn: a viewer whose write stalls is let go of
// - the server stops writing to it, its terminal waits out the grace, and the
// pane and the other viewers never notice.
//
// What the stalled client is told is bounded by TCP rather than by this code:
// the 1013 is written the moment the stuck frame is away, which is the moment
// the client starts reading again, and a client that never reads again cannot
// be told anything, because a socket whose buffer is full has no room for a
// close frame either. That is what the client's own ping watchdog is for.
func TestTerminalSlowReaderIsGivenUpOn(t *testing.T) {
	e := newSessionEnv(t)
	// A short guard for every socket in this test: a viewer that is reading
	// takes each frame in microseconds, so only one that has stopped trips it.
	e.srv.writeTimeout = 300 * time.Millisecond
	e.srv.hub.grace = time.Hour
	id := e.shellSession(100, 30)

	narrow := e.narrowServer()
	conn, _, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(narrow.URL, "http")+"/api/sessions/"+id+"/ws?viewer=tab-slow&cols=100&rows=30",
		e.dialOptions(nil))
	if err != nil {
		t.Fatalf("dial the stalled viewer: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	fast := e.dialWS(id, "viewer=tab-fast&cols=100&rows=30")
	fast.hello()
	fast.send(1, "timeout 3 base64 -w 90 /dev/urandom; echo spin-d''one\r")
	fast.waitFor("spin-done")

	var stalled *termViewer
	for _, tv := range e.srv.hub.forSession(id) {
		if tv.viewerID == "tab-slow" {
			stalled = tv
		}
	}
	if stalled == nil {
		t.Fatal("the stalled viewer was forgotten altogether")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		stalled.mu.Lock()
		gone := stalled.conn == nil
		stalled.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stalled.mu.Lock()
	live, kept := stalled.conn, stalled.viewer != nil
	stalled.mu.Unlock()
	if live != nil {
		t.Fatal("the server is still writing to a viewer that stopped taking frames")
	}
	if !kept {
		t.Fatal("the stalled viewer lost its terminal instead of its socket")
	}

	// The pane and the other viewer were never held up by it, and the stalled
	// tab can come back to the terminal it left.
	fast.send(2, "echo still-fine\r")
	fast.waitFor("still-fine")
	back := e.dialWS(id, "viewer=tab-slow&cols=100&rows=30")
	hello := back.hello()
	if hello["viewer_fresh"] != false {
		t.Fatalf("the stalled viewer lost its place: %#v", hello)
	}
}

// TestTerminalMalformedFrameIsAProtocolError: a client that breaks the framing
// is told so, and does not get to park a terminal in the ninety second grace -
// eight bad frames would otherwise fill the session's viewer table.
func TestTerminalMalformedFrameIsAProtocolError(t *testing.T) {
	e := newSessionEnv(t)
	e.srv.hub.grace = time.Hour
	id := e.shellSession(100, 30)

	cases := []struct {
		name  string
		frame []byte
	}{
		{"a frame with no header", nil},
		{"a header with no body", []byte{frameInput, 0, 0, 0, 0, 0, 0, 0}},
		{"the wrong kind byte", append([]byte{frameOutput}, make([]byte, 12)...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := e.dialWS(id, "viewer=tab-bad&cols=100&rows=30")
			client.hello()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := client.conn.Write(ctx, websocket.MessageBinary, c.frame); err != nil {
				t.Fatal(err)
			}
			cancel()
			if got := client.closeStatus(5 * time.Second); got != websocket.StatusProtocolError {
				t.Fatalf("%s closed with %v, want 1002", c.name, got)
			}
			deadline := time.Now().Add(5 * time.Second)
			for e.srv.hub.count(id) > 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if n := e.srv.hub.count(id); n != 0 {
				t.Fatalf("%s left %d viewers in the grace", c.name, n)
			}
		})
	}
	if !e.hasTmuxSession(id) {
		t.Fatal("a malformed frame took the session with it")
	}
}

// TestTerminalDeleteEndsTheViewers: deleting a session closes every window on
// to it - the connected ones with a reason, the ones waiting out their grace
// with their timers and terminals (§A.10 step 1).
func TestTerminalDeleteEndsTheViewers(t *testing.T) {
	e := newSessionEnv(t)
	e.srv.hub.grace = time.Hour
	id := e.shellSession(100, 30)

	dropped := e.dialWS(id, "viewer=tab-gone&cols=100&rows=30")
	dropped.hello()
	dropped.drop()
	watching := e.dialWS(id, "viewer=tab-here&cols=100&rows=30")
	watching.hello()
	deadline := time.Now().Add(5 * time.Second)
	for e.srv.hub.count(id) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	res, payload := e.do(t, e.client, "DELETE", "/api/sessions/"+id, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %#v", res.StatusCode, payload)
	}
	frame := watching.waitCtrl("error")
	if frame["fatal"] != true {
		t.Fatalf("the viewer was not told the session had gone: %#v", frame)
	}
	if got := watching.closeStatus(5 * time.Second); got != websocket.StatusGoingAway {
		t.Fatalf("the socket closed with %v, want 1001", got)
	}
	if n := e.srv.hub.count(id); n != 0 {
		t.Fatalf("%d viewers outlived the session", n)
	}
}

// TestTerminalViewerCapIsEnforced: a session holds at most eight viewers, which
// is the bound on what a grace period can cost.
func TestTerminalViewerCapIsEnforced(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	for i := 0; i < maxViewersPerSession; i++ {
		c := e.dialWS(id, fmt.Sprintf("viewer=tab-%d&cols=100&rows=30", i))
		c.hello()
	}
	over := e.dialWS(id, "viewer=tab-too-many&cols=100&rows=30")
	if got := over.closeStatus(5 * time.Second); got != websocket.StatusTryAgainLater {
		t.Fatalf("the ninth viewer was closed with %v, want 1013", got)
	}
	if n := e.srv.hub.count(id); n != maxViewersPerSession {
		t.Fatalf("the session holds %d viewers, want %d", n, maxViewersPerSession)
	}
}

// TestTerminalReconnectIsNotRateLimited: the handshake ceiling is there to stop
// a broken client starting terminals, not to punish a phone that keeps losing
// its connection. A viewer the server still remembers is always let back in.
func TestTerminalReconnectIsNotRateLimited(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	known := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	known.hello()
	rendered := known.settle()
	known.drop()

	// Spend the whole minute's allowance on new terminals.
	e.srv.loginMu.Lock()
	e.srv.wsRate[clientIPOfTest] = &attempt{count: handshakeBurst + 5, until: time.Now().Add(handshakeWindow)}
	e.srv.loginMu.Unlock()

	back := e.dialWS(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	hello := back.hello()
	if hello["viewer_fresh"] != false {
		t.Fatalf("the reconnect was not the remembered viewer: %#v", hello)
	}
	// A tab the server has never seen is still refused.
	opts := e.dialOptions(nil)
	conn, res, err := websocket.Dial(context.Background(), e.wsURL(id, "viewer=tab-new&cols=100&rows=30"), opts)
	if err == nil {
		conn.CloseNow()
		t.Fatal("a new viewer was let through the ceiling")
	}
	if res == nil || res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a new viewer was refused with %v, want 429", res)
	}
}

// clientIPOfTest is the address httptest clients connect from.
const clientIPOfTest = "127.0.0.1"

// TestTerminalEveryKeystrokeAfterHelloReachesThePane is the guard on the one
// promise a terminal cannot bend: what was typed is what the pane ran.
//
// It types two hundred bytes one frame at a time, the way a keyboard does,
// starting the instant hello arrives - which is the window the page used to
// lose input in, because a second attach reset the counter the server had
// already moved past and every frame below it was discarded as a resend. Every
// byte is asserted twice over: on the pane through capture-pane, and in the
// journal, which is what a reconnect and the download are read from.
func TestTerminalEveryKeystrokeAfterHelloReachesThePane(t *testing.T) {
	e := newSessionEnv(t)
	// Wide enough that the echoed line is one row, so capture-pane compares
	// what was typed rather than how tmux wrapped it.
	id := e.shellSession(260, 30)

	c := e.dialWS(id, "viewer=tab-a&cols=260&rows=30")
	c.hello()

	// echo + a space + the marker + Return is exactly two hundred bytes.
	marker := strings.Repeat("abcdefghij", 19) + "0123"
	line := "echo " + marker + "\r"
	if len(line) != 200 {
		t.Fatalf("the line is %d bytes, want 200", len(line))
	}
	for i := 0; i < len(line); i++ {
		c.send(uint64(i+1), line[i:i+1])
	}

	// The pane ran it: the echo's own output line is the marker and nothing
	// else, which no partial delivery can produce.
	deadline := time.Now().Add(20 * time.Second)
	pane := ""
	for time.Now().Before(deadline) {
		pane, _ = e.tmux("capture-pane", "-p", "-J", "-t", termux.TmuxName(id))
		if strings.Contains(pane, "\n"+marker) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(pane, "\n"+marker) {
		t.Fatalf("the pane never ran the whole line; it shows:\n%s", pane)
	}

	// The server acknowledged every frame, so nothing is left held by a tab.
	want := float64(len(line))
	c.await("an input_ack for every keystroke", 20*time.Second, func() bool {
		for _, frame := range c.ctrl {
			if frame["t"] == "input_ack" && frame["seq"] == want {
				return true
			}
		}
		return false
	})

	// And the journal, which is the reconnect and the download, holds it too.
	c.waitFor(marker)
	res, err := e.client.Get(e.server.URL + "/api/sessions/" + id + "/journal")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	journal, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(journal), marker) {
		t.Fatalf("the journal is missing the line the pane ran (%d bytes)", len(journal))
	}
}

// TestTerminalAHandshakeNeverStrandsTheViewer is the guard on the wake-up
// storm: a phone coming back fires online and visibilitychange together, so
// the first socket is abandoned while the server is still attaching its
// terminal and two more arrive on top of it.
//
// Every one of them must end in something the client can act on - a hello, or
// a close with a status it comes back on. A handshake that does neither is a
// viewer nobody can ever reach again, which is what happened while a socket
// that died before its writer existed left the next takeover waiting for a
// goroutine that was never started.
func TestTerminalAHandshakeNeverStrandsTheViewer(t *testing.T) {
	e := newSessionEnv(t)
	e.srv.hub.grace = time.Hour
	id := e.shellSession(100, 30)

	// The first socket goes without a word while the attach is still running.
	e.dialWSRaw(id, "viewer=tab-a&cols=100&rows=30", nil).drop()

	// The client comes back for as long as it is told to; what is under test
	// is that it is always told something, and that being told it eventually
	// gets it a terminal.
	var live *wsClient
	attempt := 0
	for deadline := time.Now().Add(60 * time.Second); live == nil && time.Now().Before(deadline); attempt++ {
		c := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
		frame, status, silent := c.helloOrClose(20 * time.Second)
		switch {
		case silent:
			t.Fatalf("handshake %d said nothing at all: the viewer is stranded", attempt)
		case frame != nil:
			if frame["t"] != "hello" {
				t.Fatalf("handshake %d began with %v, not hello", attempt, frame)
			}
			live = c
		case status == websocket.StatusTryAgainLater:
			// "still being attached", or a hello that could not be written -
			// both are statuses a client comes back on, which is the point.
			time.Sleep(200 * time.Millisecond)
		default:
			t.Fatalf("handshake %d closed with %v, which no client retries on", attempt, status)
		}
	}
	if live == nil {
		t.Fatalf("%d handshakes over a minute and none of them was served", attempt)
	}

	// And the viewer that was served is a working terminal, not a shell of one.
	live.send(1, "echo stranded-no\r")
	live.waitFor("stranded-no")
}

// TestTerminalLateTerminalReplyNeverReachesThePane is the defect a person
// could see: xterm.js answers the questions tmux asks on an attach, and when
// the answer arrives late - across an outage, a slow tunnel, a phone waking -
// tmux is no longer waiting for it and types it into the pane, in front of the
// command the person meant to run.
//
// Nothing asks the browser anything any more, so a report arriving here is
// stale by construction and is dropped. What the person typed still runs.
func TestTerminalLateTerminalReplyNeverReachesThePane(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	first := e.dialWS(id, "viewer=tab-a&cols=100&rows=30")
	first.hello()
	first.send(1, "echo before-the-outage\r")
	first.waitFor("before-the-outage")
	rendered := first.settle()
	first.drop()

	back := e.dialWS(id, fmt.Sprintf("viewer=tab-a&cols=100&rows=30&since=%d", rendered))
	hello := back.hello()
	ack := uint64(floatOf(t, hello, "input_ack"))

	// The replies the browser held while it was away, in the order xterm.js
	// sends them, and then the command.
	back.send(ack+1, "\x1b[?1;2c\x1b[>0;276;0c\x1b[8;30;100t\x1b[4;540;800t")
	back.send(ack+2, "echo after-the-outage\r")
	back.waitFor("after-the-outage")
	time.Sleep(500 * time.Millisecond)

	screen, err := e.tmux("capture-pane", "-p", "-t", "soc_"+id)
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	for _, corruption := range []string{"1;2c", "0;276;0c", "8;30;100t", "command not found"} {
		if strings.Contains(screen, corruption) {
			t.Fatalf("a stale terminal report was typed into the pane (%q):\n%s", corruption, screen)
		}
	}
	// The command line the shell echoed and the line it printed: the command
	// ran, and it ran as it was typed.
	if n := strings.Count(screen, "after-the-outage"); n != 2 {
		t.Fatalf("the command after the outage ran %d times, want twice:\n%s", n, screen)
	}
	journal, err := termux.TailJournal(e.srv.dataDir, id, 1<<20)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	if bytes.Contains(journal, []byte("\x1b[?1;2c")) || bytes.Contains(journal, []byte("1;2c0;276;0c")) {
		t.Fatalf("the journal carries a device attributes reply:\n%q", journal)
	}
}
