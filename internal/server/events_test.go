//go:build !windows

package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// The event stream is tested the way a page without a terminal uses it: a
// real WebSocket against httptest, and what has to be proven is that every
// change a page needs reaches it without a poll, that a hello is the whole
// state, and that a socket nobody reads can never hold the detector up.

// eventsURL is the stream's address on a test server.
func (e *testEnv) eventsURL() string {
	return "ws" + strings.TrimPrefix(e.server.URL, "http") + "/api/events"
}

// eventsHeader is the cookie of the signed in client, as a browser sends it
// with the handshake.
func (e *testEnv) eventsHeader(t *testing.T) http.Header {
	t.Helper()
	u, err := url.Parse(e.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var pairs []string
	for _, cookie := range e.client.Jar.Cookies(u) {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	header := http.Header{}
	header.Set("Cookie", strings.Join(pairs, "; "))
	return header
}

// dialEvents opens the stream as the signed in page does, and reads it in the
// background.
func (e *testEnv) dialEvents(t *testing.T) *wsClient {
	t.Helper()
	c := e.dialEventsRaw(t)
	c.readInBackground()
	return c
}

// dialEventsRaw opens the stream and leaves it unread, which is what a slow
// subscriber is.
func (e *testEnv) dialEventsRaw(t *testing.T) *wsClient {
	t.Helper()
	conn, res, err := websocket.Dial(context.Background(), e.eventsURL(), &websocket.DialOptions{
		HTTPHeader:   e.eventsHeader(t),
		Subprotocols: []string{eventsSubprotocol},
	})
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("dial the event stream: %v (status %d)", err, status)
	}
	conn.SetReadLimit(64 << 20)
	c := &wsClient{t: t, conn: conn, closed: make(chan struct{}), woke: make(chan struct{}, 1)}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return c
}

// signedIn is a server with a password, a signed in client and no terminal
// substrate: the stream needs none.
func signedIn(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	if res, _ := e.do(t, e.client, "POST", "/api/setup", `{"password":"a-good-password"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", res.StatusCode)
	}
	return e
}

// frameWhere waits for a frame of one kind that satisfies a condition.
func (c *wsClient) frameWhere(what, kind string, within time.Duration, cond func(map[string]any) bool) map[string]any {
	c.t.Helper()
	var found map[string]any
	c.await(what, within, func() bool {
		for _, frame := range c.ctrl {
			if frame["t"] == kind && cond(frame) {
				found = frame
				return true
			}
		}
		return false
	})
	return found
}

// listHas is whether a sessions frame carries a row with that id and, when
// title is not empty, that title.
func listHas(frame map[string]any, id, title string) bool {
	rows, _ := frame["sessions"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["id"] == id && (title == "" || row["title"] == title) {
			return true
		}
	}
	return false
}

// The hello is the whole state, in the shape GET /api/home answers, and every
// change to the list afterwards arrives as the whole list again - a create, a
// rename and a delete, none of which a page without a terminal heard about
// before.
func TestEventStreamHelloAndTheList(t *testing.T) {
	e := newSessionEnv(t)
	client := e.dialEvents(t)
	hello := client.hello()
	for _, key := range []string{"sessions", "rev", "digests", "answers", "now"} {
		if _, ok := hello[key]; !ok {
			t.Fatalf("hello has no %q: %#v", key, hello)
		}
	}
	if rows, _ := hello["sessions"].([]any); len(rows) != 0 {
		t.Fatalf("a fresh server's hello lists %d sessions", len(rows))
	}

	id := e.shellSession(100, 30)
	client.frameWhere("a list with the new session", "sessions", 10*time.Second, func(f map[string]any) bool {
		return listHas(f, id, "")
	})

	if res, payload := e.do(t, e.client, "PATCH", "/api/sessions/"+id, `{"title":"Renamed here"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %#v", res.StatusCode, payload)
	}
	renamed := client.frameWhere("a list with the new name", "sessions", 10*time.Second, func(f map[string]any) bool {
		return listHas(f, id, "Renamed here")
	})
	if rev, _ := renamed["rev"].(float64); int64(rev) > e.store.Rev() || rev <= hello["rev"].(float64) {
		t.Fatalf("the list says rev %v; the store is at %d and the hello said %v", renamed["rev"], e.store.Rev(), hello["rev"])
	}

	if res, payload := e.do(t, e.client, "DELETE", "/api/sessions/"+id, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %#v", res.StatusCode, payload)
	}
	client.frameWhere("a list without the session", "sessions", 10*time.Second, func(f map[string]any) bool {
		rows, _ := f["sessions"].([]any)
		return len(rows) == 0
	})

	// The poll a page falls back to says the same as a hello, without the t.
	res, home := e.do(t, e.client, "GET", "/api/home", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/home: %d", res.StatusCode)
	}
	for _, key := range []string{"sessions", "rev", "digests", "answers", "now"} {
		if _, ok := home[key]; !ok {
			t.Fatalf("/api/home has no %q: %#v", key, home)
		}
	}
	if _, ok := home["t"]; ok {
		t.Fatalf("/api/home carries a frame kind: %#v", home)
	}
}

// The lists are coalesced: a burst of writes is one frame a quarter of a
// second later, not one per write.
func TestEventStreamCoalescesTheList(t *testing.T) {
	e := newSessionEnv(t)
	id := e.shellSession(100, 30)
	client := e.dialEvents(t)
	client.hello()

	for i := range 20 {
		if res, _ := e.do(t, e.client, "PATCH", "/api/sessions/"+id, fmt.Sprintf(`{"title":"burst %d"}`, i)); res.StatusCode != http.StatusOK {
			t.Fatalf("rename %d: %d", i, res.StatusCode)
		}
	}
	client.frameWhere("the last name", "sessions", 10*time.Second, func(f map[string]any) bool {
		return listHas(f, id, "burst 19")
	})
	lists := 0
	client.mu.Lock()
	for _, frame := range client.ctrl {
		if frame["t"] == "sessions" {
			lists++
		}
	}
	client.mu.Unlock()
	if lists >= 20 {
		t.Fatalf("twenty renames were %d lists", lists)
	}
}

// Activity and titles are the frames the terminal sockets already hear; the
// stream hears them too. A ping is answered with its own id, and the tick is
// what tells a quiet stream from a dead one.
func TestEventStreamForwardsActivityTitlesAndPings(t *testing.T) {
	e := signedIn(t)
	e.srv.events.tickEvery = 50 * time.Millisecond
	client := e.dialEvents(t)
	client.hello()

	e.srv.onSessionActivity("s-somebody", termux.Activity{State: termux.StateBusy, Since: 7})
	activity := client.frameWhere("the activity", "activity", 5*time.Second, func(f map[string]any) bool {
		sessions, _ := f["sessions"].(map[string]any)
		return sessions["s-somebody"] != nil
	})
	if one, _ := activity["sessions"].(map[string]any)["s-somebody"].(map[string]any); one["state"] != "busy" {
		t.Fatalf("activity = %#v", activity)
	}

	e.srv.emitTitle("s-somebody", titlePhaseDone, "A new name", "")
	first := client.frameWhere("the title", "title", 5*time.Second, func(f map[string]any) bool {
		return f["id"] == "s-somebody" && f["phase"] == titlePhaseDone && f["title"] == "A new name"
	})
	// A page hears every title twice - here and on its terminal socket - and
	// tells the two apart by the number, so two frames never share one.
	e.srv.emitTitle("s-somebody", titlePhaseDone, "A new name", "")
	second := client.frameWhere("the same title again", "title", 5*time.Second, func(f map[string]any) bool {
		return f["id"] == "s-somebody" && f["seq"] != first["seq"]
	})
	if a, b := first["seq"].(float64), second["seq"].(float64); a <= 0 || b <= a || b > 1<<53 {
		t.Fatalf("title numbers %v then %v: want two increasing numbers a browser reads exactly", a, b)
	}

	client.sendJSON(map[string]any{"t": "ping", "id": 7})
	client.sendJSON(map[string]any{"t": "something else"})
	client.frameWhere("the pong", "pong", 5*time.Second, func(f map[string]any) bool {
		return f["id"] == float64(7)
	})
	tick := client.frameWhere("a tick", "tick", 5*time.Second, func(map[string]any) bool { return true })
	if now, _ := tick["now"].(float64); now <= 0 {
		t.Fatalf("tick = %#v", tick)
	}
}

// publish is called from the detector's tick, so a subscriber that has stopped
// reading must cost it nothing: it is dropped the moment its queue is full,
// and publish carries on.
func TestEventHubDropsASlowSubscriberWithoutBlocking(t *testing.T) {
	e := signedIn(t)
	hub := e.srv.events

	sub, _, err := hub.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for i := 0; i < eventsBuffer*4; i++ {
		hub.publish(map[string]any{"t": "activity", "n": i})
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("publishing to a subscriber nobody reads took %s", took)
	}
	select {
	case <-sub.gone:
	default:
		t.Fatal("a subscriber with a full queue was not dropped")
	}
	if n := hub.count(); n != 0 {
		t.Fatalf("the hub still counts %d subscribers", n)
	}
	// With nobody left the watcher is stopped: it only runs while somebody
	// is listening.
	hub.mu.Lock()
	watching := hub.stopWatch != nil
	hub.mu.Unlock()
	if watching {
		t.Fatal("the list watcher still runs with nobody listening")
	}
}

// And the same through a real socket: a page that stops reading is cut off,
// and the hub forgets it, while publishing never waits for it.
func TestEventStreamCutsOffASocketThatStoppedReading(t *testing.T) {
	e := signedIn(t)
	e.srv.writeTimeout = time.Second
	stuck := e.dialEventsRaw(t)

	deadline := time.Now().Add(5 * time.Second)
	for e.srv.events.count() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the socket never subscribed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Large frames, so that the kernel's buffers fill and the writer stops,
	// and then the queue behind it.
	big := incompressible(256 << 10)
	began := time.Now()
	for i := 0; i < 400 && e.srv.events.count() > 0; i++ {
		e.srv.events.publish(map[string]any{"t": "activity", "pad": big})
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("publishing took %s with a subscriber that does not read", took)
	}
	if n := e.srv.events.count(); n != 0 {
		t.Fatalf("the hub still counts %d subscribers", n)
	}
	stuck.readInBackground()
	status := stuck.closeStatus(30 * time.Second)
	if status != websocket.StatusPolicyViolation && status != -1 {
		t.Fatalf("the socket ended with %d", status)
	}
}

// The stream carries every session's title and question, so it is behind the
// password and behind the origin check, like the terminal.
func TestEventStreamRefusesStrangers(t *testing.T) {
	e := signedIn(t)

	_, res, err := websocket.Dial(context.Background(), e.eventsURL(), &websocket.DialOptions{
		Subprotocols: []string{eventsSubprotocol},
	})
	if err == nil {
		t.Fatal("an anonymous client opened the stream")
	}
	if res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous handshake = %v", res)
	}

	header := e.eventsHeader(t)
	header.Set("Origin", "https://evil.example")
	_, res, err = websocket.Dial(context.Background(), e.eventsURL(), &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{eventsSubprotocol},
	})
	if err == nil {
		t.Fatal("a foreign page opened the stream with the cookie")
	}
	if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("a foreign origin's handshake = %v", res)
	}

	if res, _ := e.do(t, e.anon, "GET", "/api/home", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous /api/home = %d", res.StatusCode)
	}
}

