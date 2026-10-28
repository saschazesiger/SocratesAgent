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

	"github.com/saschazesiger/SocratesAgent/internal/agent"
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

// A question is every new question a chat somebody sent something is
// stopped on: after work, after background work, and asked from rest - but
// not one that came back after a tick of unknown, and not one of an agent's
// start-up, which nobody has asked anything.
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
		{ID: "s-agent", Title: "Claude Code · tests", Harness: "claude", Workdir: t.TempDir(), Kind: store.KindChat},
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

// What a tap on a notification opens is in the payload: a chat that needs
// an answer opens its question, anything else opens the chat. A chat with no
// journal to take words from is told in the generic ones.
func TestPushSaysWhatATapOpens(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	sent := catch(p)
	if err := p.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.CreateSession(&store.Session{ID: "s-ask", Title: "Asks", Harness: "codex",
		Workdir: t.TempDir(), Kind: store.KindChat}); err != nil {
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
	one := next()
	if one.note.Body != "Needs an answer" || one.note.Kind != pushQuestion || one.msg.Urgency != "high" {
		t.Fatalf("a question = %+v %+v", one.note, one.msg)
	}
	// Raw, the payload says the same: badge and asking are always there.
	var raw map[string]any
	if err := json.Unmarshal(one.msg.Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if at, _ := raw["at"].(float64); raw["kind"] != "question" || at <= 0 {
		t.Fatalf("payload = %s", one.msg.Payload)
	}

	p.observe("s-ask", termux.Activity{State: termux.StateBusy})
	p.observe("s-ask", termux.Activity{State: termux.StateIdle, Unread: true})
	if one := next(); one.note.Body != "Finished" || one.note.Kind != pushFinished {
		t.Fatalf("a finished turn = %+v", one.note)
	}
}

// A service filed background finishes turns so as not to interrupt anyone
// (DEVIATIONS.md: no chime, no notification): neither the end of its turn nor
// a question it asks reaches the phone.
func TestABackgroundSessionIsNotPushed(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	pe := newPushEnv(t, p)
	if err := env.store.CreateSession(&store.Session{ID: "bg", Title: "Service", Harness: config.HarnessClaude,
		Workdir: t.TempDir(), State: store.StateRunning, Kind: store.KindChat}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetSessionStatus("bg", store.StatusBackground); err != nil {
		t.Fatal(err)
	}
	p.observe("bg", termux.Activity{State: termux.StateIdle, Since: 1})
	p.observe("bg", termux.Activity{State: termux.StateBusy, Since: 2})
	p.observe("bg", termux.Activity{State: termux.StateIdle, Unread: true, Since: 3})
	pe.none("a background chat's turn ending", 300*time.Millisecond)
	p.observe("bg", termux.Activity{State: termux.StateBusy, Since: 4})
	p.observe("bg", termux.Activity{State: termux.StateWaiting, Unread: true, Since: 5})
	pe.none("a background chat's question", 300*time.Millisecond)
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

// When a question is news. One asked from rest is, as one after work is; one
// that comes back after a tick of unknown is the same question and is told
// once; the next question after a moment of work is a new one. A first
// sighting, a question nobody asked for, a chat filed background and a tool
// are never a question to push.
func TestPushQuestionRule(t *testing.T) {
	env := newEnv(t)
	p := env.srv.push
	pe := newPushEnv(t, p)
	for _, row := range []*store.Session{
		{ID: "q", Title: "Asks", Harness: config.HarnessClaude, Kind: store.KindChat},
		{ID: "bg", Title: "Service", Harness: config.HarnessClaude, Kind: store.KindChat},
		{ID: "sh", Title: "Shell", Harness: config.HarnessShell, TmuxName: "t-sh"},
		{ID: "ide", Title: "Editor", Harness: config.HarnessVSCode, TmuxName: "t-ide"},
	} {
		row.Workdir, row.State = t.TempDir(), store.StateRunning
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

	// A first sighting after a restart, already asking: no edge.
	p.observe("q", at(termux.StateWaiting, true, 1))
	pe.none("a question seen first", quiet)

	p.observe("q", at(termux.StateIdle, false, 2))
	p.observe("q", at(termux.StateWaiting, true, 3))
	if one := pe.next("a question asked from rest", 5*time.Second); one.note.Kind != pushQuestion ||
		one.note.Body != "Needs an answer" || one.note.ID != "q" || one.msg.Urgency != "high" {
		t.Fatalf("a question asked from rest = %+v %+v", one.note, one.msg)
	}

	// Out of sight for a tick and back: the same question.
	p.observe("q", at(termux.StateUnknown, true, 4))
	p.observe("q", at(termux.StateWaiting, true, 5))
	pe.none("a question that flickered", quiet)

	// Answered, a moment of work, and the next question: news.
	p.observe("q", at(termux.StateBusy, false, 6))
	p.observe("q", at(termux.StateWaiting, true, 7))
	if one := pe.next("the next question", 5*time.Second); one.note.Kind != pushQuestion {
		t.Fatalf("the next question = %+v", one.note)
	}

	// From unknown it is the detector finding its footing.
	p.observe("q", at(termux.StateIdle, false, 8))
	p.observe("q", at(termux.StateUnknown, false, 9))
	p.observe("q", at(termux.StateWaiting, true, 10))
	pe.none("a question reached from unknown", quiet)

	// An agent's own start-up: nobody asked it anything.
	p.observe("q", at(termux.StateBusy, false, 11))
	p.observe("q", at(termux.StateWaiting, false, 12))
	pe.none("a start-up question", quiet)

	for _, id := range []string{"bg", "sh", "ide"} {
		p.observe(id, at(termux.StateBusy, false, 1))
		p.observe(id, at(termux.StateWaiting, true, 2))
		pe.none(id+"'s question", quiet)
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

// The asking set is Home's Questions card (home.js cardsOf): live chats that
// are not prepared and not done, waiting on a question in their journal. A
// chat filed background is counted - it is only kept from notifying - and a
// terminal never asks.
func TestAskingSetIsHomesQuestions(t *testing.T) {
	env := newEnv(t)
	type one struct {
		id, harness, state, status string
		activity                   string
		want                       bool
	}
	cases := []one{
		{id: "waiting", activity: agent.ActivityWaiting, want: true},
		{id: "idle", activity: agent.ActivityIdle},
		{id: "busy", activity: agent.ActivityBusy},
		{id: "done", status: store.StatusDone, activity: agent.ActivityWaiting},
		{id: "background", status: store.StatusBackground, activity: agent.ActivityWaiting},
		{id: "review", status: store.StatusReview, activity: agent.ActivityWaiting, want: true},
		{id: "shell", harness: config.HarnessShell, activity: agent.ActivityWaiting},
		{id: "prepared", state: store.StatePrestart, activity: agent.ActivityWaiting},
		{id: "exited", state: store.StateExited, activity: agent.ActivityWaiting},
		{id: "starting", state: store.StateStarting, activity: agent.ActivityWaiting, want: true},
		{id: "no-brief"},
	}
	var want []string
	for _, c := range cases {
		harness, state, kind := c.harness, c.state, store.KindChat
		if harness == "" {
			harness = config.HarnessClaude
		}
		if harness == config.HarnessShell {
			kind = store.KindTerminal
		}
		if state == "" {
			state = store.StateRunning
		}
		if err := env.store.CreateSession(&store.Session{ID: c.id, Harness: harness, State: state,
			Workdir: t.TempDir(), Kind: kind}); err != nil {
			t.Fatal(err)
		}
		if c.status != "" {
			if err := env.store.SetSessionStatus(c.id, c.status); err != nil {
				t.Fatal(err)
			}
		}
		if c.activity != "" {
			env.srv.chats.mu.Lock()
			env.srv.chats.chats[c.id] = &chatPeer{id: c.id, hub: env.srv.chats,
				brief: chatBrief{Summary: agent.Summary{Activity: c.activity}}}
			env.srv.chats.mu.Unlock()
		}
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
