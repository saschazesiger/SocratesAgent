package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// Web Push: the notification that reaches a phone whose page is not open.
//
// The page's own notification (notify.js) needs the page to be running, and a
// phone in a pocket has suspended it. So the server sends the same news - a
// chat finished a turn, or started needing an answer - through the browser
// vendor's push service, and the service worker shows it. A finished turn is
// news by the page's rule exactly (notify.completed), applied here to the
// same committed activity changes the page hears, in the words of the chat's
// journal. A question is news by a wider one (asks): every new question the
// person is stopped on, in its own words, which the journal has the moment
// it is asked.
//
// Every message also carries how many sessions are asking, and which: the
// number on the app icon, and the questions still worth a notification.

const (
	pushKeysKey = "push.vapid"
	pushSubsKey = "push.subscriptions"
	// pushTTL is how long a push service keeps a message for a phone that is
	// off. A finished turn from yesterday is not news worth a buzz.
	pushTTL = 6 * time.Hour
	// pushSubsMax bounds the list: every browser that ever turned the switch
	// on and never turned it off is in it until its push service says gone.
	pushSubsMax = 32
	// pushBodyRunes bounds a question's words in a notification: about what
	// a lock screen shows before it cuts the rest off anyway.
	pushBodyRunes = 180
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
	// asking is the asking set (Server.askingSet), counted for every message
	// at the moment it goes out: the number on the app icon, and the
	// questions a phone keeps in its notification centre. Nil, or a set that
	// could not be counted, leaves both as the phone has them.
	asking func() []string
	// chatAsk and chatTurn are what a chat's journal says (chatHub): the
	// question it asks, and how its last turn ended - false for a turn that
	// is no news, one the person stopped themselves.
	chatAsk  func(id string) string
	chatTurn func(id string) (string, bool)

	// counting is held while the asking set is counted and stamped, and
	// counted is the last stamp (stamp). It is a lock of its own, taken by
	// nothing that holds another: the count takes the chats' locks, which mu
	// is taken under.
	counting sync.Mutex
	counted  int64

	mu   sync.Mutex
	keys webpush.Keys
	prev map[string]termux.Activity
	// asked is every chat a question has been pushed for since it was last
	// seen working or idle. A question that drops out of sight for a tick and
	// comes back is the question the phone was already told about, not a
	// second one.
	asked map[string]bool
	// checking says whether the outcome of the idle at since is still being
	// read (digester.outcomePending), and finishing is every "Finished"
	// waiting for it (holdFinished); outcomeWait is how long one waits, a
	// field so that the tests can run it in milliseconds.
	checking    func(id string, since int64) bool
	finishing   map[string]*pendingFinish
	outcomeWait time.Duration
	// stopped is a server that is going: nothing is put on the wait any more.
	stopped bool
}

// pendingFinish is one turn's end that has not been pushed yet, because its
// outcome is being read: a notification that says "Partly done · 2 open:
// tests" is one that does not need the app opened to find out how the work
// was left.
type pendingFinish struct {
	title string
	// body is what it says when no verdict comes: "Finished", or a chat's
	// own words for how its turn ended.
	body string
	// since is the idle the turn ended on, which the outcome is for.
	since int64
	timer *time.Timer
}

func newPusher(st *store.Store) *pusher {
	p := &pusher{
		st:          st,
		outcomeWait: outcomeWait,
		prev:        map[string]termux.Activity{},
		asked:       map[string]bool{},
		finishing:   map[string]*pendingFinish{},
	}
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
	return p.replace(sub, "")
}

