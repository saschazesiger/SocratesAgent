package server

// Digests: what a session is doing, and what it is asking.
//
// Home shows every coding session in one line, and a session that is asking
// the person something as the question itself, with its options as buttons.
// Both come from one model call per session that reads the screen - the same
// capture the Status button reads - and answers a summary and, when there is
// one, the question in a shape a dialog can draw. The calls are made on the
// server's own time, at the moments a session's answer changes (§1.3 of
// HOME.md lists them), so that Home is current when it is opened rather than
// once it has asked.
//
// A digest is a reading, not a record: it lives in memory, a restart forgets
// it, and the next trigger reads the screen again.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// What a digest reads, what it may cost and how often it is made again.
const (
	// digestScreenLines is a screen and the end of the last answer: a
	// question asked in prose is at the bottom of an answer that can be long.
	digestScreenLines = 150
	// digestMaxTokens is a ceiling, not a target. The answer is a short
	// object, but a model that thinks before it answers spends its thinking
	// out of the same budget.
	digestMaxTokens = 1500
	// digestTimeout bounds one digest once it has its call: the screen read
	// and the model asked. The wait for a free call is not in it - forty
	// sessions read after a restart are minutes of queue, and that is no
	// model that failed to answer.
	digestTimeout = 90 * time.Second
	// digestQueueWait bounds the wait for a free call.
	digestQueueWait = 5 * time.Minute
	// digestConcurrency is how many model calls may be in flight at once,
	// for every session together: a server with twenty sessions that all
	// finish in the same minute must not open twenty calls.
	digestConcurrency = 3
	// digestBulkConcurrency is how many of them may be summaries nobody is
	// waiting on - a hello filling in every session, a working session read
	// again for Home. The one left over is always there for a question: a
	// session that has just started asking is read next, not after forty
	// summaries.
	digestBulkConcurrency = digestConcurrency - 1
	// homePolledFor is how long a GET /api/home counts as somebody looking
	// at Home: a page whose stream cannot connect polls every five seconds.
	homePolledFor = 30 * time.Second

	// defaultDigestLookEvery is how often a waiting session's screen is
	// looked at for a prompt that moved on: a several-step question going to
	// its next step, or one answered in the terminal and followed by another.
	defaultDigestLookEvery = 4 * time.Second
	// defaultDigestFreshFor is how old a busy session's summary may get
	// before a changed screen is read again, while anybody is looking. It is
	// counted from the last attempt, not the last success: a model that keeps
	// failing must not be asked again on every look at a screen whose clock
	// ticks. It is also the longest a waiting screen's read waits after
	// failures in a row (each one doubles the wait from lookEvery).
	defaultDigestFreshFor = 60 * time.Second
	// defaultDigestManualGap is the least time between two reads a person
	// asked for ("Try again"), per session: a stuck client or a fast double
	// tap gets the read that is already on its way, not a cancelled call
	// and a new one each time.
	defaultDigestManualGap = 3 * time.Second
	// digestIdleRetries is how many failed reads in a row of an idle session
	// are tried again while nobody is looking at Home: enough for a gateway
	// that refused once to be asked again for the push a question in prose
	// sends, and few enough that one that is down costs nothing for long.
	// While Home is open it is tried again for as long as it fails.
	digestIdleRetries = 5

	// The sanitising limits: what a dialog on a car's screen can show.
	digestSummaryRunes     = 160
	digestContextRunes     = 600
	digestTextRunes        = 1000
	digestItemsMax         = 4
	digestOptionsMax       = 12
	digestLabelRunes       = 120
	digestDescriptionRunes = 300
	// digestAskInputBytes bounds the program's own record of a question when
	// it is handed to the model. A real one is a few hundred bytes.
	digestAskInputBytes = 8 << 10
	// digestTranscriptTail is how much of a Claude transcript is read for a
	// pending question. The question is the last thing the program wrote,
	// and a transcript can be tens of megabytes.
	digestTranscriptTail = 4 << 20
)

// The phases of a digest.
const (
	digestPending = "pending"
	digestReady   = "ready"
	digestError   = "error"
)

// The sources of a question: a prompt on the screen, or an answer that ends
// by asking something in prose.
const (
	questionMenu = "menu"
	questionText = "text"
)

// digestNoKey is the whole answer of a server with nobody to ask.
const digestNoKey = "no OpenRouter API key is configured - open /admin and add your key"

// errDigestJSON is a model that would not answer with an object twice.
var errDigestJSON = errors.New("the home model did not answer with usable JSON")

// digest is one session's reading, exactly as the browser receives it.
type digest struct {
	SessionID string `json:"session_id"`
	// Rev moves on every change of this object, so that an answer can say
	// which question it was given for.
	Rev      int64           `json:"rev"`
	Phase    string          `json:"phase"`
	Error    string          `json:"error"`
	State    string          `json:"state"`
	MadeAt   int64           `json:"made_at"`
	Model    string          `json:"model"`
	Summary  string          `json:"summary"`
	Context  string          `json:"context"`
	Question *digestQuestion `json:"question"`
}

type digestQuestion struct {
	Source string       `json:"source,omitempty"`
	Kind   string       `json:"kind"`
	Detail string       `json:"detail"`
	Items  []digestItem `json:"items"`
}

type digestItem struct {
	Header string `json:"header"`
	Text   string `json:"text"`
	// ScreenText is the item's question as the screen spells it, where Text
	// is in the person's language: it is what tells one tab of a menu from
	// another when they share their Yes and No (answerPolicy.itemsOf).
	ScreenText string         `json:"screen_text,omitempty"`
	Multi      bool           `json:"multi"`
	FreeText   bool           `json:"free_text"`
	Options    []digestOption `json:"options"`
}

type digestOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
}

// clone is a copy that shares nothing, so that what was published cannot be
// changed under a frame that is still being encoded.
func (d digest) clone() digest {
	if d.Question != nil {
		q := *d.Question
		q.Items = make([]digestItem, len(d.Question.Items))
		for i, item := range d.Question.Items {
			item.Options = append([]digestOption(nil), item.Options...)
			q.Items[i] = item
		}
		d.Question = &q
	}
	return d
}

// digestEntry is what the digester keeps about one session.
type digestEntry struct {
	// d is the digest as it was last published; published is whether there
	// has been one at all.
	d         digest
	published bool
	// gen is the trigger this session is on. A newer trigger cancels the
	// call of an older one, and an older call that answers anyway is thrown
	// away.
	gen    uint64
	cancel context.CancelFunc
	// hash is the visible screen the current digest was made from, and look
	// what the last look at a waiting session saw.
	hash string
	look string
	// tried is when the last call was started, fails how many in a row have
	// failed, and manual when a person last asked for one.
	tried  time.Time
	fails  int
	manual time.Time
	// again is when an answer to a screen that had moved on last started a
	// read (readAgain).
	again time.Time
	// dropped is a question taken out of the digest because the session left
	// the state it was asked in, and no read since: the summary still says
	// what it asked.
	dropped bool
}

