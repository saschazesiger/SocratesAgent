//go:build !windows

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// The rule for a finished turn is the page's (notify.completed): a turn that
// ended, with an unread mark this very change set. A question is asks's.
func TestPushNewsIsTheChimeRule(t *testing.T) {
	act := func(s termux.State, unread bool) termux.Activity {
		return termux.Activity{State: s, Unread: unread}
	}
	cases := []struct {
		name       string
		prev, next termux.Activity
		want       string
	}{
		{"finished", act(termux.StateBusy, false), act(termux.StateIdle, true), "Finished"},
		{"needs you, which is a question", act(termux.StateBusy, false), act(termux.StateWaiting, true), ""},
		{"background work ended", act(termux.StateTasks, false), act(termux.StateIdle, true), "Finished"},
		{"start-up nobody asked for", act(termux.StateBusy, false), act(termux.StateIdle, false), ""},
		{"an old mark carried over a relaunch", act(termux.StateBusy, true), act(termux.StateIdle, true), ""},
		{"detector finding its footing", act(termux.StateUnknown, false), act(termux.StateIdle, true), ""},
		{"turn ended with work left running", act(termux.StateBusy, false), act(termux.StateTasks, false), ""},
		{"started working", act(termux.StateIdle, false), act(termux.StateBusy, false), ""},
	}
	for _, c := range cases {
		if got := news(c.prev, c.next); got != c.want {
			t.Errorf("%s: news = %q, want %q", c.name, got, c.want)
		}
	}
}

// A question is every new prompt somebody who typed at the session is
// stopped on: after work, after background work, and opened from the prompt
// - but not one that came back after a tick of unknown, and not a harness's
// own menu at startup, which nobody has typed at.
func TestPushAsksRule(t *testing.T) {
	act := func(s termux.State, unread bool) termux.Activity {
		return termux.Activity{State: s, Unread: unread}
	}
	cases := []struct {
		name       string
		prev, next termux.Activity
		want       bool
	}{
		{"after work", act(termux.StateBusy, false), act(termux.StateWaiting, true), true},
		{"after background work", act(termux.StateTasks, false), act(termux.StateWaiting, true), true},
		{"from the prompt", act(termux.StateIdle, false), act(termux.StateWaiting, true), true},
		{"from a turn nobody read", act(termux.StateIdle, true), act(termux.StateWaiting, true), true},
		{"back from unknown", act(termux.StateUnknown, false), act(termux.StateWaiting, true), false},
		{"a startup menu", act(termux.StateBusy, false), act(termux.StateWaiting, false), false},
		{"not a prompt", act(termux.StateBusy, false), act(termux.StateIdle, true), false},
	}
	for _, c := range cases {
		if got := asks(c.prev, c.next); got != c.want {
			t.Errorf("%s: asks = %v, want %v", c.name, got, c.want)
		}
	}
}

// setWait shortens how long a question waits for its words.
func (p *pusher) setWait(d time.Duration) {
	p.mu.Lock()
	p.wait = d
	p.mu.Unlock()
}

type sentPush struct {
	endpoint string
	note     pushNote
	msg      webpush.Message
}

// catch replaces the push service with a channel.
func catch(p *pusher) <-chan sentPush {
	out := make(chan sentPush, 16)
	var mu sync.Mutex
	p.send = func(_ context.Context, sub pushSub, msg webpush.Message) error {
		mu.Lock()
		defer mu.Unlock()
		var note pushNote
		json.Unmarshal(msg.Payload, &note)
		out <- sentPush{endpoint: sub.Endpoint, note: note, msg: msg}
		if sub.Endpoint == "https://push.example.net/gone" {
			return webpush.ErrGone
		}
		return nil
	}
	return out
}

func testSubscription(endpoint string) webpush.Subscription {
	var sub webpush.Subscription
	sub.Endpoint = endpoint
	// A valid P-256 point and secret, from RFC 8291 Appendix A.
	sub.Keys.P256dh = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	sub.Keys.Auth = "BTBZMqHH6r4Tts7J_aSIgg"
	return sub
}

