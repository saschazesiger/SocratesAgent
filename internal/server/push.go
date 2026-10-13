package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// Web Push: the notification that reaches a phone whose page is not open.
//
// The page's own notification (notify.js) needs the page to be running, and a
// phone in a pocket has suspended it. So the server sends the same news - a
// session finished, or started needing an answer - through the browser
// vendor's push service, and the service worker shows it. The rule for what
// counts as news is the page's rule exactly (notify.completed), applied here
// to the same committed activity changes the page hears.

const (
	pushKeysKey = "push.vapid"
	pushSubsKey = "push.subscriptions"
	// pushTTL is how long a push service keeps a message for a phone that is
	// off. A finished turn from yesterday is not news worth a buzz.
	pushTTL = 6 * time.Hour
	// pushSubsMax bounds the list: every browser that ever turned the switch
	// on and never turned it off is in it until its push service says gone.
	pushSubsMax = 32
)

// pushSub is one browser that asked to be told.
type pushSub struct {
	webpush.Subscription
	// Subject is the VAPID contact sent with every push to it: the origin
	// the browser subscribed from, which is a real https address when it came
	// through the tunnel.
	Subject string `json:"subject"`
	Added   int64  `json:"added"`
}

// pusher is the server's half of Web Push: the key, the subscriptions, and
// the memory of what each session was doing a moment ago.
type pusher struct {
	st *store.Store
	// send is the one way out, a field so that the tests can catch messages
	// without a push service.
	send func(ctx context.Context, sub pushSub, msg webpush.Message) error

	mu   sync.Mutex
	keys webpush.Keys
	prev map[string]termux.Activity
}

func newPusher(st *store.Store) *pusher {
	p := &pusher{st: st, prev: map[string]termux.Activity{}}
	p.send = p.deliver
	return p
}

// key answers the public key, making the pair the first time it is wanted.
func (p *pusher) key() (webpush.Keys, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keys.Public != "" {
		return p.keys, nil
	}
	var keys webpush.Keys
	err := p.st.GetJSON(pushKeysKey, &keys)
	if errors.Is(err, store.ErrNotFound) || (err == nil && keys.Public == "") {
		if keys, err = webpush.GenerateKeys(); err != nil {
			return webpush.Keys{}, err
		}
		err = p.st.SetJSON(pushKeysKey, keys)
	}
	if err != nil {
		return webpush.Keys{}, err
	}
	p.keys = keys
	return keys, nil
}

func (p *pusher) deliver(ctx context.Context, sub pushSub, msg webpush.Message) error {
	keys, err := p.key()
	if err != nil {
		return err
	}
	sender, err := webpush.NewSender(keys, sub.Subject)
	if err != nil {
		return err
	}
	return sender.Send(ctx, sub.Subscription, msg)
}

func (p *pusher) subs() []pushSub {
	var subs []pushSub
	if err := p.st.GetJSON(pushSubsKey, &subs); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("push: could not read the subscriptions: %v", err)
	}
	return subs
}

// edit changes the stored list under the lock, so a subscribe and a push
// that found a subscription gone cannot lose each other's write.
func (p *pusher) edit(change func([]pushSub) []pushSub) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.SetJSON(pushSubsKey, change(p.subs()))
}

func (p *pusher) add(sub pushSub) error {
	return p.edit(func(subs []pushSub) []pushSub {
		out := []pushSub{sub}
		for _, s := range subs {
			if s.Endpoint != sub.Endpoint {
				out = append(out, s)
			}
		}
		if len(out) > pushSubsMax {
			out = out[:pushSubsMax]
		}
		return out
	})
}

func (p *pusher) remove(endpoint string) error {
	return p.edit(func(subs []pushSub) []pushSub {
		out := subs[:0]
		for _, s := range subs {
			if s.Endpoint != endpoint {
				out = append(out, s)
			}
		}
		return out
	})
}

// pushNote is the payload the service worker reads.
type pushNote struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	ID    string `json:"id,omitempty"`
}

// news is notify.completed on the server: a session went from working to
// finished or to needing an answer, and the unread mark was set by this very
// change. It answers the line a notification says, or "" for no news.
func news(prev, next termux.Activity) string {
	if prev.State != termux.StateBusy && prev.State != termux.StateTasks {
		return ""
	}
	if !next.Unread || prev.Unread {
		return ""
	}
	switch next.State {
	case termux.StateIdle:
		return "Finished"
	case termux.StateWaiting:
		return "Needs an answer"
	}
	return ""
}

// observe is fed every committed activity change. The first sighting of a
// session only records it: with nothing before it, it cannot be an edge.
func (p *pusher) observe(id string, next termux.Activity) {
	p.mu.Lock()
	prev, seen := p.prev[id]
	p.prev[id] = next
	p.mu.Unlock()
	if !seen {
		return
	}
	body := news(prev, next)
	if body == "" {
		return
	}
	row, err := p.st.GetSession(id)
	if err != nil {
		return
	}
	// A shell and an editor take no turns, so they never finish one.
	if row.Harness == config.HarnessShell || row.Harness == config.HarnessVSCode {
		return
	}
	urgency := "normal"
	if next.State == termux.StateWaiting {
		urgency = "high"
	}
	payload, _ := json.Marshal(pushNote{Title: row.Title, Body: body, ID: id})
	sum := sha256.Sum256([]byte(id))
	msg := webpush.Message{
		Payload: payload, TTL: pushTTL, Urgency: urgency,
		// One waiting message per session: a phone that was off gets the last
		// word about it, not every one.
		Topic: hex.EncodeToString(sum[:16]),
	}
	go p.broadcast(msg)
}

func (p *pusher) broadcast(msg webpush.Message) {
	for _, sub := range p.subs() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := p.send(ctx, sub, msg)
		cancel()
		switch {
		case errors.Is(err, webpush.ErrGone):
			if err := p.remove(sub.Endpoint); err != nil {
				log.Printf("push: %v", err)
			}
		case err != nil:
			log.Printf("push to %s: %v", hostOf(sub.Endpoint), err)
		}
	}
}

// forget drops what was remembered about a deleted session.
func (p *pusher) forget(id string) {
	p.mu.Lock()
	delete(p.prev, id)
	p.mu.Unlock()
}

func hostOf(endpoint string) string {
	rest := strings.TrimPrefix(endpoint, "https://")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// pushSubject is the VAPID contact for a browser subscribing through r. The
// page's own https origin is a real address the push service can accept;
// anything else gets a mailto that is at least well formed.
func pushSubject(r *http.Request) string {
	if origin := r.Header.Get("Origin"); strings.HasPrefix(origin, "https://") {
		return origin
	}
	return "mailto:socrates@example.com"
}

/* ------------------------------------------------------------- handlers */

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	keys, err := s.push.key()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": keys.Public})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subscription webpush.Subscription `json:"subscription"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := body.Subscription.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub := pushSub{Subscription: body.Subscription, Subject: pushSubject(r), Added: time.Now().UnixMilli()}
	if err := s.push.add(sub); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.push.remove(body.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePushTest sends one push to one subscription and says how it went, so
// that turning notifications on is proved on the spot rather than at the end
// of the next turn.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var target *pushSub
	for _, sub := range s.push.subs() {
		if sub.Endpoint == body.Endpoint {
			target = &sub
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "this browser is not subscribed")
		return
	}
	payload, _ := json.Marshal(pushNote{Title: "Socrates", Body: "Notifications are on."})
	err := s.push.send(r.Context(), *target, webpush.Message{Payload: payload, TTL: time.Minute})
	if errors.Is(err, webpush.ErrGone) {
		_ = s.push.remove(target.Endpoint)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
