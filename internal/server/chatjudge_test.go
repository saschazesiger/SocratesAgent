//go:build linux

package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// waitOutcome waits for a session's verdict to be in a phase.
func (c *chatEnv) waitOutcome(t *testing.T, id, phase string) outcome {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if o, ok := c.srv.digests.outcomeOf(id); ok && o.Phase == phase {
			return o
		}
		if time.Now().After(deadline) {
			o, _ := c.srv.digests.outcomeOf(id)
			t.Fatalf("the verdict is %+v, want %s", o, phase)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A chat's ended turn is judged from its journal when outcomes are on: the
// turn as the chat recorded it goes to the home model, the verdict is the
// outcome Home, the sidebar and the push show, the push waits for it, and
// Read again judges it again.
func TestAChatsTurnIsJudgedFromItsJournal(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	c.configureOpenRouter(t, map[string]any{"outcomes": true})
	gw.always(`{"outcome":{"kind":"partial","reason":"The echo ran; the tests are not written.","todos":["Write the tests"]}}`)
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "judged", "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data, "title": "Judged"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["session"].(map[string]any)["id"].(string)
	c.send(t, id, "m1", "/tool")
	o := c.waitOutcome(t, id, digestReady)
	if o.Kind != outcomePartial || len(o.Todos) != 1 || o.Todos[0] != "Write the tests" ||
		o.Since != c.srv.manager.ActivityOf(id).Since {
		t.Fatalf("verdict %+v, activity %+v", o, c.srv.manager.ActivityOf(id))
	}
	calls := gw.seen()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].Messages[0].Content, chatJudgeMarker) ||
		!strings.Contains(calls[0].last(), "/tool") || !strings.Contains(calls[0].last(), "- Running echo hello") {
		t.Fatalf("the judge was asked %+v", calls)
	}
	push := pushOfKind(t, sent, pushFinished, 10*time.Second)
	if push.Body != "Partly done · 1 open: Write the tests" {
		t.Fatalf("the push %+v", push)
	}

	// Read again: the verdict is judged once more.
	gw.always(`{"outcome":{"kind":"complete","reason":"All done."}}`)
	res, _ := c.do(t, c.client, "POST", "/api/sessions/"+id+"/digest", "")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("read again: %d", res.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for o, _ := c.srv.digests.outcomeOf(id); o.Kind != outcomeComplete; o, _ = c.srv.digests.outcomeOf(id) {
		if time.Now().After(deadline) {
			t.Fatalf("judged again: %+v", o)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A new turn takes the verdict away.
	c.send(t, id, "m2", "/sleep 1")
	deadline = time.Now().Add(10 * time.Second)
	for _, ok := c.srv.digests.outcomeOf(id); ok && time.Now().Before(deadline); _, ok = c.srv.digests.outcomeOf(id) {
		time.Sleep(10 * time.Millisecond)
	}
	if o, ok := c.srv.digests.outcomeOf(id); ok && o.Phase != digestPending {
		t.Fatalf("a new turn kept the old verdict: %+v", o)
	}
}

// A judge that fails leaves the verdict in error and the push says how the
// turn ended in the chat's own words.
func TestAChatsJudgeThatFailsLetsThePushGo(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	c.configureOpenRouter(t, map[string]any{"outcomes": true})
	gw.refuse(http.StatusBadRequest, "no such model")
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "failing", "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data, "title": "Failing judge"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["session"].(map[string]any)["id"].(string)
	c.send(t, id, "m1", "hello")
	o := c.waitOutcome(t, id, digestError)
	if !strings.Contains(o.Error, "test/home-model") {
		t.Fatalf("verdict %+v", o)
	}
	if push := pushOfKind(t, sent, pushFinished, 10*time.Second); !strings.HasPrefix(push.Body, "Finished") {
		t.Fatalf("push %+v", push)
	}
}