// digester owns the digests and the calls that make them.
type digester struct {
	srv *Server
	sem chan struct{}
	// bulk is what a summary nobody is waiting on takes before it takes a
	// call (digestBulkConcurrency).
	bulk chan struct{}

	mu      sync.Mutex
	entries map[string]*digestEntry
	// seen is the last committed activity per session, which is where the
	// edges that trigger a digest are found: the callback carries the new
	// state and not the old.
	seen map[string]termux.Activity
	// gone is every session that was forgotten. A read of the row that
	// happened before the delete must not bring its digest back afterwards,
	// and ids are never reused, so the set only ever holds the dead.
	gone map[string]bool
	// later is every session a hello wanted read while the detector had no
	// state for it yet: observe reads it on the first one.
	later map[string]bool
	// outcomes is the verdict on every session's last turn (outcome.go);
	// oseen is the last committed activity outcomeEdge was given, and oknown
	// the last one that was not unknown, which is how a verdict is carried
	// across a tick of unknown back onto the same prompt.
	outcomes map[string]*outcome
	oseen    map[string]termux.Activity
	oknown   map[string]termux.Activity
	// epoch is where every digest's rev starts: the clock in microseconds
	// when this process started, so that a rev from before a restart is never
	// the rev of a different question after it. It stays below 2^53, the
	// largest whole number a browser reads exactly.
	epoch int64

	// capture and visible read the screen. They are fields so that a test
	// can hand the digester a screen without a pane behind it.
	capture func(ctx context.Context, id string, lines int) (string, error)
	visible func(ctx context.Context, id string) (string, error)
	// styled is visible with its colours, which is where a highlight shown
	// in colour only is (litOption).
	styled func(ctx context.Context, id string) (string, error)
	// lookEvery and freshFor are triggers 3 and 4's clocks, and manualGap the
	// least time between two reads a person asked for; fields so that a test
	// can run them in milliseconds.
	lookEvery time.Duration
	freshFor  time.Duration
	manualGap time.Duration
	// polled is when a page last asked for GET /api/home, and filled when
	// that last read the sessions nobody has read yet: a page that can only
	// poll is somebody looking at Home as much as one with a stream.
	polled time.Time
	filled time.Time
}

func newDigester(s *Server) *digester {
	d := &digester{
		srv:       s,
		sem:       make(chan struct{}, digestConcurrency),
		bulk:      make(chan struct{}, digestBulkConcurrency),
		entries:   map[string]*digestEntry{},
		seen:      map[string]termux.Activity{},
		gone:      map[string]bool{},
		later:     map[string]bool{},
		outcomes:  map[string]*outcome{},
		oseen:     map[string]termux.Activity{},
		oknown:    map[string]termux.Activity{},
		epoch:     time.Now().UnixMicro(),
		lookEvery: defaultDigestLookEvery,
		freshFor:  defaultDigestFreshFor,
		manualGap: defaultDigestManualGap,
	}
	d.capture = func(ctx context.Context, id string, lines int) (string, error) {
		return s.manager.CapturePane(ctx, id, lines)
	}
	d.visible = func(ctx context.Context, id string) (string, error) {
		return s.manager.VisiblePane(ctx, id)
	}
	d.styled = func(ctx context.Context, id string) (string, error) {
		return s.manager.StyledPane(ctx, id)
	}
	d.loadOutcomes()
	return d
}

// codingHarness is whether a session is one that takes turns and asks
// questions. A shell and an editor are tools: they have no activity worth a
// summary and nothing to ask.
func codingHarness(id string) bool {
	switch id {
	case config.HarnessClaude, config.HarnessCodex, config.HarnessOpenCode:
		return true
	}
	return false
}

// digestible is whether a session is digested at all. One that is not keeps
// its last digest as it is: the last summary of a finished session is still
// the best line Home has for it.
func digestible(row *store.Session) bool {
	return !row.IsChat() && codingHarness(row.Harness) && row.State == store.StateRunning
}

// get is one session's digest, and false when it has none.
func (d *digester) get(id string) (digest, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.entries[id]
	if e == nil || !e.published {
		return digest{}, false
	}
	return e.d.clone(), true
}

// screenOf is the hash of the visible screen a session's digest was read
// from, and false when it has none.
func (d *digester) screenOf(id string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.entries[id]
	if e == nil || !e.published {
		return "", false
	}
	return e.hash, true
}

// all is every digest there is, for a hello.
func (d *digester) all() map[string]digest {
	out := map[string]digest{}
	if d == nil {
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, e := range d.entries {
		if e.published {
			out[id] = e.d.clone()
		}
	}
	return out
}

// publishLocked tells every event stream about a changed digest. It is called
// with the lock held, so that two changes reach every stream in the order
// they were made; publish never blocks, so holding it costs nothing.
func (d *digester) publishLocked(id string, e *digestEntry) {
	e.d.Rev++
	e.published = true
	d.srv.events.publish(map[string]any{"t": "digest", "id": id, "digest": e.d.clone()})
}

// stateOf is a session's last committed activity: the one the detector
// reported, or, for a session it has not reported since the server started,
// the one it would answer now.
func (d *digester) stateOf(id string) termux.Activity {
	d.mu.Lock()
	a, ok := d.seen[id]
	d.mu.Unlock()
	if ok {
		return a
	}
	return d.srv.manager.ActivityOf(id)
}

// observe takes every committed activity change. It returns at once: it is
// called from the detector's tick.
//
// Staleness is cleared here, before anything else can happen: a question
// belongs to the state it was read in, and the moment the session has left
// that state the question is gone from the digest and every browser is told.
// A client never has to work out for itself whether the question it holds is
// still being asked.
func (d *digester) observe(id string, a termux.Activity) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.gone[id] {
		d.mu.Unlock()
		return
	}
	prev, had := d.seen[id]
	d.seen[id] = a
	e := d.entries[id]
	dropped := false
	if e != nil && e.published && e.d.Question != nil && e.d.State != string(a.State) {
		e.d.Question = nil
		e.dropped, dropped = true, true
		d.publishLocked(id, e)
	}
	// Back from unknown - a relaunch, a restart, a resume, a crash, a pane
	// that could not be read for a few ticks - to a state the digest was not
	// made in, or with the question it held dropped on the way: no edge
	// below is one (none starts from unknown), and the summary would say
	// "Asks which database to use" of a session at its prompt until its next
	// turn.
	back := had && prev.State == termux.StateUnknown && a.State != termux.StateUnknown &&
		e != nil && e.published && (e.d.State != string(a.State) || e.dropped)
	// A reading made while the detector had no answer yet - a hello straight
	// after a restart, a read asked for in the first second - read the screen
	// as a session that asks nothing, whatever it shows: a question asked in
	// prose is only a question while idle. It is read again the moment there
	// is a state to read it in, and so is one fillMissing put off for it.
	unread := a.State != termux.StateUnknown &&
		(d.later[id] || (e != nil && e.published && e.d.Phase != digestPending && e.d.State == string(termux.StateUnknown)))
	delete(d.later, id)
	d.mu.Unlock()
	if had && prev.State == a.State {
		if unread {
			d.trigger(id, false)
		}
		return
	}
	switch {
	case unread:
		// A summary a hello wanted, or one read in no state: it is read with
		// the others, ahead of them only when it is asking.
		d.trigger(id, false)
	case a.State == termux.StateWaiting:
		// 1: a session that has just started asking.
		d.trigger(id, true)
	case a.State == termux.StateIdle && (prev.State == termux.StateBusy || prev.State == termux.StateTasks):
		// 2: a turn that has just ended - its summary, and a question in
		// prose if the answer ends with one.
		d.trigger(id, true)
	case had && prev.State == termux.StateWaiting && a.State != termux.StateUnknown:
		// And a session that has just stopped asking - answered, declined
		// with Esc, a permission refused. Its summary says what it asked,
		// and an idle session reached straight from waiting would otherwise
		// say so until its next turn: no other trigger looks at it.
		d.trigger(id, true)
	case dropped && a.State != termux.StateUnknown && !d.srv.answers.live(id):
		// A question asked in prose, answered at the machine: the turn it
		// started would be under a summary that says "Asks which colour …"
		// for a minute (trigger 5 waits that long), so it is read with the
		// others. An answer run reads it when it ends.
		d.trigger(id, false)
	case back:
		d.trigger(id, false)
	}
}

