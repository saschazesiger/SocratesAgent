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
// vendor's push service, and the service worker shows it. A finished turn is
// news by the page's rule exactly (notify.completed), applied here to the
// same committed activity changes the page hears. A question is news by a
// wider one (asks): every new prompt the person is stopped on, also one a
// session at rest opened, and it waits a moment for its own words (hold).
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
	// pushQuestionWait is the longest a question waits for the digest that
	// reads its words (pendingAsk). The read of a prompt is the first in the
	// queue and takes a few seconds; a model that is slower than this, or
	// down, costs the words, not the notification.
	pushQuestionWait = 8 * time.Second
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
	// wait is how long a question waits for its digest (pushQuestionWait),
	// a field so that the tests can run it in milliseconds.
	wait time.Duration
	// chatAsk and chatTurn are what a chat's journal says (chatHub): the
	// question it asks, and how its last turn ended - false for a turn that
	// is no news, one the person stopped themselves. A chat needs no digest
	// to wait for: its question has its words the moment it is asked.
	chatAsk  func(id string) string
	chatTurn func(id string) (string, bool)

	// counting is held while the asking set is counted and stamped, and
	// counted is the last stamp (stamp). It is a lock of its own, taken by
	// nothing that holds another: the count takes the digester's lock and
	// the detector's, which mu is taken under.
	counting sync.Mutex
	counted  int64

	mu   sync.Mutex
	keys webpush.Keys
	prev map[string]termux.Activity
	// finished is every session whose last push was "Finished", with the
	// Since of the idle it was sent for: a turn that ended by asking in prose
	// is announced again as a question once its digest has read it so.
	finished map[string]int64
	// asked is every session a question has been pushed for - or is on its
	// way for - since it was last seen working or idle. A prompt that drops
	// out of sight for a tick and comes back is the prompt the phone was
	// already told about, not a second question.
	asked map[string]bool
	// pending is every question waiting for its digest (hold).
	pending map[string]*pendingAsk
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

// pendingAsk is one question that has not been pushed yet. A notification
// that says "Needs an answer" makes the person open the app to find out what
// is asked; one that says "Run the migrations against production?" can be
// answered from where it is read, and the digest that reads the prompt for
// Home is seconds away. So the push waits for it, a little: until the digest
// of this very prompt is in, or has failed, or until wait is over.
type pendingAsk struct {
	title string
	// since is the waiting commit the question is for. A read made in any
	// other commit is a read of some other screen.
	since int64
	// note is the detector's word on the prompt ("permission prompt"), for
	// the generic words when there is nothing better.
	note  string
	timer *time.Timer
	// overdue is a wait that ran out while the detector could not say what
	// the session was doing: it is sent the moment the prompt is seen again,
	// and dropped if the session is seen anywhere else.
	overdue bool
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
		wait:        pushQuestionWait,
		outcomeWait: outcomeWait,
		prev:        map[string]termux.Activity{},
		finished:    map[string]int64{},
		asked:       map[string]bool{},
		pending:     map[string]*pendingAsk{},
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
// answer; "finished" and the scheduler's "started" open the session itself;
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
	pushStarted  = "started"
	pushTest     = "test"
)

// news is notify.completed's "Finished" on the server: a session went from
// working to finished, and the unread mark was set by this very change. It
// answers the line a notification says, or "" for no news. A session that
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

// asks is whether a committed change is a session starting to ask: a prompt
// reached from work, from background work or from the prompt - a question
// at the end of a turn, the next permission prompt after one was approved,
// a menu a command opened. Not from unknown: that is the detector finding its
// footing after a restart or a relaunch, or a prompt that dropped out of
// sight for a tick and came back. And only with the unread mark, which the
// detector sets only for a session somebody has typed at: a harness's own
// menu at startup ("Do you trust this folder?") is not news, it is what the
// person who started the session is looking at.
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
	if prev.State != next.State {
		// Whatever the turn's end asked, the session has moved on from it.
		delete(p.finished, id)
	}
	due, dueBody := p.followLocked(id, next)
	p.followFinishedLocked(id, next)
	ask := seen && asks(prev, next) && !p.asked[id] && p.pending[id] == nil
	p.mu.Unlock()
	if due != nil {
		p.notify(id, due.title, dueBody, pushQuestion)
	}
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
	// A shell and an editor take no turns, so they never finish one. And a
	// service filed background finishes turns so as not to interrupt anyone:
	// no chime and no notification (DEVIATIONS.md), as the page's own
	// notifier has it - also for a prompt it stops on, which Home still counts
	// as a question.
	if row.Harness == config.HarnessShell || row.Harness == config.HarnessVSCode ||
		row.Status == store.StatusBackground {
		return
	}
	if ask && row.IsChat() {
		p.askChat(id, row.Title, next)
		return
	}
	if ask {
		p.hold(id, row.Title, next)
		return
	}
	if row.IsChat() && p.chatTurn != nil {
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
	p.mu.Lock()
	p.finished[id] = next.Since
	p.mu.Unlock()
	p.notify(id, row.Title, body, pushFinished)
}