// The watcher runs while somebody listens, and stops with the last one.
func TestEventStreamWatchesOnlyWhileSomebodyListens(t *testing.T) {
	e := signedIn(t)
	watching := func() bool {
		e.srv.events.mu.Lock()
		defer e.srv.events.mu.Unlock()
		return e.srv.events.stopWatch != nil
	}
	if watching() {
		t.Fatal("the watcher runs with nobody listening")
	}
	client := e.dialEvents(t)
	client.hello()
	if !watching() {
		t.Fatal("the watcher does not run with somebody listening")
	}
	if err := client.conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for watching() || e.srv.events.count() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the watcher kept running after the last page left")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A hello's digests and runs are maps by session id, even when empty, so a
// page can index them without a check.
func TestHomeStateShapes(t *testing.T) {
	e := signedIn(t)
	res, home := e.do(t, e.client, "GET", "/api/home", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/home: %d", res.StatusCode)
	}
	raw, _ := json.Marshal(home)
	for _, want := range []string{`"digests":{}`, `"answers":{}`, `"sessions":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("/api/home = %s, want %s", raw, want)
		}
	}
}

// A list read over HTTP - GET /api/sessions, and /api/home's - carries a rev
// no newer than its rows: every session written at or before that rev is in
// them. The rev read after the rows would be the rev of a write the rows do
// not hold, and a page that has the stream's list of that rev already draws
// these older rows over it. Sessions are written while lists are read, and
// every list is held to every write.
func TestListsAreNeverOlderThanTheirRev(t *testing.T) {
	e := signedIn(t)
	type list struct {
		rev int64
		ids map[string]bool
	}
	var (
		mu      sync.Mutex
		lists   []list
		written = map[string]int64{}
	)
	stop := make(chan struct{})
	var readers sync.WaitGroup
	read := func(get func() (int64, []string)) {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			rev, ids := get()
			l := list{rev: rev, ids: map[string]bool{}}
			for _, id := range ids {
				l.ids[id] = true
			}
			mu.Lock()
			lists = append(lists, l)
			mu.Unlock()
		}
	}
	readers.Add(2)
	go read(func() (int64, []string) {
		rec := httptest.NewRecorder()
		e.srv.handleListSessions(rec, httptest.NewRequest("GET", "/api/sessions", nil))
		var body struct {
			Rev      int64 `json:"rev"`
			Sessions []struct {
				ID string `json:"id"`
			} `json:"sessions"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		ids := make([]string, 0, len(body.Sessions))
		for _, s := range body.Sessions {
			ids = append(ids, s.ID)
		}
		return body.Rev, ids
	})
	go read(func() (int64, []string) {
		state, err := e.srv.homeState()
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		views, _ := json.Marshal(state["sessions"])
		var rows []struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(views, &rows)
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return state["rev"].(int64), ids
	})
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("w%03d", i)
		if err := e.store.CreateSession(&store.Session{ID: id, Title: id, Harness: "shell",
			Workdir: t.TempDir(), State: store.StateRunning}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		written[id] = e.store.Rev()
		mu.Unlock()
	}
	close(stop)
	readers.Wait()
	for _, l := range lists {
		for id, rev := range written {
			if rev <= l.rev && !l.ids[id] {
				t.Fatalf("a list of rev %d has no %s, which was written at rev %d", l.rev, id, rev)
			}
		}
	}
	if len(lists) < 10 {
		t.Fatalf("only %d lists were read", len(lists))
	}
}