// trigger reads the row on a goroutine of its own and digests the session if
// it is one to digest. The detector's tick is not made to wait for a read of
// the store. urgent is a read that something just happened to ask for, which
// goes ahead of the summaries (requestBulk).
func (d *digester) trigger(id string, urgent bool) {
	go func() {
		row, err := d.srv.store.GetSession(id)
		if err != nil || !digestible(row) {
			return
		}
		d.requestAs(row, urgent)
	}()
}

// request starts a digest of one session now and answers it as it stands: a
// pending one, or the error of a server with no key. A call already in flight
// for the session is cancelled - the newest reason to look is the one that
// counts.
func (d *digester) request(row *store.Session) digest {
	return d.requestAs(row, true)
}

// requestBulk is request for a summary nobody is waiting on: it queues behind
// the reads of questions, and is one of them only for a session that is
// asking.
func (d *digester) requestBulk(row *store.Session) digest {
	return d.requestAs(row, false)
}

func (d *digester) requestAs(row *store.Session, urgent bool) digest {
	in := d.stateOf(row.ID)
	state := in.State
	urgent = urgent || state == termux.StateWaiting
	settings := d.srv.Settings()
	model := strings.TrimSpace(settings.OpenRouter.HomeModel)
	if model == "" {
		model = config.DefaultHomeModel
	}
	key := strings.TrimSpace(settings.OpenRouter.APIKey)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone[row.ID] {
		// The row was read before the session was deleted.
		return digest{SessionID: row.ID}
	}
	e := d.entries[row.ID]
	if e == nil {
		e = &digestEntry{d: digest{SessionID: row.ID, State: string(termux.StateUnknown), Rev: d.epoch}}
		d.entries[row.ID] = e
	}
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.gen++
	if key == "" {
		// Nothing to ask, so nothing is asked. The digest says so once: a
		// waiting session looked at every four seconds is not news every
		// four seconds. It says what the session is doing, as a failed read
		// does (fail), and so it is said again when that changes.
		if !e.published || e.d.Phase != digestError || e.d.Error != digestNoKey || e.d.State != string(state) {
			e.d.Phase, e.d.Error, e.d.Model, e.d.State = digestError, digestNoKey, model, string(state)
			d.publishLocked(row.ID, e)
		}
		// A question waiting for its words will not get any: it goes out
		// now with the generic ones - on every request, not only the one
		// that published, since the sentence was said long before this
		// prompt appeared.
		if now, ok := d.seen[row.ID]; ok {
			d.srv.push.read(row.ID, in, now, nil)
		}
		d.judgedLocked(row.ID, in, nil, nil, model, digestNoKey)
		return e.d.clone()
	}
	// The previous summary, context and question stay while it is pending,
	// so Home does not blink; the question of a state the session has left
	// was cleared when it left it.
	e.d.Phase, e.d.Error, e.d.Model = digestPending, "", model
	d.publishLocked(row.ID, e)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.tried = time.Now()
	go d.run(ctx, cancel, e, e.gen, *row, settings, model, urgent)
	return e.d.clone()
}

func (d *digester) setManualGap(gap time.Duration) {
	d.mu.Lock()
	d.manualGap = gap
	d.mu.Unlock()
}

// requestNow is request for a person who asked: the dialog's "Read again" and
// "Try again". A read that is already on its way is the answer to asking
// again, and so is one that was asked for a moment ago: each new call would
// cancel one that may already be paid for.
func (d *digester) requestNow(row *store.Session) digest {
	d.mu.Lock()
	if e := d.entries[row.ID]; e != nil && e.published &&
		(e.cancel != nil || (!e.manual.IsZero() && time.Since(e.manual) < d.manualGap)) {
		out := e.d.clone()
		d.mu.Unlock()
		return out
	}
	d.rejudgeLocked(row.ID, d.seen[row.ID])
	d.mu.Unlock()
	out := d.request(row)
	d.mu.Lock()
	if e := d.entries[row.ID]; e != nil {
		e.manual = time.Now()
	}
	d.mu.Unlock()
	return out
}

// readAgain is request for an answer that found the screen moved on from the
// one its question was read from. A read already on its way is the read of
// the moved screen, and so is one that an answer like this started a moment
// ago: every tap on Send of a screen that keeps moving would otherwise cancel
// a call that may already be paid for, and start another. A read that
// finished before the screen moved - the question's own - is no read of it.
func (d *digester) readAgain(row *store.Session) digest {
	d.mu.Lock()
	if e := d.entries[row.ID]; e != nil && e.published &&
		(e.cancel != nil || (!e.again.IsZero() && time.Since(e.again) < d.manualGap)) {
		out := e.d.clone()
		d.mu.Unlock()
		return out
	}
	d.mu.Unlock()
	out := d.request(row)
	d.mu.Lock()
	if e := d.entries[row.ID]; e != nil {
		e.again = time.Now()
	}
	d.mu.Unlock()
	return out
}

// answered is an operator run that typed into a session coming to an end.
// The question its digest holds is the one that was answered, or the one being
// answered when the run stopped: it is taken out at once, so that a second
// device still showing it is told the question has changed rather than
// answering the next prompt with it - a permission prompt's Yes and No are the
// same words every time. And the screen is read again for what came after,
// unless a read that started after the keys is on its way already.
func (d *digester) answered(id string, since time.Time) {
	if d == nil {
		return
	}
	d.mu.Lock()
	e := d.entries[id]
	if e != nil && e.published && e.d.Question != nil {
		e.d.Question = nil
		d.publishLocked(id, e)
	}
	// A read that started before the keys read the question that was just
	// answered, and is replaced: request cancels it.
	reading := e != nil && e.cancel != nil && !e.tried.Before(since)
	d.mu.Unlock()
	if reading {
		return
	}
	row, err := d.srv.store.GetSession(id)
	if err != nil || !digestible(row) {
		return
	}
	d.request(row)
}