func TestPushReachesEverySubscriberAndForgetsTheGone(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	sent := catch(p)
	for _, e := range []string{"https://push.example.net/a", "https://push.example.net/gone"} {
		if err := p.add(pushSub{Subscription: testSubscription(e)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []*store.Session{
		{ID: "s-agent", Title: "Claude Code · tests", Harness: "claude", Workdir: t.TempDir(), TmuxName: "t1"},
		{ID: "s-shell", Title: "Shell", Harness: "shell", Workdir: t.TempDir(), TmuxName: "t2"},
	} {
		if err := env.store.CreateSession(row); err != nil {
			t.Fatal(err)
		}
	}

	busy := termux.Activity{State: termux.StateBusy}
	done := termux.Activity{State: termux.StateIdle, Unread: true}
	// A first sighting is never an edge, even one that looks like a finish.
	p.observe("s-agent", done)
	p.observe("s-agent", busy)
	p.observe("s-agent", done)
	// A shell takes no turns.
	p.observe("s-shell", busy)
	p.observe("s-shell", done)

	got := map[string]sentPush{}
	for range 2 {
		select {
		case one := <-sent:
			got[one.endpoint] = one
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d pushes were sent", len(got))
		}
	}
	one := got["https://push.example.net/a"]
	if one.note.Title != "Claude Code · tests" || one.note.Body != "Finished" || one.note.ID != "s-agent" ||
		one.note.Kind != pushFinished {
		t.Errorf("note = %+v", one.note)
	}
	if one.msg.Topic == "" || len(one.msg.Topic) > 32 || one.msg.TTL <= 0 {
		t.Errorf("message = %+v", one.msg)
	}
	select {
	case extra := <-sent:
		t.Fatalf("an extra push was sent: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
	// The push service said gone, so that subscription is no more.
	deadline := time.Now().Add(5 * time.Second)
	for len(p.subs()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("subscriptions = %+v", p.subs())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPushEndpoints(t *testing.T) {
	env := newEnv(t)
	env.do(t, env.client, "POST", "/api/setup", `{"password":"a-good-password"}`)
	sent := catch(env.srv.push)

	res, body := env.do(t, env.client, "GET", "/api/push", "")
	key, _ := body["key"].(string)
	if res.StatusCode != http.StatusOK || len(key) != 87 {
		t.Fatalf("key = %d %v", res.StatusCode, body)
	}
	// The same key on the next ask: a new one would orphan every browser.
	if _, again := env.do(t, env.client, "GET", "/api/push", ""); again["key"] != key {
		t.Fatalf("the key changed: %v", again["key"])
	}
	if res, _ := env.do(t, env.anon, "GET", "/api/push", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous key = %d", res.StatusCode)
	}

	bad, _ := json.Marshal(map[string]any{"subscription": testSubscription("http://push.example.net/a")})
	if res, _ := env.do(t, env.client, "POST", "/api/push/subscribe", string(bad)); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an http endpoint = %d", res.StatusCode)
	}
	good, _ := json.Marshal(map[string]any{"subscription": testSubscription("https://push.example.net/a")})
	for range 2 {
		if res, _ := env.do(t, env.client, "POST", "/api/push/subscribe", string(good)); res.StatusCode != http.StatusOK {
			t.Fatalf("subscribe = %d", res.StatusCode)
		}
	}
	if subs := env.srv.push.subs(); len(subs) != 1 {
		t.Fatalf("subscribing twice kept %d", len(subs))
	}
	// A plain http page has no subject of its own to give.
	if subs := env.srv.push.subs(); subs[0].Subject != "mailto:socrates@example.com" {
		t.Fatalf("subject = %q", subs[0].Subject)
	}

	res, _ = env.do(t, env.client, "POST", "/api/push/test", `{"endpoint":"https://push.example.net/a"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("test = %d", res.StatusCode)
	}
	if one := <-sent; one.note.Body != "Notifications are on." || one.note.Kind != pushTest ||
		one.note.Badge == nil || *one.note.Badge != 0 || one.note.Asking == nil {
		t.Fatalf("test push = %+v", one.note)
	}
	if res, _ := env.do(t, env.client, "POST", "/api/push/test", `{"endpoint":"https://push.example.net/b"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("test for a stranger = %d", res.StatusCode)
	}

	if res, _ := env.do(t, env.client, "POST", "/api/push/unsubscribe", `{"endpoint":"https://push.example.net/a"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("unsubscribe = %d", res.StatusCode)
	}
	if subs := env.srv.push.subs(); len(subs) != 0 {
		t.Fatalf("still subscribed: %+v", subs)
	}
}

// What a tap on a notification opens is in the payload: a session that needs
// an answer opens its question, anything else opens the session.
func TestPushSaysWhatATapOpens(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	// Nothing reads this session's screen, so its question goes out with
	// the generic words when the wait is over.
	p.setWait(20 * time.Millisecond)
	sent := catch(p)
	if err := p.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.CreateSession(&store.Session{ID: "s-ask", Title: "Asks", Harness: "codex",
		Workdir: t.TempDir(), TmuxName: "t1"}); err != nil {
		t.Fatal(err)
	}
	next := func() sentPush {
		t.Helper()
		select {
		case one := <-sent:
			return one
		case <-time.After(5 * time.Second):
			t.Fatal("no push was sent")
		}
		return sentPush{}
	}

	p.observe("s-ask", termux.Activity{State: termux.StateIdle})
	p.observe("s-ask", termux.Activity{State: termux.StateBusy})
	p.observe("s-ask", termux.Activity{State: termux.StateWaiting, Unread: true})
	if one := next(); one.note.Body != "Needs an answer" || one.note.Kind != pushQuestion || one.msg.Urgency != "high" {
		t.Fatalf("a question = %+v %+v", one.note, one.msg)
	}

	p.observe("s-ask", termux.Activity{State: termux.StateBusy})
	p.observe("s-ask", termux.Activity{State: termux.StateIdle, Unread: true})
	if one := next(); one.note.Body != "Finished" || one.note.Kind != pushFinished {
		t.Fatalf("a finished turn = %+v", one.note)
	}

	env.srv.sched.announce("s-ask", "Started: Asks", "The scheduled start ran.", "normal")
	if one := next(); one.note.Kind != pushStarted || one.note.ID != "s-ask" || one.msg.Topic != pushTopic("s-ask") {
		t.Fatalf("a scheduled start = %+v %+v", one.note, one.msg)
	}
}

// A turn that ends by asking in prose is "Finished" to the detector and a
// question to Home. Once its digest has read the question, the phone is told
// the same as Home: the question, kind question, on the topic of the
// "Finished" it replaces. A turn that ends without a question stays finished,
// and so does one somebody has looked at since.
func TestAQuestionInProseIsPushedAsAQuestion(t *testing.T) {
	e := newDigestEnv(t)
	sent := catch(e.srv.push)
	if err := e.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	next := func(what string) sentPush {
		t.Helper()
		select {
		case one := <-sent:
			return one
		case <-time.After(10 * time.Second):
			t.Fatalf("no push was sent for %s", what)
		}
		return sentPush{}
	}
	none := func(what string) {
		t.Helper()
		select {
		case one := <-sent:
			t.Fatalf("%s pushed %+v", what, one.note)
		case <-time.After(time.Second):
		}
	}
	e.session("p1", config.HarnessClaude, "● Which color should the button be? Red, green or blue?\n❯ \n")
	turn := func(since int64, unread bool) {
		e.srv.onSessionActivity("p1", termux.Activity{State: termux.StateBusy, Since: since})
		e.srv.onSessionActivity("p1", termux.Activity{State: termux.StateIdle, Unread: unread, Since: since + 1})
	}

	e.gw.digestAlways(textQuestionJSON)
	turn(1, true)
	finished := next("the end of the turn")
	if finished.note.Body != "Finished" || finished.note.Kind != pushFinished {
		t.Fatalf("the end of the turn = %+v", finished.note)
	}
	e.digestOf("p1", "a question in prose", func(d digest) bool {
		return ready(d) && d.Question != nil && d.Question.Source == questionText
	})
	asked := next("the question in prose")
	if asked.note.Body != "Which color should the button be?" || asked.note.Kind != pushQuestion || asked.note.ID != "p1" ||
		asked.msg.Urgency != "high" || asked.msg.Topic != finished.msg.Topic {
		t.Fatalf("the question in prose = %+v %+v; the finished turn was on topic %q", asked.note, asked.msg, finished.msg.Topic)
	}
	if asked.note.Badge == nil || *asked.note.Badge != 1 || len(asked.note.Asking) != 1 || asked.note.Asking[0] != "p1" {
		t.Fatalf("the question in prose counts %v %v", asked.note.Badge, asked.note.Asking)
	}

	e.gw.digestAlways(`{"summary":"Done.","context":"","question":null}`)
	turn(10, true)
	if one := next("a turn without a question"); one.note.Kind != pushFinished {
		t.Fatalf("a turn without a question = %+v", one.note)
	}
	e.digestOf("p1", "read without a question", func(d digest) bool { return ready(d) && d.Question == nil && d.State == "idle" })
	none("a turn without a question")

	// Read by somebody before its digest came back: they have seen it.
	e.gw.digestBlock()
	e.gw.digestAlways(textQuestionJSON)
	turn(20, true)
	next("the end of the third turn")
	e.srv.onSessionActivity("p1", termux.Activity{State: termux.StateIdle, Since: 21})
	e.gw.digestRelease()
	e.digestOf("p1", "the third question", func(d digest) bool { return ready(d) && d.Question != nil })
	none("a question somebody has seen")
}

// A service filed background finishes turns so as not to interrupt anyone
// (DEVIATIONS.md: no chime, no notification): neither the end of its turn nor
// a question it asks, in prose or on a menu, reaches the phone. Filed
// background after its turn was announced, the question read afterwards is
// not announced either.
func TestABackgroundSessionIsNotPushed(t *testing.T) {
	e := newDigestEnv(t)
	sent := catch(e.srv.push)
	if err := e.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	none := func(what string) {
		t.Helper()
		select {
		case one := <-sent:
			t.Fatalf("%s pushed %+v", what, one.note)
		case <-time.After(time.Second):
		}
	}
	e.session("bg", config.HarnessClaude, "● Which color should the button be? Red, green or blue?\n❯ \n")
	if err := e.store.SetSessionStatus("bg", store.StatusBackground); err != nil {
		t.Fatal(err)
	}
	e.gw.digestAlways(textQuestionJSON)
	e.srv.onSessionActivity("bg", termux.Activity{State: termux.StateBusy, Since: 1})
	e.srv.onSessionActivity("bg", termux.Activity{State: termux.StateIdle, Unread: true, Since: 2})
	e.digestOf("bg", "a question in prose", func(d digest) bool {
		return ready(d) && d.Question != nil && d.Question.Source == questionText
	})
	none("a background session's turn ending on a question in prose")
	e.srv.onSessionActivity("bg", termux.Activity{State: termux.StateBusy, Since: 3})
	e.srv.onSessionActivity("bg", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 4})
	none("a background session's prompt")

	// Announced while it was in progress, and filed background before the
	// question was read.
	if err := e.store.SetSessionStatus("bg", store.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	e.gw.digestBlock()
	e.srv.onSessionActivity("bg", termux.Activity{State: termux.StateBusy, Since: 5})
	e.srv.onSessionActivity("bg", termux.Activity{State: termux.StateIdle, Unread: true, Since: 6})
	select {
	case one := <-sent:
		if one.note.Kind != pushFinished {
			t.Fatalf("the end of the turn = %+v", one.note)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the end of an in-progress session's turn was not pushed")
	}
	if err := e.store.SetSessionStatus("bg", store.StatusBackground); err != nil {
		t.Fatal(err)
	}
	e.gw.digestRelease()
	e.digestOf("bg", "the second question in prose", func(d digest) bool {
		return ready(d) && d.Question != nil && d.State == "idle"
	})
	none("a question read after the session was filed background")
}

// pushEnv is a pusher with one subscription and a channel of what it sent.
type pushEnv struct {
	t    *testing.T
	sent <-chan sentPush
}

func newPushEnv(t *testing.T, p *pusher) *pushEnv {
	t.Helper()
	sent := catch(p)
	if err := p.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	return &pushEnv{t: t, sent: sent}
}

func (e *pushEnv) next(what string, within time.Duration) sentPush {
	e.t.Helper()
	select {
	case one := <-e.sent:
		return one
	case <-time.After(within):
		e.t.Fatalf("no push was sent for %s", what)
	}
	return sentPush{}
}

func (e *pushEnv) none(what string, within time.Duration) {
	e.t.Helper()
	select {
	case one := <-e.sent:
		e.t.Fatalf("%s pushed %+v", what, one.note)
	case <-time.After(within):
	}
}

// When a question is news. A prompt opened from the prompt is, as one after
// work is; one that comes back after a tick of unknown is the same prompt and
// is told once; the next prompt after a moment of work is a new one. A first
// sighting, a prompt nobody typed at, a session filed background and a tool
// are never a question to push.
func TestPushQuestionRule(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	// Nothing reads these screens: every question goes out when the wait is
	// over, with the generic words.
	p.setWait(10 * time.Millisecond)
	pe := newPushEnv(t, p)
	for _, row := range []*store.Session{
		{ID: "q", Title: "Asks", Harness: config.HarnessClaude},
		{ID: "bg", Title: "Service", Harness: config.HarnessClaude},
		{ID: "sh", Title: "Shell", Harness: config.HarnessShell},
		{ID: "ide", Title: "Editor", Harness: config.HarnessVSCode},
	} {
		row.Workdir, row.TmuxName, row.State = t.TempDir(), "t-"+row.ID, store.StateRunning
		if err := env.store.CreateSession(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.store.SetSessionStatus("bg", store.StatusBackground); err != nil {
		t.Fatal(err)
	}
	at := func(s termux.State, unread bool, since int64) termux.Activity {
		return termux.Activity{State: s, Unread: unread, Since: since}
	}
	quiet := 150 * time.Millisecond

	// A first sighting after a restart, already on a prompt: no edge.
	p.observe("q", at(termux.StateWaiting, true, 1))
	pe.none("a prompt seen first", quiet)

	p.observe("q", at(termux.StateIdle, false, 2))
	p.observe("q", at(termux.StateWaiting, true, 3))
	if one := pe.next("a prompt opened from the prompt", 5*time.Second); one.note.Kind != pushQuestion ||
		one.note.Body != "Needs an answer" || one.note.ID != "q" || one.msg.Urgency != "high" {
		t.Fatalf("a prompt opened from the prompt = %+v %+v", one.note, one.msg)
	}

	// Out of sight for a tick and back: the same prompt.
	p.observe("q", at(termux.StateUnknown, true, 4))
	p.observe("q", at(termux.StateWaiting, true, 5))
	pe.none("a prompt that flickered", quiet)

	// Approved, a moment of work, and the next permission prompt: news.
	p.observe("q", at(termux.StateBusy, false, 6))
	p.observe("q", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 7, Note: "permission prompt"})
	if one := pe.next("the next permission prompt", 5*time.Second); one.note.Body != "Needs your permission" {
		t.Fatalf("the next permission prompt = %+v", one.note)
	}

	// From unknown it is the detector finding its footing.
	p.observe("q", at(termux.StateIdle, false, 8))
	p.observe("q", at(termux.StateUnknown, false, 9))
	p.observe("q", at(termux.StateWaiting, true, 10))
	pe.none("a prompt reached from unknown", quiet)

	// A harness's own menu at startup: nobody typed at it.
	p.observe("q", at(termux.StateBusy, false, 11))
	p.observe("q", at(termux.StateWaiting, false, 12))
	pe.none("a startup menu", quiet)

	for _, id := range []string{"bg", "sh", "ide"} {
		p.observe(id, at(termux.StateBusy, false, 1))
		p.observe(id, at(termux.StateWaiting, true, 2))
		pe.none(id+"'s prompt", quiet)
	}
}

// A question waits for its digest to have read it, and says what it asks.
// It carries the asking set at the moment it went out: this session, which
// is on a prompt, and nothing that is not.
func TestAQuestionIsPushedWithItsWords(t *testing.T) {
	e := newDigestEnv(t)
	// Longer than any read: a push that came without the words came from
	// the wait being over, which is not what is tested.
	e.srv.push.setWait(time.Minute)
	pe := newPushEnv(t, e.srv.push)
	e.session("w1", "claude", "Which database should we use?\n❯ 1. Postgres\n  2. SQLite\n  3. Type something.")
	e.session("w2", "codex", "Done.\n❯ \n")
	e.srv.onSessionActivity("w2", termux.Activity{State: termux.StateIdle, Since: 1})

	e.gw.digestAlways(askedJSON)
	e.srv.onSessionActivity("w1", termux.Activity{State: termux.StateIdle, Since: 1})
	e.srv.onSessionActivity("w1", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 2})
	one := pe.next("the question", 20*time.Second)
	if one.note.Body != "Which database should we use?" || one.note.Kind != pushQuestion || one.note.ID != "w1" ||
		one.note.Title != "Session w1" || one.msg.Topic != pushTopic("w1") {
		t.Fatalf("the question = %+v %+v", one.note, one.msg)
	}
	if one.note.Badge == nil || *one.note.Badge != 1 || len(one.note.Asking) != 1 || one.note.Asking[0] != "w1" {
		t.Fatalf("the question counts %v %v", one.note.Badge, one.note.Asking)
	}
	// The look at a waiting screen reads it again; it is the same question.
	e.srv.digests.lookAround(context.Background())
	pe.none("a question read again", 300*time.Millisecond)

	// Raw, the payload says the same: badge and asking are always there.
	var raw map[string]any
	if err := json.Unmarshal(one.msg.Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if at, _ := raw["at"].(float64); raw["badge"] != float64(1) || raw["kind"] != "question" || at <= 0 {
		t.Fatalf("payload = %s", one.msg.Payload)
	}
}

// Every count is stamped later than the one before it, however fast they
// come: a phone that gets the messages out of order keeps the icon and the
// tray as the latest count left them, and tells which that is by the stamp.
// A message whose set could not be counted carries no stamp, as it carries
// no set.
func TestPushCountsAreStampedInOrder(t *testing.T) {
	p := newPusher(nil)
	p.asking = func() []string { return []string{"a"} }
	var last int64
	for range 100 {
		var note pushNote
		if err := json.Unmarshal(p.payload(pushNote{Title: "t"}), &note); err != nil {
			t.Fatal(err)
		}
		if note.At <= last || note.Badge == nil || *note.Badge != 1 {
			t.Fatalf("stamp %d after %d (badge %v)", note.At, last, note.Badge)
		}
		last = note.At
	}
	p.asking = func() []string { return nil }
	var note pushNote
	if err := json.Unmarshal(p.payload(pushNote{Title: "t"}), &note); err != nil {
		t.Fatal(err)
	}
	if note.At != 0 || note.Badge != nil || note.Asking != nil {
		t.Fatalf("an uncounted message = %+v", note)
	}
}

// A question whose words cannot be had is pushed without them - at once
// when the read fails or there is no key to read with, and when the wait is
// over when the read is slow - and a question answered at the machine
// before anything went out is never pushed.
func TestAQuestionWithoutItsWords(t *testing.T) {
	e := newDigestEnv(t)
	pe := newPushEnv(t, e.srv.push)
	e.session("f1", "claude", "Do you want to proceed?\n❯ 1. Yes\n  2. No")
	ask := func(since int64, note string) {
		e.srv.onSessionActivity("f1", termux.Activity{State: termux.StateBusy, Since: since})
		e.srv.onSessionActivity("f1", termux.Activity{State: termux.StateWaiting, Unread: true, Since: since + 1, Note: note})
	}

	// A read that fails: the generic words, long before the wait is over.
	e.srv.push.setWait(time.Minute)
	e.gw.digestRefuse(http.StatusNotFound, "test/home-model is not a valid model ID")
	ask(1, "permission prompt")
	if one := pe.next("a question whose read failed", 20*time.Second); one.note.Body != "Needs your permission" ||
		one.note.Kind != pushQuestion {
		t.Fatalf("a question whose read failed = %+v", one.note)
	}

	// No key: the same, at once.
	e.configureOpenRouter(t, map[string]any{"api_key": ""})
	ask(10, "")
	if one := pe.next("a question with no key to read it", 20*time.Second); one.note.Body != "Needs an answer" {
		t.Fatalf("a question with no key = %+v", one.note)
	}

	// A read slower than the wait: the generic words when it is over, and
	// nothing more when the read comes in.
	e.configureOpenRouter(t, map[string]any{"api_key": "k"})
	e.gw.digestRefuse(0, "")
	e.gw.digestAlways(askedJSON)
	e.gw.digestBlock()
	e.srv.push.setWait(100 * time.Millisecond)
	ask(20, "")
	if one := pe.next("a question whose read was slow", 20*time.Second); one.note.Body != "Needs an answer" {
		t.Fatalf("a question whose read was slow = %+v", one.note)
	}
	e.gw.digestRelease()
	e.digestOf("f1", "the slow question", func(d digest) bool { return ready(d) && d.Question != nil })
	pe.none("the words of a question already pushed", 300*time.Millisecond)

	// Answered at the machine while it was being read: nothing at all.
	e.gw.digestBlock()
	e.srv.push.setWait(200 * time.Millisecond)
	ask(30, "")
	e.srv.onSessionActivity("f1", termux.Activity{State: termux.StateBusy, Since: 32})
	pe.none("a question answered before it was pushed", time.Second)
	e.gw.digestRelease()

	// Deleted while it was being read: nothing either, and no timer left.
	e.gw.digestBlock()
	ask(40, "")
	e.srv.push.forget("f1")
	pe.none("a question of a deleted session", time.Second)
	e.gw.digestRelease()
	e.srv.push.mu.Lock()
	left := len(e.srv.push.pending)
	e.srv.push.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d questions are still waiting", left)
	}
}

// A wait that runs out while the detector cannot say what the session is
// doing is held until it can: the prompt seen again is pushed then, and a
// session seen working instead is not pushed at all.
func TestAQuestionOverdueThroughUnknown(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	p.setWait(time.Minute)
	pe := newPushEnv(t, p)
	if err := env.store.CreateSession(&store.Session{ID: "u", Title: "Asks", Harness: config.HarnessCodex,
		Workdir: t.TempDir(), TmuxName: "t-u", State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	// runOut is the wait running out now, rather than in a minute.
	runOut := func() {
		t.Helper()
		p.mu.Lock()
		rec := p.pending["u"]
		if rec == nil {
			p.mu.Unlock()
			t.Fatal("no question is waiting")
		}
		rec.timer.Stop()
		p.mu.Unlock()
		p.expire("u", rec)
	}
	ask := func(since int64) {
		p.observe("u", termux.Activity{State: termux.StateBusy, Since: since})
		p.observe("u", termux.Activity{State: termux.StateWaiting, Unread: true, Since: since + 1})
		p.observe("u", termux.Activity{State: termux.StateUnknown, Unread: true, Since: since + 2})
		runOut()
	}

	ask(1)
	pe.none("a question whose session cannot be read", 200*time.Millisecond)
	p.observe("u", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 4})
	if one := pe.next("the prompt seen again", 5*time.Second); one.note.Kind != pushQuestion || one.note.ID != "u" {
		t.Fatalf("the prompt seen again = %+v", one.note)
	}

	ask(10)
	p.observe("u", termux.Activity{State: termux.StateBusy, Since: 13})
	p.observe("u", termux.Activity{State: termux.StateIdle, Since: 14})
	pe.none("a question answered while its session could not be read", 200*time.Millisecond)

	// The pane died: the program that asked is gone, and the menu the next
	// one opens on - relaunched, or resumed - is not its question.
	ask(20)
	p.observe("u", termux.Activity{State: termux.StateUnknown, Unread: true, Since: 23, Source: activitySourcePane})
	p.observe("u", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 24})
	pe.none("a question whose program ended", 200*time.Millisecond)
	p.mu.Lock()
	rec, asked := p.pending["u"], p.asked["u"]
	p.mu.Unlock()
	if rec != nil || asked {
		t.Fatalf("a question whose pane died is still held (%v) or asked (%v)", rec != nil, asked)
	}
	// Also when its wait has not run out yet: it never will.
	p.observe("u", termux.Activity{State: termux.StateBusy, Since: 30})
	p.observe("u", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 31})
	p.observe("u", termux.Activity{State: termux.StateUnknown, Unread: true, Since: 32, Source: activitySourcePane})
	p.mu.Lock()
	rec, asked = p.pending["u"], p.asked["u"]
	p.mu.Unlock()
	if rec != nil || asked {
		t.Fatalf("a held question whose pane died is still held (%v) or asked (%v)", rec != nil, asked)
	}
}

// The asking set is Home's Questions card (home.js cardsOf): live sessions
// that are not tools, not prepared and not done, on a prompt or idle with a
// question asked in prose. A session filed background is counted - it is
// only kept from notifying.
func TestAskingSetIsHomesQuestions(t *testing.T) {
	env := newEnv(t)
	d := env.srv.digests
	type one struct {
		id, harness, state, status string
		act                        termux.State
		prose, menu, silent        bool
		want                       bool
	}
	cases := []one{
		{id: "waiting", act: termux.StateWaiting, want: true},
		{id: "prose", harness: config.HarnessCodex, act: termux.StateIdle, prose: true, want: true},
		{id: "idle", act: termux.StateIdle},
		{id: "idle-menu", act: termux.StateIdle, menu: true},
		{id: "busy", act: termux.StateBusy},
		{id: "busy-prose", act: termux.StateBusy, prose: true},
		{id: "unknown", act: termux.StateUnknown},
		{id: "done", status: store.StatusDone, act: termux.StateWaiting},
		{id: "background", status: store.StatusBackground, act: termux.StateWaiting, want: true},
		{id: "review", status: store.StatusReview, act: termux.StateWaiting, want: true},
		{id: "shell", harness: config.HarnessShell, act: termux.StateWaiting},
		{id: "vscode", harness: config.HarnessVSCode, act: termux.StateWaiting},
		{id: "prepared", state: store.StatePrestart, act: termux.StateWaiting},
		{id: "exited", state: store.StateExited, act: termux.StateWaiting},
		{id: "starting", state: store.StateStarting, act: termux.StateWaiting, want: true},
		{id: "no-activity", silent: true},
	}
	var want []string
	for _, c := range cases {
		harness, state := c.harness, c.state
		if harness == "" {
			harness = config.HarnessClaude
		}
		if state == "" {
			state = store.StateRunning
		}
		if err := env.store.CreateSession(&store.Session{ID: c.id, Harness: harness, State: state,
			Workdir: t.TempDir(), TmuxName: "t-" + c.id}); err != nil {
			t.Fatal(err)
		}
		if c.status != "" {
			if err := env.store.SetSessionStatus(c.id, c.status); err != nil {
				t.Fatal(err)
			}
		}
		d.mu.Lock()
		if !c.silent {
			d.seen[c.id] = termux.Activity{State: c.act, Since: 1}
		}
		if c.prose || c.menu {
			source := questionText
			if c.menu {
				source = questionMenu
			}
			d.entries[c.id] = &digestEntry{published: true, d: digest{SessionID: c.id, Phase: digestReady,
				Question: &digestQuestion{Source: source, Kind: "text", Items: []digestItem{{Text: "Which?"}}}}}
		}
		d.mu.Unlock()
		if c.want {
			want = append(want, c.id)
		}
	}
	slices.Sort(want)
	if got := env.srv.askingSet(); !slices.Equal(got, want) {
		t.Fatalf("asking = %v, want %v", got, want)
	}
}

// A browser whose subscription was changed under it subscribes again and
// says which one it had: the old one is gone from the list in the same
// write. And with a tunnel address, that is the contact every push carries.
func TestPushSubscriptionReplacesTheOldOne(t *testing.T) {
	env := signedIn(t)
	env.srv.mu.Lock()
	env.srv.settings.Tunnel.Hostname = "socrates.example.org"
	env.srv.mu.Unlock()
	for _, e := range []string{"https://push.example.net/old", "https://push.example.net/other"} {
		body, _ := json.Marshal(map[string]any{"subscription": testSubscription(e)})
		if res, _ := env.do(t, env.client, "POST", "/api/push/subscribe", string(body)); res.StatusCode != http.StatusOK {
			t.Fatalf("subscribe = %d", res.StatusCode)
		}
	}
	body, _ := json.Marshal(map[string]any{"subscription": testSubscription("https://push.example.net/new"),
		"old_endpoint": "https://push.example.net/old"})
	if res, _ := env.do(t, env.client, "POST", "/api/push/subscribe", string(body)); res.StatusCode != http.StatusOK {
		t.Fatalf("resubscribe = %d", res.StatusCode)
	}
	var endpoints []string
	for _, sub := range env.srv.push.subs() {
		endpoints = append(endpoints, sub.Endpoint)
		if sub.Subject != "https://socrates.example.org" {
			t.Errorf("%s has subject %q", sub.Endpoint, sub.Subject)
		}
	}
	if !slices.Equal(endpoints, []string{"https://push.example.net/new", "https://push.example.net/other"}) {
		t.Fatalf("subscriptions = %v", endpoints)
	}
}

// The home screen icon is outside the sign-in, at the root, as iOS asks for
// it.
func TestAppleTouchIconIsPublic(t *testing.T) {
	env := newEnv(t)
	for _, path := range []string{"/apple-touch-icon.png", "/apple-touch-icon-precomposed.png"} {
		res, err := env.anon.Get(env.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("%s = %d %q", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}
