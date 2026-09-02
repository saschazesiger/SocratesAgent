//go:build !windows

package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/saschazesiger/SocratesAgent/internal/store"
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
	if got := silent.closeStatus(5 * time.Second); got != websocket.StatusPolicyViolation {
		t.Fatalf("the silent viewer closed with %v, want 1008", got)
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
	fast.send(1, "i=0; while [ $i -lt 40000 ]; do echo \"line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"; i=$((i+1)); done; echo spin-done\r")
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