// reread reads a session's screen again, on the server's own time, if it is
// one to read and nothing is reading it already.
func (d *digester) reread(id string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	e := d.entries[id]
	reading := e != nil && e.cancel != nil
	d.mu.Unlock()
	if reading {
		return
	}
	row, err := d.srv.store.GetSession(id)
	if err != nil || !digestible(row) {
		return
	}
	d.request(row)
}

// forget drops a deleted session's digest, stops its call and tells every
// stream that it is gone.
func (d *digester) forget(id string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gone[id] = true
	e := d.entries[id]
	delete(d.seen, id)
	delete(d.oseen, id)
	delete(d.oknown, id)
	d.dropOutcomeLocked(id)
	if e == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	delete(d.entries, id)
	if e.published {
		d.srv.events.publish(map[string]any{"t": "digest", "id": id, "digest": nil})
	}
}

// polledHome is a GET /api/home: a page that cannot hold a stream is
// somebody looking at Home as well, and is what the looks keep summaries
// current for. It reads the sessions nobody has read yet as a hello does, at
// most once a look: a page that polls asks every five seconds, and the read
// is of the sessions that have no digest, which are few.
func (d *digester) polledHome() {
	if d == nil {
		return
	}
	d.mu.Lock()
	now := time.Now()
	d.polled = now
	fill := d.filled.IsZero() || now.Sub(d.filled) >= d.lookEvery
	if fill {
		d.filled = now
	}
	d.mu.Unlock()
	if fill {
		go d.fillMissing()
	}
}

// polledSince is whether a page polled Home at or after t.
func (d *digester) polledSince(t time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.polled.IsZero() && !d.polled.Before(t)
}

// fillMissing queues every running coding session that has no digest at all.
// It is what a hello does: a page that has just connected wants a line for
// every session it is about to draw - and what a reload does about a line
// that is wrong: a read that failed, once its wait is over, is no line either.
func (d *digester) fillMissing() {
	d.fill(false)
}

// settingsChanged is a new key, gateway or home model saved: every session
// with nothing but an error to show is read again at once, whatever its wait
// - the error may well have been the settings' - and the pages that are open
// see the readings arrive without reconnecting.
func (d *digester) settingsChanged() {
	d.fill(true)
}

// fill is fillMissing; failed counts every failed read as missing, its wait
// over or not.
func (d *digester) fill(failed bool) {
	rows, err := d.srv.store.ListSessions()
	if err != nil {
		return
	}
	key := strings.TrimSpace(d.srv.Settings().OpenRouter.APIKey) != ""
	now := time.Now()
	for i := range rows {
		row := &rows[i]
		if !digestible(row) {
			continue
		}
		unknown := d.stateOf(row.ID).State == termux.StateUnknown
		d.mu.Lock()
		e := d.entries[row.ID]
		// A digest that could only say "no key" is no digest once there is
		// one, and nor is a failed read whose wait is over.
		missing := e == nil || (key && e.d.Phase == digestError &&
			(e.d.Error == digestNoKey || d.failedRead(e, now) || failed && e.cancel == nil))
		// A session the detector has not answered for yet - every session in
		// the first seconds after a restart, which is when a page that was
		// open reconnects - is read once it has: read now, its screen would
		// be read in no state, and a question asked in prose is no question
		// there. observe reads it on its first state.
		// The detector may have answered since it was asked: observe has
		// then been, and will not come back for it.
		if a, ok := d.seen[row.ID]; ok {
			unknown = a.State == termux.StateUnknown
		}
		later := missing && unknown
		if later {
			d.later[row.ID] = true
		}
		d.mu.Unlock()
		if missing && !later {
			d.requestBulk(row)
		}
	}
}

/* ------------------------------------------------------------ the call */

// slot waits for a free call, bounded by digestQueueWait, and says whether it
// has one. A summary nobody waits on takes one of the bulk places first, so
// that it never holds the last call a question would take.
func (d *digester) slot(ctx context.Context, urgent bool) bool {
	wait := time.NewTimer(digestQueueWait)
	defer wait.Stop()
	if !urgent {
		select {
		case d.bulk <- struct{}{}:
		case <-ctx.Done():
			return false
		case <-wait.C:
			return false
		}
	}
	select {
	case d.sem <- struct{}{}:
		return true
	case <-ctx.Done():
	case <-wait.C:
	}
	if !urgent {
		<-d.bulk
	}
	return false
}

func (d *digester) release(urgent bool) {
	<-d.sem
	if !urgent {
		<-d.bulk
	}
}

