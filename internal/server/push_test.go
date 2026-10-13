package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// The rule is the page's (notify.completed): a turn that ended or started
// needing an answer, with an unread mark this very change set.
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
		{"needs you", act(termux.StateBusy, false), act(termux.StateWaiting, true), "Needs an answer"},
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
	if one.note.Title != "Claude Code · tests" || one.note.Body != "Finished" || one.note.ID != "s-agent" {
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

	res, _ = env.do(t, env.client, "POST", "/api/push/test", `{"endpoint":"https://push.example.net/a"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("test = %d", res.StatusCode)
	}
	if one := <-sent; one.note.Body != "Notifications are on." {
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