// incompressible is n bytes of text no compressor can shrink, so that what is
// published is what reaches the wire.
func incompressible(n int) string {
	buf := make([]byte, n/2)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// closeCode is how a stream the server closed was closed, once it has been.
func (c *wsClient) closeCode(within time.Duration) websocket.StatusCode {
	c.t.Helper()
	select {
	case <-c.closed:
	case <-time.After(within):
		c.t.Fatal("the stream was not closed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return websocket.CloseStatus(c.readErr)
}

// Signing out, or every login being revoked by a new password, ends every
// stream of those logins at once and says why; one whose login lapsed some
// other way ends at its next tick. A stream that is still open carries every
// session's title, summary and question.
func TestEventStreamEndsWithItsLogin(t *testing.T) {
	e := signedIn(t)
	e.srv.events.tickEvery = 100 * time.Millisecond
	first := e.dialEvents(t)
	first.hello()

	// A new password signs everybody out - this page gets a new login, and
	// its old stream ends with the others.
	res, _ := e.do(t, e.client, "POST", "/api/settings/password",
		`{"current":"a-good-password","next":"another-good-password"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change the password: %d", res.StatusCode)
	}
	if code := first.closeCode(5 * time.Second); code != eventsSignedOut {
		t.Fatalf("a revoked stream was closed with %d", code)
	}

	second := e.dialEvents(t)
	second.hello()
	if res, _ := e.do(t, e.client, "POST", "/api/logout", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	if code := second.closeCode(5 * time.Second); code != eventsSignedOut {
		t.Fatalf("a signed out stream was closed with %d", code)
	}

	// A login that is gone without anybody signing out - expired, or deleted
	// under the server - is found at the stream's next tick.
	if res, _ := e.do(t, e.client, "POST", "/api/login", `{"password":"another-good-password"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", res.StatusCode)
	}
	third := e.dialEvents(t)
	third.hello()
	if err := e.store.DeleteAllLogins(); err != nil {
		t.Fatal(err)
	}
	if code := third.closeCode(5 * time.Second); code != eventsSignedOut {
		t.Fatalf("a stream whose login lapsed was closed with %d", code)
	}
}

// Switching between tabs opens a stream every time a tab is shown. That is a
// person using the app, not a broken client, and it is counted per login, not
// per address a whole household shares.
func TestEventStreamHandshakesAreCountedPerLogin(t *testing.T) {
	e := signedIn(t)
	for i := 0; i < handshakeBurst+10; i++ {
		c := e.dialEventsRaw(t)
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	}
	e.srv.loginMu.Lock()
	for key, a := range e.srv.wsRate {
		if strings.HasPrefix(key, "events ") && strings.Contains(key, clientIPOfTest) {
			t.Errorf("the stream's handshakes are counted by address: %q %d", key, a.count)
		}
	}
	e.srv.loginMu.Unlock()
}

// A pong says that every frame published before its ping has arrived. A
// writer that comes back from a long write to frames and a pong both waiting
// writes the frames first, whichever the select would have picked.
func TestEventStreamPongFollowsTheFramesBeforeItsPing(t *testing.T) {
	e := signedIn(t)
	big := incompressible(4 << 20)
	for round := 0; round < 5; round++ {
		c := e.dialEventsRaw(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		read := func() map[string]any {
			t.Helper()
			_, payload, err := c.conn.Read(ctx)
			if err != nil {
				t.Fatalf("round %d: read: %v", round, err)
			}
			var frame map[string]any
			if err := json.Unmarshal(payload, &frame); err != nil {
				t.Fatal(err)
			}
			return frame
		}
		if hello := read(); hello["t"] != "hello" {
			t.Fatalf("the first frame was %v", hello["t"])
		}
		// The writer stops on the large frames while nothing is read; the
		// small one queues behind them, and the ping comes after it.
		for i := 0; i < 4; i++ {
			e.srv.events.publish(map[string]any{"t": "activity", "pad": big})
		}
		e.srv.events.publish(map[string]any{"t": "activity", "last": true})
		if err := c.conn.Write(ctx, websocket.MessageText, []byte(`{"t":"ping","id":1}`)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		last := false
		for {
			frame := read()
			if frame["last"] == true {
				last = true
			}
			if frame["t"] == "pong" {
				break
			}
		}
		if !last {
			t.Fatalf("round %d: the pong came before a frame published ahead of its ping", round)
		}
		cancel()
		_ = c.conn.CloseNow()
	}
}

// Home's state and the session list go out gzipped to a client that takes
// it - the page that polls is the page on a bad link - and as they are to one
// that does not.
func TestHomeStateIsCompressed(t *testing.T) {
	e := signedIn(t)
	for i := 0; i < 20; i++ {
		if err := e.store.CreateSession(&store.Session{
			ID: fmt.Sprintf("z%02d", i), Title: strings.Repeat("A long title ", 8), Harness: "claude",
			Workdir: t.TempDir(), TmuxName: fmt.Sprintf("t-z%02d", i), State: store.StateRunning,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/home", "/api/sessions"} {
		for _, c := range []struct {
			accept string
			gzip   bool
		}{{"gzip, deflate, br", true}, {"identity", false}, {"gzip;q=0", false}, {"br, gzip; q=0.5", true}} {
			req, err := http.NewRequest("GET", e.server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Set by hand, the transport leaves the body as it came.
			req.Header.Set("Accept-Encoding", c.accept)
			res, err := e.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d %v", path, res.StatusCode, err)
			}
			if got := res.Header.Get("Content-Encoding") == "gzip"; got != c.gzip {
				t.Fatalf("GET %s with %q: Content-Encoding %q", path, c.accept, res.Header.Get("Content-Encoding"))
			}
			if c.gzip {
				zr, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				if body, err = io.ReadAll(zr); err != nil {
					t.Fatal(err)
				}
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil || len(decoded["sessions"].([]any)) != 20 {
				t.Fatalf("GET %s with %q: %v %.200s", path, c.accept, err, body)
			}
		}
	}
}

// Every session committing at once - the first detector tick after a restart -
// is a burst of frames no writer sends as fast as they come, and a page that
// reads them all is not a slow one: its stream stays open, and hears the last
// of them.
func TestEventStreamHoldsABurstOfEverySession(t *testing.T) {
	e := signedIn(t)
	const n = 150
	for i := 0; i < n; i++ {
		row := store.Session{ID: fmt.Sprintf("s-%03d", i), Title: "burst", Harness: "claude", State: store.StateRunning}
		if err := e.store.CreateSession(&row); err != nil {
			t.Fatal(err)
		}
	}
	client := e.dialEvents(t)
	client.hello()
	for round, state := range []termux.State{termux.StateBusy, termux.StateIdle} {
		for i := 0; i < n; i++ {
			e.srv.onSessionActivity(fmt.Sprintf("s-%03d", i), termux.Activity{State: state, Since: int64(round + 1)})
		}
	}
	last := fmt.Sprintf("s-%03d", n-1)
	client.frameWhere("the last frame of the burst", "activity", 10*time.Second, func(f map[string]any) bool {
		one, _ := f["sessions"].(map[string]any)[last].(map[string]any)
		return one != nil && one["state"] == string(termux.StateIdle)
	})
	select {
	case <-client.closed:
		t.Fatalf("the stream of a page that reads it was closed: %v", websocket.CloseStatus(client.readErr))
	default:
	}
	if got := e.srv.events.count(); got != 1 {
		t.Fatalf("the hub counts %d subscribers", got)
	}
}