// run is one digest: read the screen, ask, sanitise, publish. Every ending
// but a cancellation is published; a cancelled run was replaced or its
// session deleted, and either way somebody else has the last word.
func (d *digester) run(ctx context.Context, cancel context.CancelFunc, e *digestEntry, gen uint64,
	row store.Session, settings config.Settings, model string, urgent bool) {
	defer cancel()
	if !d.slot(ctx, urgent) {
		if ctx.Err() == nil {
			d.fail(e, gen, "", d.stateOf(row.ID), "the screen was not read in time: too many sessions were being read at once")
		}
		return
	}
	defer d.release(urgent)
	// The call's own clock starts with the call.
	ctx, stop := context.WithTimeout(ctx, digestTimeout)
	defer stop()

	activity := d.stateOf(row.ID)
	// A read of a turn's end that is waiting for its verdict says it too
	// (outcome.go).
	d.mu.Lock()
	judge := settings.OpenRouter.Outcomes && d.wantsOutcomeLocked(row.ID, activity)
	d.mu.Unlock()
	hash, visible := "", ""
	if v, err := d.visible(ctx, row.ID); err == nil {
		visible, hash = v, screenHash(v)
	}
	screen, err := d.capture(ctx, row.ID, digestScreenLines)
	if err != nil {
		d.fail(e, gen, hash, activity, "the screen could not be read: "+err.Error())
		return
	}
	// Claude's own record of the question it asks, for a question that is on
	// the screen: a session killed, crashed or resumed while it asked leaves
	// the record with no answer in its transcript for good, and handed to the
	// model as the authority on what is asked it would make a question of an
	// empty prompt, or of the next permission prompt.
	var ask json.RawMessage
	if row.Harness == config.HarnessClaude && activity.State == termux.StateWaiting {
		shown := visible
		if shown == "" {
			shown = screen
		}
		if menuOpen(shown) {
			ask = claudePendingAsk(harnesses.ClaudeConfigDirFor(d.srv.dataDir, sessionProfile(&row)), row.CLISessionID)
			if !askShown(ask, shown) {
				ask = nil
			}
		}
	}
	language := config.LanguageName(config.NormalizeLanguage(settings.Voice.Language))
	system, prompt, tokens := digestSystemPrompt(language), digestPrompt(&row, activity, screen, ask), digestMaxTokens
	if judge {
		system += outcomeSystemPrompt()
		tokens = outcomeMaxTokens
		if row.Harness == config.HarnessClaude {
			configDir := harnesses.ClaudeConfigDirFor(d.srv.dataDir, sessionProfile(&row))
			if turn, ok := readClaudeTurn(configDir, row.CLISessionID); ok {
				if words := turnPrompt(turn); words != "" {
					prompt = words + prompt
				}
			}
		}
	}
	messages := []openrouter.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	answer, err := askDigest(ctx, client, model, messages, tokens)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return
		}
		why := err.Error()
		var refusal *openrouter.StatusError
		switch {
		case errors.As(err, &refusal):
			_, why = modelProblem(err, model, "home")
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			why = fmt.Sprintf("the home model %s did not answer within %s", model, digestTimeout)
		}
		d.fail(e, gen, hash, activity, why)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries[row.ID] != e || e.gen != gen {
		return
	}
	e.cancel = nil
	e.hash, e.look = hash, ""
	e.fails, e.dropped = 0, false
	// The state the screen was read in is the state the digest is for. If
	// the session has left it while the model was thinking, the question
	// is already gone from the screen and does not get into the digest.
	question := sanitizeQuestion(answer.Question, activity.State)
	recordedText(question, ask)
	if now, ok := d.seen[row.ID]; ok && now.State != activity.State {
		question = nil
	}
	// A menu open under its cursor on the screen of a session the detector
	// still calls idle is no question asked in prose. It is a prompt the
	// state has not caught up with - the status is written a moment after
	// the paint, and the detector commits on its next tick - or a menu the
	// person opened at the prompt. Answered as prose, its words would be
	// typed into the menu. The move to waiting reads it again (observe).
	if question != nil && question.Source == questionText && menuOpen(visible) {
		question = nil
	}
	e.d.Phase, e.d.Error = digestReady, ""
	e.d.State = string(activity.State)
	e.d.MadeAt = time.Now().UnixMilli()
	e.d.Model = model
	e.d.Summary = oneLine(answer.Summary, digestSummaryRunes)
	e.d.Context = oneLine(answer.Context, digestContextRunes)
	e.d.Question = question
	d.publishLocked(row.ID, e)
	if now, ok := d.seen[row.ID]; ok {
		// A turn that ended by asking in prose was pushed as "Finished"; it
		// is a question, and the phone is told so.
		if question != nil && question.Source == questionText {
			d.srv.push.askedInProse(row.ID, row.Title, now, question)
		}
		// A prompt that is waiting for its words to be pushed has them.
		d.srv.push.read(row.ID, activity, now, question)
	}
	if judge {
		d.judgedLocked(row.ID, activity, answer.Outcome, question, model, "")
	}
	// Read in no state, and the detector has answered since: the state it
	// answered with is the reading that counts, and observe, which came
	// while this one was on its way, left it to this one.
	if now, ok := d.seen[row.ID]; ok && activity.State == termux.StateUnknown && now.State != termux.StateUnknown {
		d.trigger(row.ID, false)
	}
}

// fail ends a digest with a sentence. The summary and context it had are
// kept: the last thing that was true about a session is a better line than
// none.
//
// The question is not. It was read from one screen, and a read that failed
// for a different screen - or for one it could not even look at - was made
// because the prompt may have moved on: what the digest would hold up is a
// question the screen may no longer be asking, with buttons that answer it.
//
// The state is the one the read was made in, recorded as a ready digest
// records it: a failed read is still the latest word on what the session is
// doing. A failure that kept an earlier digest's state - or "unknown", on a
// session that had never been read - would have POST /answer refuse an
// answer of one's own to a session that is waiting, as one to a session that
// has stopped asking. A question that is kept was read in this same state.
//
// in is the activity the read was made in. A prompt waiting for its words to
// be pushed is pushed without them.
func (d *digester) fail(e *digestEntry, gen uint64, hash string, in termux.Activity, why string) {
	state := in.State
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries[e.d.SessionID] != e || e.gen != gen {
		return
	}
	e.cancel = nil
	if e.d.Question != nil && (hash == "" || hash != e.hash || e.d.State != string(state)) {
		e.d.Question = nil
	}
	e.d.State = string(state)
	e.hash, e.look = hash, ""
	e.fails++
	e.dropped = false
	e.d.Phase, e.d.Error = digestError, why
	d.publishLocked(e.d.SessionID, e)
	if now, ok := d.seen[e.d.SessionID]; ok {
		d.srv.push.read(e.d.SessionID, in, now, nil)
	}
	d.judgedLocked(e.d.SessionID, in, nil, nil, e.d.Model, why)
}

// failedRead is whether an entry holds a read that failed and whose wait to
// be tried again is over: not the "no key" sentence, which is no failed read
// and is read again when there is a key (fillMissing), and not one with a
// read on its way. Called with the lock held.
func (d *digester) failedRead(e *digestEntry, now time.Time) bool {
	return e.cancel == nil && e.fails > 0 && e.d.Phase == digestError && e.d.Error != digestNoKey &&
		(e.tried.IsZero() || now.Sub(e.tried) >= d.retryAfter(e.fails))
}

// retryAfter is how long the looks wait before reading a screen again after
// fails failed reads in a row: lookEvery, doubled for each, up to freshFor.
func (d *digester) retryAfter(fails int) time.Duration {
	wait := d.lookEvery
	for i := 0; i < fails && wait < d.freshFor; i++ {
		wait *= 2
	}
	return min(wait, d.freshFor)
}

// digestAnswer is the model's object before it is sanitised.
type digestAnswer struct {
	Summary  string          `json:"summary"`
	Context  string          `json:"context"`
	Question *digestQuestion `json:"question"`
	// Outcome is there only when the read was asked for one (outcome.go).
	Outcome *outcomeAnswer `json:"outcome"`
}

// askDigest asks once and, if the answer is not the object, exactly once more
// with the parser's complaint attached - the operator's rule, for the same
// reason.
func askDigest(ctx context.Context, client *openrouter.Client, model string,
	messages []openrouter.Message, maxTokens int) (digestAnswer, error) {
	temperature := 0.0
	attempt := messages
	for try := 1; try <= 2; try++ {
		res, err := client.Chat(ctx, openrouter.ChatRequest{
			Model:       model,
			Messages:    attempt,
			Temperature: &temperature,
			MaxTokens:   maxTokens,
		}, nil)
		if err != nil {
			return digestAnswer{}, err
		}
		answer, perr := parseDigestAnswer(res.Content)
		if perr == nil {
			return answer, nil
		}
		attempt = append(append([]openrouter.Message{}, attempt...),
			openrouter.Message{Role: "assistant", Content: res.Content},
			openrouter.Message{Role: "user", Content: "That was not usable JSON: " + perr.Error() +
				". Answer with one JSON object and nothing else."})
	}
	return digestAnswer{}, errDigestJSON
}

func parseDigestAnswer(raw string) (digestAnswer, error) {
	text := stripFence(strings.TrimSpace(raw))
	var answer digestAnswer
	if text == "" {
		return answer, errors.New("the answer was empty")
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return answer, err
	}
	return answer, nil
}

// digestMarker opens every digest instruction. It says what the call is for,
// and it is how a stand in gateway tells a digest from an operator step.
const digestMarker = "You read the terminal of a coding session for Home"