// askChat pushes the question a chat has just started asking, at once and in
// its own words: the ask in its journal is the question. Like hold, only for
// the commit it was found in, and once until the chat is seen working or
// idle again.
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
		body = questionBody(nil, at.Note)
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
	p.finished[id] = rec.since
	p.mu.Unlock()
	p.notify(id, rec.title, rec.body, pushFinished)
}

// judged is the digester's verdict on a turn's end. A "Finished" waiting for
// exactly this idle goes out now, saying how the turn left the work; a
// verdict that came after the wait was over has nothing left to say, as the
// turn's end was pushed already. The digester calls it with its own lock held
// (read).
func (p *pusher) judged(id string, o outcome, q *digestQuestion) {
	p.mu.Lock()
	rec := p.finishing[id]
	if rec == nil || rec.since != o.Since {
		p.mu.Unlock()
		return
	}
	p.dropFinishedLocked(id, rec)
	body, kind := outcomeBody(o, q)
	if kind == pushFinished {
		p.finished[id] = rec.since
	}
	p.mu.Unlock()
	p.notify(id, rec.title, body, kind)
}

// releaseFinished is the outcomes switched off: every turn's end that was
// waiting for one is pushed now.
func (p *pusher) releaseFinished() {
	p.mu.Lock()
	type due struct{ id, title, body string }
	var out []due
	for id, rec := range p.finishing {
		p.dropFinishedLocked(id, rec)
		p.finished[id] = rec.since
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

// outcomeBody is what the notification of a judged turn says, and what a tap
// on it opens. A turn that ends by asking is pushed as the question it asks,
// as a prompt's is; one that failed to be judged is "Finished".
func outcomeBody(o outcome, q *digestQuestion) (string, string) {
	if o.Phase != digestReady {
		return "Finished", pushFinished
	}
	with := func(label, rest string) string {
		if rest = strings.TrimSpace(rest); rest == "" {
			return label
		}
		return oneLine(label+" · "+rest, pushBodyRunes)
	}
	switch o.Kind {
	case outcomeComplete:
		return with("Complete", o.Reason), pushFinished
	case outcomePartial:
		if len(o.Todos) > 0 {
			return with("Partly done", fmt.Sprintf("%d open: %s", len(o.Todos), o.Todos[0])), pushFinished
		}
		return with("Partly done", o.Reason), pushFinished
	case outcomeBlocked:
		return with("Blocked", o.Reason), pushFinished
	case outcomeNeedsInput:
		if q != nil {
			return questionBody(q, ""), pushQuestion
		}
		return with("Waiting on you", o.Reason), pushFinished
	}
	return "Finished", pushFinished
}

// followLocked moves the questions along with a committed change. A session
// seen working or idle has moved on from whatever it asked: the next prompt
// is a new question, and a question still waiting for its digest was
// answered at the machine before the phone was told, so it is never sent.
// A session back on its prompt after a tick of unknown is on the prompt it
// was held for, in a new commit - which is the commit its digest now reads -
// and one whose wait ran out meanwhile is due now. But not a session whose
// pane is gone or dead: the program that asked has ended, and whatever
// prompt is seen next - the menu a relaunched one opens on, an update notice
// - is another program's screen, not this question. Called with the lock
// held; it answers the question that is due and what it says, for the caller
// to send once the lock is let go.
func (p *pusher) followLocked(id string, next termux.Activity) (*pendingAsk, string) {
	rec := p.pending[id]
	switch next.State {
	case termux.StateBusy, termux.StateTasks, termux.StateIdle:
		delete(p.asked, id)
		if rec != nil {
			p.dropLocked(id, rec)
		}
	case termux.StateUnknown:
		// The detector commits this source only for a pane tmux says is
		// absent or dead, never for one it merely could not read.
		if next.Source != activitySourcePane {
			break
		}
		delete(p.asked, id)
		if rec != nil {
			p.dropLocked(id, rec)
		}
	case termux.StateWaiting:
		if rec == nil {
			return nil, ""
		}
		rec.since, rec.note = next.Since, next.Note
		if rec.overdue {
			p.dropLocked(id, rec)
			return rec, questionBody(nil, rec.note)
		}
	}
	return nil, ""
}

// hold puts a question on the wait for its digest (pendingAsk). The row was
// read without the lock, so the commit it was read for has to still be the
// session's last one: a forget in between, or a change the detector has
// committed since, and there is nothing to hold.
func (p *pusher) hold(id, title string, at termux.Activity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now, ok := p.prev[id]
	if p.stopped || !ok || now.State != termux.StateWaiting || now.Since != at.Since || p.asked[id] || p.pending[id] != nil {
		return
	}
	rec := &pendingAsk{title: title, since: at.Since, note: at.Note}
	// The callback takes the lock this holds, so it cannot see the record
	// before the timer is in it.
	rec.timer = time.AfterFunc(p.wait, func() { p.expire(id, rec) })
	p.pending[id] = rec
	p.asked[id] = true
}

// expire is a question whose digest did not come in time: it is pushed with
// the generic words, which is better than a question nobody is told about.
// Unless the detector cannot say right now whether the session is still on
// it - then it goes the moment the prompt is seen again (followLocked).
func (p *pusher) expire(id string, rec *pendingAsk) {
	p.mu.Lock()
	if p.pending[id] != rec {
		// Sent, answered or forgotten while the timer was firing.
		p.mu.Unlock()
		return
	}
	rec.timer = nil
	if p.prev[id].State != termux.StateWaiting {
		rec.overdue = true
		p.mu.Unlock()
		return
	}
	p.dropLocked(id, rec)
	body := questionBody(nil, rec.note)
	p.mu.Unlock()
	p.notify(id, rec.title, body, pushQuestion)
}

// read is the digester's word on a session it has finished reading: in is
// the activity the screen was read in, now the session's last committed
// one, and q the question the read found - nil for a read that found none,
// that failed, or that could not be made for want of a key. A question
// waiting for exactly this read goes out now, with the question's own words
// when there are some and the generic ones when there are not.
//
// The digester calls it with its own lock held, so it takes nothing but the
// pusher's lock and sends from a goroutine of its own (notify): the order is
// digester.mu, then pusher.mu, and never the other way round.
func (p *pusher) read(id string, in, now termux.Activity, q *digestQuestion) {
	p.mu.Lock()
	rec := p.pending[id]
	// The read has to be of this very prompt: made in its commit, and the
	// session still in it. A read of the screen before the prompt, finished a
	// moment after it appeared, is the turn's summary, not its question.
	if rec == nil || in.State != termux.StateWaiting || now.State != termux.StateWaiting ||
		in.Since != rec.since || now.Since != rec.since {
		p.mu.Unlock()
		return
	}
	if q != nil && q.Source != questionMenu {
		q = nil
	}
	p.dropLocked(id, rec)
	body := questionBody(q, rec.note)
	p.mu.Unlock()
	p.notify(id, rec.title, body, pushQuestion)
}

// dropLocked takes a question off the wait. Called with the lock held.
func (p *pusher) dropLocked(id string, rec *pendingAsk) {
	if rec.timer != nil {
		rec.timer.Stop()
		rec.timer = nil
	}
	delete(p.pending, id)
}

// questionBody is what a notification about a question says: the question
// itself, on one line and short enough for a lock screen, or - with no words
// read - what kind of answer is wanted. note is the detector's word on the
// prompt.
func questionBody(q *digestQuestion, note string) string {
	if q != nil && len(q.Items) > 0 {
		if text := oneLine(q.Items[0].Text, pushBodyRunes); text != "" {
			return text
		}
	}
	if note == "permission prompt" || (q != nil && q.Kind == "confirm") {
		return "Needs your permission"
	}
	return "Needs an answer"
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

// askedInProse is a digest that read a question in prose at the end of a
// turn whose end was pushed as "Finished": Home counts it as a question, so
// the phone is told the same, on the same topic - the "Finished" is replaced -
// and a tap opens the question rather than the terminal. Only for the very
// idle that was announced, and only while nobody has looked at it: a person
// who read the session has seen the question. It says the question, as a
// prompt's push does. The digester calls it with its lock held (read).
func (p *pusher) askedInProse(id, title string, now termux.Activity, q *digestQuestion) {
	p.mu.Lock()
	since, ok := p.finished[id]
	if ok && now.State == termux.StateIdle && now.Since == since {
		delete(p.finished, id)
	} else {
		ok = false
	}
	p.mu.Unlock()
	if !ok || !now.Unread {
		return
	}
	// Filed background since its turn ended: it is not announced again, as a
	// question or as anything else.
	if row, err := p.st.GetSession(id); err != nil || row.Status == store.StatusBackground {
		return
	}
	p.notify(id, title, questionBody(q, ""), pushQuestion)
}

// payload is a note as it goes out, with the asking set as it stands now.
// It is counted here, on the goroutine that sends and with no lock held,
// rather than where the news was found: that is under the digester's lock or
// the pusher's, and the count takes both.
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

// forget drops what was remembered about a deleted session, and a question
// about it that has not gone out yet never will.
func (p *pusher) forget(id string) {
	p.mu.Lock()
	delete(p.prev, id)
	delete(p.finished, id)
	delete(p.asked, id)
	if rec := p.pending[id]; rec != nil {
		p.dropLocked(id, rec)
	}
	if rec := p.finishing[id]; rec != nil {
		p.dropFinishedLocked(id, rec)
	}
	p.mu.Unlock()
}

// stop drops every question still waiting for its words, and holds no more:
// the server is going, and a timer that fired after it would push from a
// store that is closed.
func (p *pusher) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	for id, rec := range p.pending {
		p.dropLocked(id, rec)
	}
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