// replace adds a subscription in place of an old one, in one write. A push
// service may change a browser's subscription on its own - a key rotated, an
// endpoint expired - and the worker subscribes again and says which one it
// had (pushsubscriptionchange): the old one is dead, and kept in the list it
// would only cost a failed send per message until its service said gone.
func (p *pusher) replace(sub pushSub, old string) error {
	return p.edit(func(subs []pushSub) []pushSub {
		out := []pushSub{sub}
		for _, s := range subs {
			if s.Endpoint != sub.Endpoint && (old == "" || s.Endpoint != old) {
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
//
// Kind is what a tap on it opens: "question" opens the question dialog of
// that session, which is the whole point of being told a session needs an
// answer; "finished" opens the session itself;
// the "test" a person asks for opens Home.
//
// Badge and Asking are the asking set when the message went out: the number
// the worker puts on the app icon, and the sessions whose question
// notifications are still worth keeping - a question answered at the machine
// or on another device is taken out of the notification centre by the next
// message that reaches the phone. They are left out of a message whose set
// could not be counted, and a worker that finds them missing leaves the
// badge and the notifications as they are.
//
// At is when they were counted, in milliseconds of the server's clock and
// never the same twice (pusher.stamp). Messages are sent from goroutines of
// their own, and a push service holds back a "Finished" (normal urgency) on a
// phone that dozes while it hands a question (high) over at once: they reach
// the phone in any order. A worker that has put a newer count on the icon
// shows an older message all the same, and leaves the icon and the tray as
// the newer one put them - an older set would take a question off them that
// started after it was counted.
type pushNote struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	ID     string   `json:"id,omitempty"`
	Kind   string   `json:"kind,omitempty"`
	Badge  *int     `json:"badge,omitempty"`
	Asking []string `json:"asking,omitzero"`
	At     int64    `json:"at,omitempty"`
}

// The kinds of a pushNote.
const (
	pushQuestion = "question"
	pushFinished = "finished"
	pushTest     = "test"
)

// news is notify.completed's "Finished" on the server: a chat went from
// working to finished, and the unread mark was set by this very change. It
// answers the line a notification says, or "" for no news. A chat that
// starts asking is asks.
func news(prev, next termux.Activity) string {
	if prev.State != termux.StateBusy && prev.State != termux.StateTasks {
		return ""
	}
	if !next.Unread || prev.Unread {
		return ""
	}
	if next.State == termux.StateIdle {
		return "Finished"
	}
	return ""
}

// asks is whether a committed change is a chat starting to ask: a question
// reached from work, from background work or from rest. Not from unknown:
// that is the detector finding its footing after a restart, or a question
// that dropped out of sight for a tick and came back. And only with the
// unread mark, which the detector sets only for a chat somebody has sent
// something: an agent's start-up is not news.
func asks(prev, next termux.Activity) bool {
	if next.State != termux.StateWaiting || !next.Unread {
		return false
	}
	switch prev.State {
	case termux.StateBusy, termux.StateTasks, termux.StateIdle:
		return true
	}
	return false
}

// observe is fed every committed activity change. The first sighting of a
// session only records it: with nothing before it, it cannot be an edge.
func (p *pusher) observe(id string, next termux.Activity) {
	p.mu.Lock()
	prev, seen := p.prev[id]
	p.prev[id] = next
	p.followLocked(id, next)
	p.followFinishedLocked(id, next)
	ask := seen && asks(prev, next) && !p.asked[id]
	p.mu.Unlock()
	if !seen {
		return
	}
	body := news(prev, next)
	if body == "" && !ask {
		return
	}
	row, err := p.st.GetSession(id)
	if err != nil {
		return
	}
	// Only a chat takes turns: a Shell and an editor never finish one. And a
	// service filed background finishes turns so as not to interrupt anyone:
	// no chime and no notification (DEVIATIONS.md), as the page's own
	// notifier has it - also for a question it stops on, which Home still
	// counts.
	if !row.IsChat() || row.Status == store.StatusBackground {
		return
	}
	if ask {
		p.askChat(id, row.Title, next)
		return
	}
	if p.chatTurn != nil {
		words, news := p.chatTurn(id)
		if !news {
			return
		}
		body = words
	}
	// A turn whose outcome is being read waits for it.
	if p.checking != nil && p.checking(id, next.Since) {
		p.holdFinished(id, row.Title, body, next)
		return
	}
	p.notify(id, row.Title, body, pushFinished)
}

// followLocked moves the questions along with a committed change: a chat
// seen working or idle has moved on from whatever it asked, so its next
// question is a new one. Called with the lock held.
func (p *pusher) followLocked(id string, next termux.Activity) {
	switch next.State {
	case termux.StateBusy, termux.StateTasks, termux.StateIdle:
		delete(p.asked, id)
	}
}

// askChat pushes the question a chat has just started asking, at once and in
// its own words: the ask in its journal is the question. Only for the commit
// it was found in, and once until the chat is seen working or idle again.
func (p *pusher) askChat(id, title string, at termux.Activity) {
	p.mu.Lock()
	now, ok := p.prev[id]
	if p.stopped || !ok || now.State != termux.StateWaiting || now.Since != at.Since || p.asked[id] {
		p.mu.Unlock()
		return
	}
	p.asked[id] = true
	p.mu.Unlock()
	body := ""
	if p.chatAsk != nil {
		body = oneLine(p.chatAsk(id), pushBodyRunes)
	}
	if body == "" {
		body = "Needs an answer"
	}
	p.notify(id, title, body, pushQuestion)
}

// followFinishedLocked moves a "Finished" that waits for its outcome along with
// a committed change. A session seen working, asking or doing background
// work has moved on from the turn: it was not its end after all, and is not
// announced. Nor is one somebody has read meanwhile - the person has seen how
// it ended. A tick of unknown keeps it, and the prompt it comes back to is the
// same idle in a new commit, which the outcome is carried to (outcomeEdge).
// Called with the lock held.
func (p *pusher) followFinishedLocked(id string, next termux.Activity) {
	rec := p.finishing[id]
	if rec == nil {
		return
	}
	switch {
	case next.State == termux.StateUnknown:
	case next.State != termux.StateIdle, !next.Unread:
		p.dropFinishedLocked(id, rec)
	default:
		rec.since = next.Since
	}
}

// holdFinished puts a turn's end on the wait for its outcome. The row was read
// without the lock, so the commit it was read for has to still be the
// session's last one.
func (p *pusher) holdFinished(id, title, body string, at termux.Activity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now, ok := p.prev[id]
	if p.stopped || !ok || now.State != termux.StateIdle || now.Since != at.Since || p.finishing[id] != nil {
		return
	}
	rec := &pendingFinish{title: title, body: body, since: at.Since}
	rec.timer = time.AfterFunc(p.outcomeWait, func() { p.expireFinished(id, rec) })
	p.finishing[id] = rec
}

// expireFinished is an outcome that did not come in time: the turn's end is
// pushed as it would have been without one.
func (p *pusher) expireFinished(id string, rec *pendingFinish) {
	p.mu.Lock()
	if p.finishing[id] != rec {
		p.mu.Unlock()
		return
	}
	p.dropFinishedLocked(id, rec)
	p.mu.Unlock()
	p.notify(id, rec.title, rec.body, pushFinished)
}

// judged is the digester's verdict on a turn's end. A "Finished" waiting for
// exactly this idle goes out now, saying how the turn left the work; a
// verdict that came after the wait was over has nothing left to say, as the
// turn's end was pushed already. The digester calls it with its own lock held
// (judgedLocked).
func (p *pusher) judged(id string, o outcome) {
	p.mu.Lock()
	rec := p.finishing[id]
	if rec == nil || rec.since != o.Since {
		p.mu.Unlock()
		return
	}
	p.dropFinishedLocked(id, rec)
	p.mu.Unlock()
	p.notify(id, rec.title, outcomeBody(o), pushFinished)
}

// releaseFinished is the outcomes switched off: every turn's end that was
// waiting for one is pushed now.
func (p *pusher) releaseFinished() {
	p.mu.Lock()
	type due struct{ id, title, body string }
	var out []due
	for id, rec := range p.finishing {
		p.dropFinishedLocked(id, rec)
		out = append(out, due{id, rec.title, rec.body})
	}
	p.mu.Unlock()
	for _, d := range out {
		p.notify(d.id, d.title, d.body, pushFinished)
	}
}

// dropFinishedLocked takes a turn's end off the wait. Called with the lock
// held.
func (p *pusher) dropFinishedLocked(id string, rec *pendingFinish) {
	if rec.timer != nil {
		rec.timer.Stop()
		rec.timer = nil
	}
	delete(p.finishing, id)
}

// outcomeBody is what the notification of a judged turn says; one that
// failed to be judged is "Finished".
func outcomeBody(o outcome) string {
	if o.Phase != digestReady {
		return "Finished"
	}
	with := func(label, rest string) string {
		if rest = strings.TrimSpace(rest); rest == "" {
			return label
		}
		return oneLine(label+" · "+rest, pushBodyRunes)
	}
	switch o.Kind {
	case outcomeComplete:
		return with("Complete", o.Reason)
	case outcomePartial:
		if len(o.Todos) > 0 {
			return with("Partly done", fmt.Sprintf("%d open: %s", len(o.Todos), o.Todos[0]))
		}
		return with("Partly done", o.Reason)
	case outcomeBlocked:
		return with("Blocked", o.Reason)
	case outcomeNeedsInput:
		return with("Waiting on you", o.Reason)
	}
	return "Finished"
}

// notify sends one session's news. Questions are high urgency: an answer is
// what the session is stopped on.
func (p *pusher) notify(id, title, body, kind string) {
	urgency := "normal"
	if kind == pushQuestion {
		urgency = "high"
	}
	if title == "" {
		title = "Socrates"
	}
	note := pushNote{Title: title, Body: body, ID: id, Kind: kind}
	// One waiting message per session: a phone that was off gets the last
	// word about it, not every one - and a "Finished" that turned out to be a
	// question is replaced by the question.
	msg := webpush.Message{TTL: pushTTL, Urgency: urgency, Topic: pushTopic(id)}
	go p.broadcast(note, msg)
}

// pushTopic is the Topic of every message about one session: a push service
// keeps one waiting message per topic and subscription, and replaces it with
// the next. It is a hash because a topic is at most 32 characters of the
// URL-safe alphabet.
func pushTopic(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}

// payload is a note as it goes out, with the asking set as it stands now.
// It is counted here, on the goroutine that sends and with no lock held,
// rather than where the news was found: that is under the digester's lock or
// the pusher's.
func (p *pusher) payload(note pushNote) []byte {
	if p.asking != nil {
		p.counting.Lock()
		if ids := p.asking(); ids != nil {
			n := len(ids)
			note.Badge, note.Asking, note.At = &n, ids, p.stampLocked()
		}
		p.counting.Unlock()
	}
	data, _ := json.Marshal(note)
	return data
}

// stampLocked is the At of a set just counted: the clock in milliseconds,
// moved on by one where it has not moved since the last count. Counts are
// made one at a time, so a later stamp is a later count - which is what a
// phone orders them by. The clock and not a counter, because the worker keeps
// the last stamp it applied across a restart of this server, and a counter
// that started again from one would have every message after it ignored.
// Called with counting held.
func (p *pusher) stampLocked() int64 {
	at := time.Now().UnixMilli()
	if at <= p.counted {
		at = p.counted + 1
	}
	p.counted = at
	return at
}

func (p *pusher) broadcast(note pushNote, msg webpush.Message) {
	subs := p.subs()
	if len(subs) == 0 {
		return
	}
	msg.Payload = p.payload(note)
	for _, sub := range subs {
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

// forget drops what was remembered about a deleted session, and a turn's
// end about it that has not gone out yet never will.
func (p *pusher) forget(id string) {
	p.mu.Lock()
	delete(p.prev, id)
	delete(p.asked, id)
	if rec := p.finishing[id]; rec != nil {
		p.dropFinishedLocked(id, rec)
	}
	p.mu.Unlock()
}

// stop drops every turn's end still waiting for its verdict, and holds no
// more: the server is going, and a timer that fired after it would push from
// a store that is closed.
func (p *pusher) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	for id, rec := range p.finishing {
		p.dropFinishedLocked(id, rec)
	}
}

func hostOf(endpoint string) string {
	rest := strings.TrimPrefix(endpoint, "https://")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// pushSubject is the VAPID contact for a browser subscribing through r. The
// tunnel's public address is the server's real address wherever the browser
// happened to come from - Apple's push service refuses a subject it does not
// take for a real contact, and a phone that subscribed over the local
// network would otherwise be given the mailto. Then the page's own https
// origin, which is a real address too; anything else gets a mailto that is at
// least well formed.
func (s *Server) pushSubject(r *http.Request) string {
	if public := s.Settings().Tunnel.PublicURL(); public != "" {
		return public
	}
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
		// OldEndpoint is the subscription this one replaces, from a service
		// worker that was told its subscription changed (replace).
		OldEndpoint string `json:"old_endpoint"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := body.Subscription.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub := pushSub{Subscription: body.Subscription, Subject: s.pushSubject(r), Added: time.Now().UnixMilli()}
	if err := s.push.replace(sub, strings.TrimSpace(body.OldEndpoint)); err != nil {
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
	// It carries the badge like every other message, so that turning
	// notifications on also puts the right number on the icon at once.
	payload := s.push.payload(pushNote{Title: "Socrates", Body: "Notifications are on.", Kind: pushTest})
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