// digestSystemPrompt is what a digest is, and the shape of its answer.
//
// Option labels are kept as the screen spells them, in the screen's order:
// the operator that answers later finds the option it is told to choose by
// that label, on the same screen, and a translated label is one it has to
// guess at.
func digestSystemPrompt(language string) string {
	return digestMarker + ", the page of Socrates that shows a person what each of their " +
		"coding sessions is doing and answers the questions they ask. You are given the program " +
		"that runs in the terminal, what the activity detector says it is doing, and the last " +
		fmt.Sprint(digestScreenLines) + " lines of its screen.\n" +
		"Answer with one JSON object and nothing else:\n" +
		`{"summary":"…","context":"…","question":null}` + " or, when something is asked,\n" +
		`{"summary":"…","context":"…","question":{"kind":"choice|multi|confirm|plan|text","detail":"…",` +
		`"items":[{"header":"…","text":"…","screen_text":"…","multi":false,"free_text":false,` +
		`"options":[{"label":"…","description":"…","recommended":false}]}]}}` + "\n" +
		"summary: one line of at most " + fmt.Sprint(digestSummaryRunes) + " characters - what the " +
		"session is working on, or what it has done; plain words, no markdown, no file paths unless " +
		"they are the point.\n" +
		"context: when something is asked, two or three sentences a person reads before answering - " +
		"which program in which project, what it is working on, and why it asks. Otherwise empty.\n" +
		"question: only when the terminal is waiting for the user, or is idle and its last answer " +
		"ends by asking the user something; null in every other case, and null when you are not sure.\n" +
		"- kind: choice (pick one), multi (pick several), confirm (a permission prompt: yes, no, and " +
		"the like), plan (a plan to approve), text (asked in prose, with no options).\n" +
		"- items: one per question; a prompt with several tabs has one item per tab, in order.\n" +
		"- screen_text: the item's question exactly as the screen (or the program's own record) " +
		"spells it, untranslated; empty when the screen shows no question text for it.\n" +
		"- options: the answers on the screen, in the screen's order, each label exactly as the " +
		"screen spells it and without its number. Leave out the rows that are not answers - the row " +
		`for an answer of one's own ("Type something.", "Tell Claude what to change", "No, and tell ` +
		`Claude what to do differently", "No, and tell Codex what to do differently") becomes ` +
		`free_text true, and "Chat about this", "Submit" and "Cancel" rows are left out. ` +
		`recommended is true only when the screen says so.\n` +
		"- detail: optional, for example the plan in two sentences.\n" +
		"Write summary, context, the question text and detail in " + language + "; keep headers, " +
		"labels and descriptions as the screen shows them."
}

// digestPrompt is the one session the digest is about.
func digestPrompt(row *store.Session, a termux.Activity, screen string, ask json.RawMessage) string {
	state := stateWords(a.State)
	if note := strings.TrimSpace(a.Note); note != "" {
		state += " (" + note + ")"
	}
	var b strings.Builder
	b.WriteString("The terminal runs " + harnessLabel(row.Harness) + ". The session is called " +
		fmt.Sprintf("%q", row.Title) + " and works in " + row.Workdir + ". It is " + state + ".\n")
	if len(ask) > 0 {
		b.WriteString("The program's own record of the question it is asking. It is authoritative " +
			"for the items, their options and whether several may be chosen - the screen shows only " +
			"one tab of a question with several:\n")
		b.Write(ask)
		b.WriteString("\n")
	}
	b.WriteString("Screen:\n" + screen)
	return b.String()
}

/* ---------------------------------------------------------- sanitising */

// menuOpen is whether a screen ends on a menu with its cursor on a numbered
// row ("❯ 1. Postgres", "› 2. No, and tell Codex…"): a prompt that takes
// keys, not a line. A list in the program's answer has no cursor, and the
// idle prompt's own "❯" has no number after it.
func menuOpen(screen string) bool {
	for _, row := range screenRows(screen) {
		if row.token != "" && row.cursor {
			return true
		}
	}
	return false
}

// sanitizeQuestion holds a question to what a dialog can show, and keeps it
// only in a state that asks one: a prompt on the screen while waiting, a
// question in prose while idle.
func sanitizeQuestion(q *digestQuestion, state termux.State) *digestQuestion {
	if q == nil {
		return nil
	}
	out := &digestQuestion{
		Kind:   strings.ToLower(strings.TrimSpace(q.Kind)),
		Detail: clipRunes(strings.TrimSpace(q.Detail), digestTextRunes),
	}
	switch state {
	case termux.StateWaiting:
		out.Source = questionMenu
	case termux.StateIdle:
		out.Source = questionText
	default:
		return nil
	}
	switch out.Kind {
	case "choice", "multi", "confirm", "plan", "text":
	default:
		out.Kind = "choice"
	}
	for _, item := range q.Items {
		if len(out.Items) == digestItemsMax {
			break
		}
		clean := digestItem{
			Header:     oneLine(item.Header, digestLabelRunes),
			Text:       clipRunes(strings.TrimSpace(item.Text), digestTextRunes),
			ScreenText: clipRunes(rowText(item.ScreenText), digestTextRunes),
			Multi:      item.Multi,
			FreeText:   item.FreeText || out.Kind == "text",
		}
		labels := map[string]bool{}
		for _, option := range item.Options {
			if len(clean.Options) == digestOptionsMax {
				break
			}
			label := oneLine(option.Label, digestLabelRunes)
			// An empty label is no button, and two with the same label are
			// one button: an answer names its choice by its label.
			if label == "" || labels[label] {
				continue
			}
			labels[label] = true
			clean.Options = append(clean.Options, digestOption{
				Label:       label,
				Description: oneLine(option.Description, digestDescriptionRunes),
				Recommended: option.Recommended,
			})
		}
		if len(clean.Options) == 0 && !clean.FreeText {
			continue
		}
		out.Items = append(out.Items, clean)
	}
	if len(out.Items) == 0 {
		return nil
	}
	return out
}

// recordedText gives each item of a question its text as the program's own
// record of it has it (claudePendingAsk), which is the text the screen shows
// whatever language the digest wrote: the record's questions are the items,
// in order, and an item is matched only to the one with its options.
func recordedText(q *digestQuestion, ask json.RawMessage) {
	if q == nil || len(ask) == 0 {
		return
	}
	var record struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(ask, &record) != nil || len(record.Questions) != len(q.Items) {
		return
	}
	for i := range q.Items {
		asked := record.Questions[i]
		labels := map[string]bool{}
		for _, option := range asked.Options {
			labels[strings.ToLower(rowText(option.Label))] = true
		}
		same := strings.TrimSpace(asked.Question) != ""
		for _, option := range q.Items[i].Options {
			same = same && labels[strings.ToLower(rowText(option.Label))]
		}
		if same {
			q.Items[i].ScreenText = clipRunes(rowText(asked.Question), digestTextRunes)
		}
	}
}

// oneLine is text with every run of white space made one space, cut to n runes.
func oneLine(text string, n int) string {
	return clipRunes(strings.Join(strings.Fields(text), " "), n)
}

// clipRunes cuts text to at most n runes, with an ellipsis where it was cut.
func clipRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return strings.TrimSpace(string(runes[:n-1])) + "…"
}

// screenHash is what two looks at a screen compare: its lines without their
// trailing blanks, and without the blank lines above and below them, so that
// a pane that was made taller or shorter around the same prompt is the same
// screen - and with what ticks on it masked where it ticks, and nowhere else
// (hashedLines). A prompt with a sub-agent's progress above it, a clock in
// its bottom bar or a token count under its rows is the same prompt a second
// later, where a screen compared as it is would be a new question on every
// look, and no answer to it could ever be sent. What it asks, the command or
// the amount it asks about and its rows are compared as they are: the same
// permission prompt for "--amount=$5000" where "--amount=$5" was answered, or
// for "sleep 600s" after "sleep 5s", is a question nobody has read.
func screenHash(screen string) string {
	sum := sha256.Sum256([]byte(strings.Join(hashedLines(screen), "\n")))
	return hex.EncodeToString(sum[:16])
}

// hashedLines is a screen as screenHash compares it. The rows of the menu on
// it, and the rest of a label its last row wraps onto, are as they are.
// Anywhere else a line that reports progress (progressLine) is the same line
// whatever it says: a spinner's "✻ Cooking… (12s · ↓ 345 tokens)", a
// sub-agent's "⎿ Read(file.go)" and "+3 more tool uses", "Done (12 tool uses
// · 1m 2s)". Under the menu's last row - its hint, the bottom bar - and on a
// bottom bar anywhere (barLine), a duration, a count of tokens, a percentage,
// a cost or a time of day is masked (tickingText). The lines above the menu
// that are not progress are the question, and nothing on them is masked.
func hashedLines(screen string) []string {
	lines := screenLines(screen)
	first, last := menuSpan(lines)
	for i, line := range lines {
		switch {
		case first >= 0 && i >= first && i <= last:
		case progressLine.MatchString(line):
			lines[i] = "~"
		case first >= 0 && i > last, barLine.MatchString(line):
			lines[i] = tickingText.ReplaceAllString(line, "#")
		}
	}
	return lines
}

// menuSpan is the first and the last line of the menu on a screen - its last
// run of rows (screenRows), down to the rest of the last row's label - or -1
// and -1 for a screen with none.
func menuSpan(lines []string) (int, int) {
	rows, starts := allScreenRows(strings.Join(lines, "\n"))
	if len(rows) == 0 {
		return -1, -1
	}
	first, last := rows[starts[len(starts)-1]].line, rows[len(rows)-1].line
	column := len([]rune(rowPointer.FindString(lines[last])))
	if m := numberedRow.FindStringSubmatchIndex(lines[last]); m != nil {
		column = len([]rune(lines[last][:m[6]]))
	}
	for k := 0; k < rowContinues && last+1 < len(lines); k++ {
		next := lines[last+1]
		indent := len([]rune(next)) - len([]rune(strings.TrimLeft(next, " ")))
		if strings.TrimSpace(next) == "" || ruleLine.MatchString(next) || indent < column {
			break
		}
		last++
	}
	return first, last
}

// progressLine is a line a harness rewrites while it works, and the same
// line whatever it says: a braille spinner in front of it; one of Claude's
// spinner frames ("·", "✢", "✳", "✶", "✻", "✽") in front of a line that is
// still going ("Cooking…") or says for how long ("Worked for 3s"); a tool's
// or a sub-agent's line under its call ("⎿ Read(file.go)"); a sub-agent's
// "+3 more tool uses" and "Done (12 tool uses · …)"; a busy hint. A markdown
// bullet ("*", "•", "-") is none of them: a plan is written in them.
var progressLine = regexp.MustCompile(`^[\s│┃|]*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏◐◓◑◒]` +
	`|^[\s│┃|]*[·✢✳✶✻✽]\s.*(?:…|\.\.\.|\b\d+(?:[.,]\d+)?\s?(?:ms|s|m|h)\b)` +
	`|^[\s│┃|]*⎿` +
	`|^[\s│┃|]*\+\d+ more tool uses?\b` +
	`|\b\d+ tool uses?\b` +
	`|esc to interrupt|esc interrupt`)

// barLine is a harness's bottom bar, where a token count, a cost or a clock
// ticks on a screen with no menu as well: OpenCode's "ctrl+p commands",
// Codex's "100% context left", Claude's mode bar and its "? for shortcuts".
var barLine = regexp.MustCompile(`ctrl\+p commands|context left|for shortcuts|⏵⏵|⏸|auto-compact`)

// tickingText is what a screen counts while it shows the same thing: a
// duration ("12s", "1m 5s", "350ms"), a count of tokens ("↓ 345 tokens",
// "10.5K"), a percentage, a cost, a time of day. A bare number is not in it:
// "kill 1234" and "kill 5678" are two different questions.
var tickingText = regexp.MustCompile(`\b\d+(?:[.,]\d+)?\s?(?:ms|s|m|h|min|mins|sec|secs)\b` +
	`|\b\d+(?:[.,]\d+)?\s?[kKmM]?\s+tokens?\b` +
	`|\b\d+(?:[.,]\d+)?[kKM]\b` +
	`|\b\d+(?:[.,]\d+)?\s?%` +
	`|\$\s?\d+(?:[.,]\d+)?` +
	`|\b\d{1,2}:\d{2}(?::\d{2})?\b`)

// screenLines is a screen as screenHash compares it.
func screenLines(screen string) []string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

/* ------------------------------------------------- the waiting screens */

// start runs triggers 3 and 4 until ctx ends - after one read of every
// session that came through a restart with no digest.
//
// Digests live in memory, and a restart takes them all. A session that sits
// waiting is read again on its first state (observe), and one that is
// working by the looks while anybody is watching; but one that sits idle has
// no edge to be read on and no look that tries it, and the turn it ended on
// may have ended on a question in prose. Until a page connected and its
// hello read it, the asking set would leave it out: the next push - a
// prompt in another session - would take one off the app's icon, and close
// the notification of the question it is still asking. So it is read now,
// whether or not anybody is looking, as a hello would read it; the detector
// has no state for anything yet, so each one is read on its first
// (fillMissing puts it off until then).
func (d *digester) start(ctx context.Context) {
	go func() {
		d.fillMissing()
		ticker := time.NewTicker(d.lookEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			d.lookAround(ctx)
		}
	}()
}

// lookAround is one pass of triggers 3 and 4.
//
// 3: a waiting session whose screen differs from the one its digest was read
// from, and is the same on two looks in a row, is read again - the prompt
// moved on, or was answered in the terminal and another one appeared. Two
// looks, because a prompt that is being repainted is not yet the next one.
//
// 4: while anybody is looking at Home, a working session whose summary is a
// minute old is read again when its screen has changed: the summary stays
// current for the people who are reading it, and costs nothing when nobody is.
//
// And an idle session whose last read failed is read again once its wait is
// over, as a waiting one is: nothing else would read it before its next turn,
// and the turn that ended may have ended on a question in prose - one Home
// would never count, and a push that would stay "Finished". While nobody is
// looking at Home it is tried only digestIdleRetries times, so that a gateway
// that is down does not take a call per idle session every minute for good.
func (d *digester) lookAround(ctx context.Context) {
	if strings.TrimSpace(d.srv.Settings().OpenRouter.APIKey) == "" {
		return
	}
	rows, err := d.srv.store.ListSessions()
	if err != nil {
		return
	}
	now := time.Now()
	watched := d.srv.events.count() > 0 || d.polledSince(now.Add(-homePolledFor))
	for i := range rows {
		row := &rows[i]
		if ctx.Err() != nil {
			return
		}
		// A session an answer is being typed into is read when the run
		// ends: its screen is moving because of the run.
		if !digestible(row) || d.srv.answers.live(row.ID) {
			continue
		}
		state := d.stateOf(row.ID).State
		waiting := state == termux.StateWaiting
		working := state == termux.StateBusy || state == termux.StateTasks
		if state == termux.StateIdle {
			d.mu.Lock()
			e := d.entries[row.ID]
			retry := e != nil && e.d.State == string(termux.StateIdle) && d.failedRead(e, now) &&
				(watched || e.fails < digestIdleRetries)
			d.mu.Unlock()
			// With the summaries, not ahead of them: a retry is nothing a
			// person is waiting on, and a gateway that is slow is when the
			// call kept for a question is wanted most. One that has started
			// asking since is a question again (requestAs).
			if retry {
				d.requestBulk(row)
			}
			continue
		}
		if !waiting && !(working && watched) {
			continue
		}
		d.mu.Lock()
		e := d.entries[row.ID]
		inFlight := e != nil && e.cancel != nil
		// Counted from the last attempt, whatever it ended in: a model that
		// fails is not asked again on every look at a screen whose clock
		// ticks, and a failing one is asked less often each time.
		fresh := e != nil && !e.tried.IsZero() && now.Sub(e.tried) < d.freshFor
		backoff := e != nil && e.fails > 0 && !e.tried.IsZero() && now.Sub(e.tried) < d.retryAfter(e.fails)
		d.mu.Unlock()
		if inFlight || (working && fresh) || backoff {
			continue
		}
		one, cancel := context.WithTimeout(ctx, 10*time.Second)
		visible, err := d.visible(one, row.ID)
		cancel()
		if err != nil {
			continue
		}
		hash := screenHash(visible)

		d.mu.Lock()
		e = d.entries[row.ID]
		again := false
		switch {
		case e == nil:
			// A waiting session nobody has read yet, or a working one with no
			// line on Home: either way there is nothing to compare with.
			again = true
		case e.cancel != nil:
		case waiting:
			// A read that failed is tried again once its wait is over, on
			// the same screen too: the question it was for is still being
			// asked, and nothing else will read it.
			again = (hash != e.hash && hash == e.look) || (e.fails > 0 && e.d.Phase == digestError)
			e.look = hash
		default:
			again = hash != e.hash
		}
		d.mu.Unlock()
		switch {
		case again && waiting:
			d.request(row)
		case again:
			d.requestBulk(row)
		}
	}
}

/* ---------------------------------------------- Claude Code's own record */

// claudePendingAsk is the input of the AskUserQuestion call a Claude Code
// session is waiting on, or nil.
//
// The screen of a question with several tabs shows one tab; the transcript
// has all of them, with their options and whether each takes several
// answers. The pending call is the last assistant tool_use named
// AskUserQuestion that has no tool_result yet and no prompt of the person's
// after it. It is only ever asked about a menu that is open on the screen
// (askShown). The transcript is found the
// way a resume verifies it - <config dir>/projects/*/<cli session id>.jsonl -
// and anything that goes wrong on the way is simply no record: the screen is
// still there to read.
func claudePendingAsk(configDir, cliSession string) json.RawMessage {
	cliSession = strings.TrimSpace(cliSession)
	if configDir == "" || cliSession == "" || filepath.Base(cliSession) != cliSession {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", cliSession+".jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	data, err := readTail(matches[0], digestTranscriptTail)
	if err != nil {
		return nil
	}
	var pendingID string
	var pending json.RawMessage
	answered := map[string]bool{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
		}
		// A user message that is a prompt - a plain string, or blocks with
		// text in them - is a turn the person started after the question:
		// nobody types one while a menu is open, so the question was left
		// behind (the program killed or interrupted, the session resumed)
		// and is asked no more, whatever its tool_use is still owed.
		var prompt string
		if entry.Type == "user" && json.Unmarshal(entry.Message.Content, &prompt) == nil {
			pendingID, pending = "", nil
			continue
		}
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			switch {
			case entry.Type == "assistant" && block.Type == "tool_use" && block.Name == "AskUserQuestion":
				pendingID, pending = block.ID, block.Input
			case block.Type == "tool_result" && block.ToolUseID != "":
				answered[block.ToolUseID] = true
			case entry.Type == "user" && block.Type == "text":
				pendingID, pending = "", nil
			}
		}
	}
	if pendingID == "" || answered[pendingID] || len(pending) == 0 {
		return nil
	}
	var compact bytes.Buffer
	if json.Compact(&compact, pending) != nil || compact.Len() > digestAskInputBytes {
		return nil
	}
	return compact.Bytes()
}

// askShown is whether the question a record of Claude's (claudePendingAsk)
// holds is the one on a screen: the text of one of its questions, or two of
// its options (questionShown). A record whose question is not there is one
// the program left behind - the screen asks something else, or nothing.
func askShown(ask json.RawMessage, screen string) bool {
	if len(ask) == 0 {
		return false
	}
	var record struct {
		Questions []struct {
			Question    string `json:"question"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(ask, &record) != nil || len(record.Questions) == 0 {
		return false
	}
	q := &digestQuestion{Source: questionMenu}
	for _, asked := range record.Questions {
		item := digestItem{Text: asked.Question, Multi: asked.MultiSelect}
		for _, option := range asked.Options {
			item.Options = append(item.Options, digestOption{Label: option.Label})
		}
		q.Items = append(q.Items, item)
	}
	return questionShown(q, screen)
}

// readTail reads at most the last n bytes of a file, from the start of the
// first whole line in them.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > n {
		offset = info.Size() - n
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, err
	}
	if offset > 0 {
		if nl := bytes.IndexByte(buf, '\n'); nl >= 0 {
			buf = buf[nl+1:]
		}
	}
	return buf, nil
}

/* ------------------------------------------------------------ the route */

// handleDigestNow reads a session's screen again, now: the dialog's "Read
// again", and its "Try again" after an error.
func (s *Server) handleDigestNow(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	if row.IsChat() {
		// A chat's reading is its journal, current already; what can be read
		// again is the verdict on its last turn.
		writeJSON(w, http.StatusAccepted, map[string]any{"digest": s.chats.rejudge(row.ID)})
		return
	}
	if !codingHarness(row.Harness) {
		writeError(w, http.StatusBadRequest, "only a coding session has a screen worth reading for Home")
		return
	}
	if row.State != store.StateRunning {
		writeError(w, http.StatusBadRequest, "this session is not running, so there is no screen to read")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"digest": s.digests.requestNow(row)})
}
