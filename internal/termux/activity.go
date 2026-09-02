package termux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// State is what a session is doing, as far as anything outside this package is
// concerned. It is not the store's `state`: a session can be `running` and any
// of these four.
type State string

const (
	// StateUnknown is no usable evidence yet, and is where every session
	// starts and where a dead pane ends.
	StateUnknown State = "unknown"
	// StateBusy is the harness working.
	StateBusy State = "busy"
	// StateIdle is a harness waiting for a new instruction.
	StateIdle State = "idle"
	// StateWaiting is a harness that needs the user: a permission prompt, a
	// question, a menu.
	StateWaiting State = "waiting"
)

// Activity is one session's answer, as the browser receives it.
//
// State lives only in memory, like sizeOwner: it is re-derived from a live
// pane every second, so a persisted copy could only ever be stale. Unread is
// the one field that is written down, because "this finished while you were
// not looking" is exactly the fact a restart must not lose.
type Activity struct {
	State  State  `json:"state"`
	Unread bool   `json:"unread"`
	Since  int64  `json:"since"`          // unix ms of the last committed change
	Source string `json:"source"`         // "exact"|"screen"|"quiet"|"pane" - hover-only detail
	Note   string `json:"note,omitempty"` // e.g. "permission prompt" - hover-only
}

// Where an answer came from, which the page shows only on hover.
const (
	sourceExact  = "exact"
	sourceScreen = "screen"
	sourceQuiet  = "quiet"
	sourcePane   = "pane"
)

// The detector's whole clock, in one place.
//
// The three settle counts are anti-flicker and nothing else: busy has to
// appear at once because the spinner is the only feedback a typed prompt gets,
// and idle has to wait two ticks because a harness between two tool calls is
// briefly quiet and a sidebar that blinks on every tool call is worse than one
// that is a second late.
const (
	// ActivityInterval is one tick: one tmux command for every session.
	ActivityInterval = 1 * time.Second
	// ScrapeEvery is how often one session's screen may be captured.
	ScrapeEvery = 2 * time.Second
	// IdleSettle and UnknownSettle are consecutive ticks before those two
	// states are committed.
	IdleSettle    = 2
	UnknownSettle = 3
	// ExactMissTicks is how many ticks an exact answer is reused after its
	// source stops answering.
	ExactMissTicks = 3
	// BusyCeiling is how long a committed busy may stand on an exact source
	// that has gone silent before that answer is dropped altogether.
	BusyCeiling = 120 * time.Second
	// HardQuiet and QuietBusy are the two ends of output quiescence: no bytes
	// at all for HardQuiet is idle, bytes within QuietBusy is busy, and
	// anything between is no answer.
	HardQuiet = 30 * time.Second
	QuietBusy = 2 * time.Second
)

// unreadKey is where the unread marks live in the store's key/value table. A
// map of session id to the moment it was marked, so that a row that no longer
// exists can be dropped on the way in.
const unreadKey = "activity.unread"

// ActivityChange is one committed change, for the callers that would otherwise
// have to poll: the operator loop waiting for a turn to end, and anything else
// that wants to know without asking every second.
type ActivityChange struct {
	SessionID string
	Activity  Activity
}

// ------------------------------------------------------------- the pane line

// paneLine is one line of the activity poll's own `list-panes -a`.
//
// It is a second list-panes, kept apart from the lifecycle poll's, so that
// neither can break the other's parsing: a format that gained a field would
// otherwise silently change what "the pane is dead" means.
type paneLine struct {
	session  string
	dead     bool
	pid      int
	activity int64 // #{window_activity}, the epoch second of the pane's last output
	command  string
	title    string
}

// The title comes last because Codex's approval title contains a pipe
// (`[ . ] Action Required | SocratesAgent`), and SplitN hands the last field
// back whole. Every field before it is pipe-free by construction.
const activityPaneFormat = "#{session_name}|#{pane_dead}|#{pane_pid}|#{window_activity}|" +
	"#{pane_current_command}|#{pane_title}"

func parseActivityPanes(out string) map[string]paneLine {
	panes := map[string]paneLine{}
	for _, line := range Lines(out) {
		f := strings.SplitN(line, "|", 6)
		if len(f) < 6 {
			continue
		}
		p := paneLine{session: f[0], dead: f[1] == "1", command: f[4], title: f[5]}
		p.pid, _ = strconv.Atoi(strings.TrimSpace(f[2]))
		p.activity, _ = strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		// One session, one window, one pane: a later line for the same session
		// would only be a pane we did not make.
		if _, seen := panes[p.session]; !seen {
			panes[p.session] = p
		}
	}
	return panes
}

func (m *Manager) listActivityPanes(ctx context.Context) (map[string]paneLine, error) {
	out, err := m.tmux.Run(ctx, "list-panes", "-a", "-F", activityPaneFormat)
	if err != nil {
		// A live server that has lost its last session refuses a command that
		// needs a target. That is an empty answer, not a failure.
		if noSuchTarget(err) {
			return map[string]paneLine{}, nil
		}
		return nil, err
	}
	return parseActivityPanes(out), nil
}

// ---------------------------------------------------------------- the layers

// snapshot is everything a layer is allowed to look at.
type snapshot struct {
	row  store.Session
	pane paneLine
	plan harnesses.LaunchPlan
	now  time.Time
}

// observation is a layer's answer. Not every layer can answer every tick, and
// "I do not know" is a different thing from "unknown".
type observation struct {
	state State
	note  string
	ok    bool
}

func seen(s State, note string) observation { return observation{state: s, note: note, ok: true} }

// missing is the answer of a layer that has nothing to say.
var missing = observation{}

// source is one harness's exact signal: Claude's session file, Codex's title,
// OpenCode's event stream, the shell's foreground process.
type source interface {
	Read(ctx context.Context, s snapshot) observation
}

// forgetter is a source that keeps something per session - a resolved pid, an
// open event stream - and has to be told when a session goes.
type forgetter interface {
	forget(sessionID string)
}

// --------------------------------------------------------------- the tracker

// track is the detector's memory of one session. Only the tick writes it,
// under the activity lock.
type track struct {
	committed Activity
	running   bool

	// The candidate state and how many consecutive ticks have proposed it.
	pending      State
	pendingCount int

	// The last exact answer, when it came, and how many ticks have gone
	// without one. dropped is the runaway guard having given up on it.
	lastExact   observation
	lastExactAt time.Time
	exactMiss   int
	dropped     bool

	// The last screen scrape and when it was taken; a capture-pane is a fork,
	// so one session's screen is read at most every ScrapeEvery.
	scrapeAt   time.Time
	lastScrape observation

	// waitingAt is when waiting was committed and inputAt when a keystroke was
	// last accepted. Quiescence may only release a waiting that has been
	// answered, and those two timestamps are how that is known.
	waitingAt time.Time
	inputAt   time.Time
}

// activity is the whole detector: the per session tracks, the unread marks,
// the exact sources and the subscribers. It has a lock of its own rather than
// the Manager's, because the tick holds it across a capture-pane and the
// Manager's lock is on the path of every attach.
type activity struct {
	m *Manager

	mu       sync.Mutex
	tracks   map[string]*track
	unread   map[string]int64
	sources  map[harnesses.Kind]source
	subs     map[int]chan ActivityChange
	nextSub  int
	loaded   bool
	pending  []ActivityChange
	started  bool
	stopOnce sync.Once

	// capture is how the screen layer reads a pane, and now is the clock the
	// two out-of-band writers read. Both are fields so that the ladder's table
	// tests can drive a sequence of ticks without a tmux server and without
	// waiting for real seconds to pass.
	capture func(ctx context.Context, sessionID string, lines int) (string, error)
	now     func() time.Time
}

func newActivity(m *Manager) *activity {
	a := &activity{
		m:       m,
		tracks:  map[string]*track{},
		unread:  map[string]int64{},
		subs:    map[int]chan ActivityChange{},
		capture: m.CapturePane,
		now:     time.Now,
	}
	a.sources = map[harnesses.Kind]source{
		harnesses.KindShell:    shellSource{},
		harnesses.KindClaude:   newClaudeSource(),
		harnesses.KindCodex:    codexSource{},
		harnesses.KindOpenCode: newOpenCodeSource(m),
	}
	return a
}

// setSource replaces one harness's exact layer. It is unexported because it
// exists for the ladder's table tests and for nothing else.
func (m *Manager) setSource(kind harnesses.Kind, src source) {
	m.act.mu.Lock()
	defer m.act.mu.Unlock()
	m.act.sources[kind] = src
}

// -------------------------------------------------------------------- the API

// StartActivity brings the detector up: the unread marks come back from the
// store, and one goroutine polls tmux once a second for every session at once.
//
// It is started after Adopt, so that the first tick sees the sessions that
// survived the last run rather than an empty server.
func (m *Manager) StartActivity(ctx context.Context) {
	m.act.load()
	m.act.mu.Lock()
	if m.act.started {
		m.act.mu.Unlock()
		return
	}
	m.act.started = true
	m.act.mu.Unlock()

	go func() {
		ticker := time.NewTicker(ActivityInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.act.stop()
				return
			case <-ticker.C:
				m.act.tick(ctx, time.Now())
			}
		}
	}()
}

// ActivityOf is one session's committed answer. A session nobody has looked at
// yet is unknown, and carries whatever unread mark survived the last run.
func (m *Manager) ActivityOf(id string) Activity {
	a := m.act
	a.mu.Lock()
	defer a.mu.Unlock()
	if tr := a.tracks[id]; tr != nil {
		return tr.committed
	}
	return Activity{State: StateUnknown, Unread: a.unread[id] != 0}
}

// Activities is every running session's answer, for the frames that carry the
// whole map: a hello, and the catch-up after a reconnect.
func (m *Manager) Activities() map[string]Activity {
	a := m.act
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]Activity, len(a.tracks))
	for id, tr := range a.tracks {
		if tr.running {
			out[id] = tr.committed
		}
	}
	return out
}

// NoteInput records a keystroke that reached the pane. It clears the unread
// mark - typing at a session is the whole of having seen it - and it is what
// lets thirty seconds of silence release a permission prompt that was
// answered.
func (m *Manager) NoteInput(id string) {
	a := m.act
	a.mu.Lock()
	tr := a.trackOf(id)
	tr.inputAt = a.now()
	a.clearUnread(id, tr)
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// MarkRead clears the unread mark from an explicit read: the `read` control
// frame, or POST /api/sessions/{id}/read.
func (m *Manager) MarkRead(id string) {
	a := m.act
	a.mu.Lock()
	tr := a.trackOf(id)
	a.clearUnread(id, tr)
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// ResetActivity forgets everything derived about a session, which is what a
// relaunch, a resume and a restart all leave behind: a new process, a new pid,
// a new event stream, and an old answer that is about to be wrong.
//
// The unread mark is deliberately kept: work that finished before the restart
// is still work nobody has seen.
func (m *Manager) ResetActivity(id string) {
	a := m.act
	a.mu.Lock()
	tr := a.tracks[id]
	fresh := tr == nil || (tr.committed.State == StateUnknown && tr.pendingCount == 0)
	unread := a.unread[id] != 0
	a.tracks[id] = &track{
		committed: Activity{State: StateUnknown, Unread: unread, Since: a.now().UnixMilli()},
		running:   tr != nil && tr.running,
	}
	if !fresh {
		a.publish(id, a.tracks[id])
	}
	a.forgetSources(id)
	// A relaunch may have been planned differently - a new argv, a resumed
	// conversation, a different working directory - and two of the layers read
	// the plan.
	a.m.forgetPlan(id)
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// SubscribeActivity hands out a channel of committed changes and the function
// that closes it. It is how the operator loop waits for a turn to end without
// polling, and the only caller that has to keep reading: a subscriber that
// falls behind loses changes rather than blocking the tick.
func (m *Manager) SubscribeActivity() (<-chan ActivityChange, func()) {
	a := m.act
	a.mu.Lock()
	id := a.nextSub
	a.nextSub++
	ch := make(chan ActivityChange, 64)
	a.subs[id] = ch
	a.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			// Both under the lock: fire sends under it too, so a tick that is
			// publishing cannot be holding this channel while it closes.
			a.mu.Lock()
			delete(a.subs, id)
			close(ch)
			a.mu.Unlock()
		})
	}
}

// WaitIdle blocks until a session is anything but busy, and answers straight
// away when it already is. The context is the caller's whole patience: the
// operator loop gives one turn a bounded wait and the run's wall clock decides
// the rest.
func (m *Manager) WaitIdle(ctx context.Context, id string) (Activity, error) {
	if a := m.ActivityOf(id); a.State != StateBusy {
		return a, nil
	}
	ch, stop := m.SubscribeActivity()
	defer stop()
	// The state may have moved between the check and the subscription.
	if a := m.ActivityOf(id); a.State != StateBusy {
		return a, nil
	}
	for {
		select {
		case <-ctx.Done():
			return m.ActivityOf(id), ctx.Err()
		case change, ok := <-ch:
			if !ok {
				return m.ActivityOf(id), nil
			}
			if change.SessionID == id && change.Activity.State != StateBusy {
				return change.Activity, nil
			}
		}
	}
}

// -------------------------------------------------------------------- the tick

// tick asks tmux once what every session is doing and moves each row to the
// answer.
func (a *activity) tick(ctx context.Context, now time.Time) {
	if a.m.Available() != nil {
		return
	}
	rows, err := a.m.st.ListSessions(true)
	if err != nil {
		return
	}
	panes, err := a.m.listActivityPanes(ctx)
	if err != nil {
		// We could not tell. Unknown settles only when the command succeeded
		// and the session was absent, never on an error.
		return
	}
	a.apply(ctx, rows, panes, now)
}

// apply is the tick without tmux, which is what the ladder's table tests
// drive.
func (a *activity) apply(ctx context.Context, rows []store.Session, panes map[string]paneLine, now time.Time) {
	a.mu.Lock()
	seenRows := map[string]bool{}
	for i := range rows {
		row := rows[i]
		running := row.State == store.StateRunning || row.State == store.StateStarting
		_, tracked := a.tracks[row.ID]
		if !running && !tracked {
			// Nothing was ever said about this row, and nothing has to be.
			continue
		}
		seenRows[row.ID] = true
		pane, ok := panes[row.TmuxName]
		a.evaluate(ctx, row, pane, ok, running, now)
		// A session that has ended and settled on unknown is no longer the
		// detector's business; its unread mark is the store's.
		if tr := a.tracks[row.ID]; !running && tr.committed.State == StateUnknown && tr.pendingCount == 0 {
			delete(a.tracks, row.ID)
			a.forgetSources(row.ID)
			delete(seenRows, row.ID)
		}
	}
	// A row that is gone from the store entirely - deleted while the tick was
	// running - leaves nothing behind.
	for id := range a.tracks {
		if !seenRows[id] {
			delete(a.tracks, id)
			a.forgetSources(id)
		}
	}
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// evaluate runs the arbitration ladder for one session. The activity lock is
// held: the layers do read a file and, at most every two seconds, fork a
// capture-pane, and the alternative - copying every track in and out - buys
// nothing on a lock nothing else holds for long.
func (a *activity) evaluate(ctx context.Context, row store.Session, pane paneLine, alive, running bool, now time.Time) {
	tr := a.trackOf(row.ID)
	tr.running = running

	// 1. The pane is gone or dead. Nothing else is asked, and everything this
	//    session was keeping open is closed.
	if !alive || pane.dead {
		a.forgetSources(row.ID)
		a.commit(row.ID, tr, observation{state: StateUnknown, ok: true}, sourcePane, now)
		return
	}

	kind := harnesses.Kind(row.Harness)
	snap := snapshot{row: row, pane: pane, plan: a.m.planFor(row.ID), now: now}

	// 2. The exact layer, with a short memory: a signal that blinks out for a
	//    tick or two is a file being rewritten, not a turn ending.
	obs, from, fresh := a.exact(ctx, kind, tr, snap)

	// 3-4. The screen, then quiescence.
	if !obs.ok {
		if scraped := a.scrape(ctx, kind, tr, snap); scraped.ok {
			obs, from = scraped, sourceScreen
		}
	}
	if !obs.ok {
		if quiet := quiescence(pane, now); quiet.ok {
			obs, from = quiet, sourceQuiet
		}
	}
	// 5. Nothing at all.
	if !obs.ok {
		obs, from = observation{state: StateUnknown, ok: true}, ""
	}

	// What the ladder answered before the runaway guard. Step 7 asks it as
	// well: a screen that says the prompt is gone is evidence whether or not
	// the guard has since rewritten the answer to a silent idle.
	ladderObs, ladderFrom := obs, from

	// 6. The runaway guard. A pane that is alive and silent for thirty seconds
	//    with no exact signal is idle, whatever is still painted on it: this is
	//    what makes "spins for ever" impossible. It only fires once the exact
	//    layer has really dropped out - not while its last answer is still
	//    being reused - so a five minute test suite that prints nothing stays
	//    busy on every harness that has one, and a file that is unreadable for
	//    a tick or two does not read as a turn ending.
	if from != sourceExact {
		if quiet := quiescence(pane, now); quiet.ok && quiet.state == StateIdle {
			obs, from = quiet, sourceQuiet
		}
	}
	if !fresh && tr.committed.State == StateBusy && !tr.lastExactAt.IsZero() &&
		now.Sub(tr.lastExactAt) >= BusyCeiling {
		// The exact source has been gone for two minutes. Its last answer is
		// not evidence any more, and the layers below decide until it answers
		// again.
		tr.dropped = true
	}

	// 7. Waiting is sticky against quiescence. A permission prompt is silent by
	//    nature and may sit for an hour while the user drives.
	if tr.committed.State == StateWaiting && obs.state != StateWaiting {
		// An exact answer and a recognised non-waiting screen both mean the
		// prompt is gone. The screen counts even when the guard has replaced
		// it: it was read this tick either way.
		released := from == sourceExact || from == sourceScreen
		if !released && ladderObs.state != StateWaiting {
			released = ladderFrom == sourceExact || ladderFrom == sourceScreen
		}
		if !released {
			// Otherwise only silence after an answer releases it.
			released = obs.state == StateIdle && from == sourceQuiet && tr.inputAt.After(tr.waitingAt)
		}
		if !released {
			obs = seen(StateWaiting, tr.committed.Note)
			from = tr.committed.Source
		}
	}

	a.commit(row.ID, tr, obs, from, now)
}

// exact reads the harness's own signal, and reuses the last one for a few
// ticks when it does not answer. The third result is whether the answer is
// this tick's rather than remembered, which is what the runaway guard turns on.
func (a *activity) exact(ctx context.Context, kind harnesses.Kind, tr *track, snap snapshot) (observation, string, bool) {
	src := a.sources[kind]
	if src == nil {
		return missing, "", false
	}
	if obs := src.Read(ctx, snap); obs.ok {
		tr.lastExact, tr.lastExactAt, tr.exactMiss, tr.dropped = obs, snap.now, 0, false
		return obs, sourceExact, true
	}
	tr.exactMiss++
	if !tr.dropped && tr.lastExact.ok && tr.exactMiss <= ExactMissTicks {
		return tr.lastExact, sourceExact, false
	}
	return missing, "", false
}

// scrape reads the pane, at most every ScrapeEvery per session, and answers
// with whatever the harness's own furniture says.
func (a *activity) scrape(ctx context.Context, kind harnesses.Kind, tr *track, snap snapshot) observation {
	if !scrapes(kind) {
		return missing
	}
	if !tr.scrapeAt.IsZero() && snap.now.Sub(tr.scrapeAt) < ScrapeEvery {
		return tr.lastScrape
	}
	tr.scrapeAt = snap.now
	screen, err := a.capture(ctx, snap.row.ID, 40)
	if err != nil {
		tr.lastScrape = missing
		return missing
	}
	tr.lastScrape = scrapeScreen(kind, screen)
	return tr.lastScrape
}

// quiescence is the one layer every harness has: all four TUIs repaint a
// spinner several times a second while they work, so thirty seconds without a
// byte is a fact about the program and not about the terminal.
func quiescence(pane paneLine, now time.Time) observation {
	if pane.activity <= 0 {
		return missing
	}
	since := now.Sub(time.Unix(pane.activity, 0))
	switch {
	case since < 0:
		// The pane's clock is ahead of ours. Nothing to say.
		return missing
	case since <= QuietBusy:
		return seen(StateBusy, "")
	case since >= HardQuiet:
		return seen(StateIdle, "")
	}
	return missing
}

// ------------------------------------------------------------- the committing

// commit debounces one observation into the committed state, and is the only
// place an unread mark is set.
func (a *activity) commit(id string, tr *track, obs observation, from string, now time.Time) {
	next := obs.state
	prev := tr.committed
	if next == prev.State {
		tr.pending, tr.pendingCount = "", 0
		if obs.note != prev.Note || from != prev.Source {
			tr.committed.Note, tr.committed.Source = obs.note, from
			// The source alone is hover-only detail; a note is what the row
			// says out loud, so only that is worth a frame.
			if obs.note != prev.Note {
				a.publish(id, tr)
			}
		}
		return
	}
	need := 1
	switch next {
	case StateIdle:
		// The anti-flicker between two tool calls.
		need = IdleSettle
	case StateUnknown:
		// A transient read failure must never blank a row.
		need = UnknownSettle
	}
	if tr.pending == next {
		tr.pendingCount++
	} else {
		tr.pending, tr.pendingCount = next, 1
	}
	if tr.pendingCount < need {
		return
	}
	tr.pending, tr.pendingCount = "", 0
	tr.committed = Activity{
		State: next, Unread: prev.Unread, Since: now.UnixMilli(), Source: from, Note: obs.note,
	}
	if next == StateWaiting {
		tr.waitingAt = now
	}
	// Work finished, or the agent started needing you. Unknown counts as "not
	// busy", so a pane that dies mid-turn also marks unread: something ended
	// and nobody saw it.
	if (prev.State == StateBusy && next != StateBusy) || (prev.State != StateWaiting && next == StateWaiting) {
		tr.committed.Unread = true
		a.unread[id] = now.UnixMilli()
		a.saveUnread()
	}
	a.publish(id, tr)
}

func (a *activity) clearUnread(id string, tr *track) {
	if _, ok := a.unread[id]; !ok && !tr.committed.Unread {
		return
	}
	delete(a.unread, id)
	tr.committed.Unread = false
	if tr.committed.Since == 0 {
		tr.committed.Since = a.now().UnixMilli()
	}
	a.saveUnread()
	a.publish(id, tr)
}

func (a *activity) trackOf(id string) *track {
	tr := a.tracks[id]
	if tr == nil {
		tr = &track{committed: Activity{State: StateUnknown, Unread: a.unread[id] != 0}}
		a.tracks[id] = tr
	}
	return tr
}

// publish records a committed change for the callback and the subscribers. It
// only queues: the callback runs outside the lock, because it ends in a
// WebSocket write and nothing that writes a socket may hold this.
func (a *activity) publish(id string, tr *track) {
	a.pending = append(a.pending, ActivityChange{SessionID: id, Activity: tr.committed})
}

func (a *activity) take() []ActivityChange {
	out := a.pending
	a.pending = nil
	return out
}

// fire delivers what publish queued. The subscriber sends happen under the
// lock and the callback outside it, and both halves of that are deliberate: a
// send cannot block, because the channels are buffered and the select has a
// default, so holding the lock across them costs nothing and is the whole of
// what makes an unsubscribe safe - the stop function closes the channel in the
// same locked section, so no send can ever land on a closed one. The callback
// ends in a WebSocket write, and nothing that writes a socket may hold this.
func (a *activity) fire(changes []ActivityChange) {
	if len(changes) == 0 {
		return
	}
	a.mu.Lock()
	for _, change := range changes {
		for _, ch := range a.subs {
			// A subscriber that has stopped reading loses changes rather than
			// stopping the tick.
			select {
			case ch <- change:
			default:
			}
		}
	}
	a.mu.Unlock()
	if a.m.cfg.OnActivity == nil {
		return
	}
	for _, change := range changes {
		a.m.cfg.OnActivity(change.SessionID, change.Activity)
	}
}

// ------------------------------------------------------------------ the unread

// load brings the unread marks back, dropping the ids that no longer exist.
func (a *activity) load() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loaded {
		return
	}
	a.loaded = true
	marks := map[string]int64{}
	if err := a.m.st.GetJSON(unreadKey, &marks); err != nil {
		return
	}
	rows, err := a.m.st.ListSessions(true)
	if err != nil {
		return
	}
	known := map[string]bool{}
	for i := range rows {
		known[rows[i].ID] = true
	}
	changed := false
	for id, at := range marks {
		if known[id] {
			a.unread[id] = at
			continue
		}
		changed = true
	}
	if changed {
		a.saveUnread()
	}
}

func (a *activity) saveUnread() {
	if err := a.m.st.SetJSON(unreadKey, a.unread); err != nil {
		a.m.logf("could not record the unread sessions: %v", err)
	}
}

// forget drops everything about one session, which is what deleting it does.
func (a *activity) forget(id string) {
	a.mu.Lock()
	delete(a.tracks, id)
	_, had := a.unread[id]
	delete(a.unread, id)
	if had {
		a.saveUnread()
	}
	a.forgetSources(id)
	a.mu.Unlock()
}

func (a *activity) forgetSources(id string) {
	for _, src := range a.sources {
		if f, ok := src.(forgetter); ok {
			f.forget(id)
		}
	}
}

// stop closes everything the sources hold open, which is one event stream per
// running OpenCode session.
func (a *activity) stop() {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		ids := make([]string, 0, len(a.tracks))
		for id := range a.tracks {
			ids = append(ids, id)
		}
		a.mu.Unlock()
		for _, id := range ids {
			a.mu.Lock()
			a.forgetSources(id)
			a.mu.Unlock()
		}
	})
}

// planFor is the launch plan next to a session, remembered between ticks: the
// shell's own name and Codex's project directory both come out of it, and
// reading a file per session per second for that would be a fork's worth of
// work for a value that never changes.
func (m *Manager) planFor(id string) harnesses.LaunchPlan {
	m.mu.Lock()
	if plan, ok := m.plans[id]; ok {
		m.mu.Unlock()
		return plan
	}
	m.mu.Unlock()
	plan, err := m.readPlan(id)
	if err != nil {
		// A plan that cannot be read is not cached: it may be a session that
		// is still being written.
		return harnesses.LaunchPlan{}
	}
	m.mu.Lock()
	if m.plans == nil {
		m.plans = map[string]harnesses.LaunchPlan{}
	}
	m.plans[id] = plan
	m.mu.Unlock()
	return plan
}

func (m *Manager) forgetPlan(id string) {
	m.mu.Lock()
	delete(m.plans, id)
	m.mu.Unlock()
}

// -------------------------------------------------------------- Shell, layer 1

// shellSource answers from the pane's own foreground process.
//
// The children check is not optional: `bash script.sh`, `sh -c …`, a
// `#!/bin/bash` script and a nested interactive shell all report `bash` as the
// foreground command and would otherwise read as idle for their whole run. A
// nested idle shell reads as busy under this rule; that is the cheaper mistake.
type shellSource struct{}

// shellNames is what "this is the shell itself" means when the launch plan
// cannot be read.
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
	"ksh": true, "ash": true, "busybox": true, "login": true,
}

func (shellSource) Read(_ context.Context, s snapshot) observation {
	cmd := strings.TrimSpace(s.pane.command)
	if cmd == "" {
		return missing
	}
	if !isTheShell(cmd, s.plan) {
		return seen(StateBusy, cmd)
	}
	if hasChild(s.pane.pid) {
		return seen(StateBusy, cmd)
	}
	return seen(StateIdle, "")
}

func isTheShell(cmd string, plan harnesses.LaunchPlan) bool {
	if len(plan.Argv) > 0 {
		return cmd == filepath.Base(plan.Argv[0])
	}
	return shellNames[strings.TrimPrefix(cmd, "-")]
}

// hasChild reports whether a process has a child, which is the difference
// between a shell at its prompt and a shell running a script.
func hasChild(pid int) bool {
	if pid <= 0 {
		return false
	}
	if has, ok := procChildren(pid); ok {
		return has
	}
	return psChildren(pid)
}

// procChildren reads Linux's own answer. The boolean is whether it could be
// read at all: the file needs CONFIG_PROC_CHILDREN, and it does not exist on
// the BSDs or macOS.
func procChildren(pid int) (bool, bool) {
	tasks, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	if err != nil {
		return false, false
	}
	answered := false
	for _, task := range tasks {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", task.Name(), "children"))
		if err != nil {
			continue
		}
		answered = true
		if strings.TrimSpace(string(raw)) != "" {
			return true, true
		}
	}
	return false, answered
}

// psChildren is the answer everywhere else: one fork, and only when /proc
// could not say.
func psChildren(pid int) bool {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=").Output()
	if err != nil {
		return false
	}
	want := strconv.Itoa(pid)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == want {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------- Claude, layer 1

// claudeStatusSlack is how much older than its own process a status file may
// compute and still be believed. See readClaudeStatus.
const claudeStatusSlack = 2 * time.Second

// claudeStatus is the file Claude Code keeps for its own process.
type claudeStatus struct {
	PID        int    `json:"pid"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
}

// claudeSource reads `<home>/.claude/sessions/<pid>.json`, which Claude Code
// rewrites on every state change and which is the only signal of the four that
// carries "waiting" directly.
type claudeSource struct {
	mu    sync.Mutex
	cache map[string]claudePID
}

// claudePID is a resolved process and the pane it was resolved under. The pane
// pid is kept so that a relaunch invalidates the answer without anybody having
// to say so.
type claudePID struct {
	pid     int
	panePID int
	misses  int
}

func newClaudeSource() *claudeSource { return &claudeSource{cache: map[string]claudePID{}} }

func (c *claudeSource) forget(id string) {
	c.mu.Lock()
	delete(c.cache, id)
	c.mu.Unlock()
}

func (c *claudeSource) Read(_ context.Context, s snapshot) observation {
	home := claudeHome(s.plan)
	if home == "" || s.pane.pid <= 0 {
		return missing
	}
	dir := filepath.Join(home, ".claude", "sessions")

	c.mu.Lock()
	cached := c.cache[s.row.ID]
	if cached.panePID != s.pane.pid {
		cached = claudePID{panePID: s.pane.pid}
	}
	c.mu.Unlock()

	// The pane's own pid first: tmux execs our argv directly, so it is the
	// harness's own process.
	pid := cached.pid
	status, ok := readClaudeStatus(dir, s.pane.pid)
	if ok {
		pid = s.pane.pid
	} else if pid > 0 && pid != s.pane.pid {
		status, ok = readClaudeStatus(dir, pid)
	}
	if !ok {
		// A wrapper, or a Claude that has not written its file yet. Walk the
		// pane's descendants once and take the newest that has one.
		if found, st, hit := findClaudeStatus(dir, s.pane.pid); hit {
			pid, status, ok = found, st, true
		}
	}

	c.mu.Lock()
	if ok {
		c.cache[s.row.ID] = claudePID{pid: pid, panePID: s.pane.pid}
	} else {
		cached.misses++
		if cached.misses > ExactMissTicks {
			// The file has been gone for three ticks; the pid it named is not
			// this pane's Claude any more.
			cached = claudePID{panePID: s.pane.pid}
		}
		c.cache[s.row.ID] = cached
	}
	c.mu.Unlock()
	if !ok {
		return missing
	}

	switch strings.ToLower(strings.TrimSpace(status.Status)) {
	case "busy":
		return seen(StateBusy, "")
	case "idle":
		return seen(StateIdle, "")
	case "waiting":
		note := strings.TrimSpace(status.WaitingFor)
		if note == "" {
			note = "waiting for you"
		}
		return seen(StateWaiting, note)
	}
	return missing
}

// claudeHome is the home directory the pane was given, and ours only as a
// fallback: a session launched with a HOME of its own writes its status file
// there.
func claudeHome(plan harnesses.LaunchPlan) string {
	if h := strings.TrimSpace(plan.Env["HOME"]); h != "" {
		return h
	}
	return strings.TrimSpace(os.Getenv("HOME"))
}

// readClaudeStatus reads one status file and refuses it unless it is really
// this process's.
//
// `~/.claude/sessions/` is never garbage collected, so after a reboot a
// recycled pid can point at a dead session's file that still says busy. The
// file counts only when its own `.pid` matches and its mtime is not older than
// the process it names.
func readClaudeStatus(dir string, pid int) (claudeStatus, bool) {
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	info, err := os.Stat(path)
	if err != nil {
		return claudeStatus{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return claudeStatus{}, false
	}
	var status claudeStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return claudeStatus{}, false
	}
	if status.PID != 0 && status.PID != pid {
		return claudeStatus{}, false
	}
	// The slack is for the clock, not the file: a process start is btime (one
	// second of granularity, and it moves under NTP) plus the kernel's ticks,
	// so a file written in the first moment after exec can compute as older
	// than the process that wrote it. Two seconds is more than that error and
	// far less than a recycled pid's file age.
	if started, ok := processStart(pid); ok && info.ModTime().Add(claudeStatusSlack).Before(started) {
		return claudeStatus{}, false
	}
	return status, true
}

// findClaudeStatus walks a pane's descendants looking for one that has written
// a session file, and takes the newest. It is bounded because a pane's process
// tree is not: three levels and sixty-four processes is more than any launcher
// wrapper needs and less than a fork bomb costs.
func findClaudeStatus(dir string, panePID int) (int, claudeStatus, bool) {
	var (
		best     claudeStatus
		bestPID  int
		bestTime time.Time
		found    bool
	)
	for _, pid := range descendants(panePID, 3, 64) {
		path := filepath.Join(dir, strconv.Itoa(pid)+".json")
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		status, ok := readClaudeStatus(dir, pid)
		if !ok {
			continue
		}
		if !found || info.ModTime().After(bestTime) {
			best, bestPID, bestTime, found = status, pid, info.ModTime(), true
		}
	}
	return bestPID, best, found
}

// descendants is a pane's process tree, breadth first and bounded.
func descendants(pid, depth, limit int) []int {
	children := childrenOf(pid)
	out := []int{}
	level := children
	for d := 0; d < depth && len(level) > 0 && len(out) < limit; d++ {
		var next []int
		for _, child := range level {
			if len(out) >= limit {
				break
			}
			out = append(out, child)
			next = append(next, childrenOf(child)...)
		}
		level = next
	}
	return out
}

func childrenOf(pid int) []int {
	if pid <= 0 {
		return nil
	}
	tasks, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	if err == nil {
		var out []int
		for _, task := range tasks {
			raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", task.Name(), "children"))
			if err != nil {
				continue
			}
			for _, field := range strings.Fields(string(raw)) {
				if child, err := strconv.Atoi(field); err == nil {
					out = append(out, child)
				}
			}
		}
		return out
	}
	return psChildrenOf(pid)
}

func psChildrenOf(pid int) []int {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	want := strconv.Itoa(pid)
	var kids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == want {
			if child, err := strconv.Atoi(fields[0]); err == nil {
				kids = append(kids, child)
			}
		}
	}
	return kids
}

// processStart is when a process began, which is what tells a live pid from a
// recycled one.
func processStart(pid int) (time.Time, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err == nil {
		// Field 22 is the start time in clock ticks since boot. The command
		// name in field 2 may contain spaces and parentheses, so everything up
		// to the last ')' is skipped first.
		s := string(raw)
		at := strings.LastIndex(s, ")")
		if at < 0 {
			return time.Time{}, false
		}
		fields := strings.Fields(s[at+1:])
		// fields[0] is field 3 (state), so field 22 is fields[19].
		if len(fields) < 20 {
			return time.Time{}, false
		}
		ticks, err := strconv.ParseInt(fields[19], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		boot, ok := bootTime()
		if !ok {
			return time.Time{}, false
		}
		return boot.Add(time.Duration(ticks) * time.Second / time.Duration(clockTicks)), true
	}
	// Everywhere else, ask ps.
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	started, err := time.ParseInLocation("Mon Jan  2 15:04:05 2006", strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return started, true
}

// clockTicks is USER_HZ, which is 100 on every Linux this runs on. It is a
// constant rather than a sysconf call because being a second out on a boot
// time only matters to a comparison that is already measured in minutes.
const clockTicks = 100

var (
	bootOnce sync.Once
	bootAt   time.Time
	bootOK   bool
)

func bootTime() (time.Time, bool) {
	bootOnce.Do(func() {
		raw, err := os.ReadFile("/proc/stat")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			rest, ok := strings.CutPrefix(line, "btime ")
			if !ok {
				continue
			}
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return
			}
			bootAt, bootOK = time.Unix(secs, 0), true
			return
		}
	})
	return bootAt, bootOK
}

// -------------------------------------------------------------- Codex, layer 1

// braille is the spinner Codex prefixes its title with while it works.
func braille(r rune) bool { return r >= 0x2800 && r <= 0x28FF }

// codexSource reads the terminal title, which is the only one of the four
// titles that encodes activity.
//
// An unrecognised title is *unavailable* and never idle: tmux reports the
// hostname before the first OSC title, and "non-empty means idle" would assert
// idle from it.
type codexSource struct{}

func (codexSource) Read(_ context.Context, s snapshot) observation {
	title := strings.TrimSpace(s.pane.title)
	if title == "" {
		return missing
	}
	if strings.Contains(title, "Action Required") {
		return seen(StateWaiting, "approval required")
	}
	for _, r := range title {
		if braille(r) {
			return seen(StateBusy, "")
		}
		break
	}
	if s.plan.Cwd != "" && title == filepath.Base(s.plan.Cwd) {
		return seen(StateIdle, "")
	}
	return missing
}

// ------------------------------------------------------------------- the screen

// scrapes reports whether a harness has a screen worth reading. The shell has
// no furniture of its own, so its ladder is L1 then quiescence.
func scrapes(kind harnesses.Kind) bool { return kind != harnesses.KindShell }

// The furniture each TUI paints, verified against the real programs. The busy
// literals are deliberately not shared: OpenCode's bottom bar says `esc
// interrupt`, with no "to", where Claude's and Codex's say `esc to interrupt`.
var (
	claudeBusy    = regexp.MustCompile(`esc to interrupt`)
	claudeWaiting = regexp.MustCompile(`Do you want to proceed\?`)
	claudeIdle    = regexp.MustCompile(`(?m)^\s*❯\s*$|⏵⏵ `)

	codexBusy    = regexp.MustCompile(`esc to interrupt`)
	codexWaiting = regexp.MustCompile(`Press enter to confirm or esc to cancel|` +
		`Would you like to make the following edits\?|Press enter to continue`)
	codexIdle = regexp.MustCompile(`Ask Codex to do anything`)

	openCodeBusy = regexp.MustCompile(`esc interrupt`)
	openCodeIdle = regexp.MustCompile(`ctrl\+p commands`)
)

// scrapeScreen is the third layer: what the harness has painted, read the way
// a person reads it. Waiting is looked for first, because an approval dialog
// can sit on a screen whose bottom bar still carries a busy hint.
func scrapeScreen(kind harnesses.Kind, screen string) observation {
	switch kind {
	case harnesses.KindClaude:
		switch {
		case claudeWaiting.MatchString(screen):
			return seen(StateWaiting, "permission prompt")
		case claudeBusy.MatchString(screen):
			return seen(StateBusy, "")
		case claudeIdle.MatchString(screen):
			return seen(StateIdle, "")
		}
	case harnesses.KindCodex:
		switch {
		case codexWaiting.MatchString(screen):
			return seen(StateWaiting, "approval required")
		case codexBusy.MatchString(screen):
			return seen(StateBusy, "")
		case codexIdle.MatchString(screen):
			return seen(StateIdle, "")
		}
	case harnesses.KindOpenCode:
		// Never infer waiting from an OpenCode screen: no verified pattern
		// exists, and the event stream carries permissions exactly.
		switch {
		case openCodeBusy.MatchString(screen):
			return seen(StateBusy, "")
		case openCodeIdle.MatchString(screen):
			return seen(StateIdle, "")
		}
	}
	return missing
}

// ---------------------------------------------------------- OpenCode, layer 1

// OpenCode reconnect budget. A dead port must not be dialled every second for
// the life of the session, and a stream that drops once must come back at once.
const (
	openCodeBackoffMin = 1 * time.Second
	openCodeBackoffMax = 15 * time.Second
	// openCodeStreamShort is how long a stream has to last to count as a
	// connection worth reconnecting to at once.
	openCodeStreamShort = 5 * time.Second
)

// openCodeSource is one long lived event stream per running OpenCode session.
//
// The port is per Socrates session, so several OpenCode instances never share
// a stream. Within one stream a set of busy session ids is kept, because a
// parent session and its sub-agents all emit session.status and an `idle` for
// one of them must not clear another's `busy`.
type openCodeSource struct {
	m *Manager

	mu       sync.Mutex
	watchers map[string]*openCodeWatcher
}

func newOpenCodeSource(m *Manager) *openCodeSource {
	return &openCodeSource{m: m, watchers: map[string]*openCodeWatcher{}}
}

func (o *openCodeSource) Read(_ context.Context, s snapshot) observation {
	o.mu.Lock()
	w := o.watchers[s.row.ID]
	if w == nil {
		access, ok := o.m.OpenCodeAccessOf(s.row.ID)
		if !ok {
			o.mu.Unlock()
			return missing
		}
		w = startOpenCodeWatcher(access, workdirOf(s))
		o.watchers[s.row.ID] = w
	}
	o.mu.Unlock()
	return w.answer()
}

func (o *openCodeSource) forget(id string) {
	o.mu.Lock()
	w := o.watchers[id]
	delete(o.watchers, id)
	o.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

func workdirOf(s snapshot) string {
	if s.plan.Cwd != "" {
		return s.plan.Cwd
	}
	return s.row.Workdir
}

// openCodeWatcher holds what one session's server has said.
type openCodeWatcher struct {
	access  harnesses.ServerAccess
	workdir string
	cancel  context.CancelFunc

	mu      sync.Mutex
	ok      bool
	busy    map[string]bool
	waiting map[string]bool
}

func startOpenCodeWatcher(access harnesses.ServerAccess, workdir string) *openCodeWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &openCodeWatcher{
		access: access, workdir: workdir, cancel: cancel,
		busy: map[string]bool{}, waiting: map[string]bool{},
	}
	go w.run(ctx)
	return w
}

func (w *openCodeWatcher) stop() { w.cancel() }

// answer is the layer's view of the stream: waiting beats busy beats idle, and
// nothing at all until the server has answered once.
func (w *openCodeWatcher) answer() observation {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ok {
		return missing
	}
	if len(w.waiting) > 0 {
		return seen(StateWaiting, "permission prompt")
	}
	if len(w.busy) > 0 {
		return seen(StateBusy, "")
	}
	return seen(StateIdle, "")
}

// run keeps one event stream open, and polls the status endpoint while it is
// down: the stream carries deltas only, so a poll is both the seed for the
// next connection and the answer while there is none.
func (w *openCodeWatcher) run(ctx context.Context) {
	backoff := openCodeBackoffMin
	for ctx.Err() == nil {
		w.poll(ctx)
		opened := time.Now()
		err := w.stream(ctx)
		if err == nil {
			backoff = openCodeBackoffMin
			// A stream that ends cleanly the moment it opens is a proxy or a
			// half-started server answering 200 and hanging up, not a
			// connection: reconnecting at once would dial it in a hot loop, so
			// a short one waits out the floor first.
			if time.Since(opened) >= openCodeStreamShort {
				continue
			}
		}
		deadline := time.Now().Add(backoff)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(ActivityInterval):
			}
			w.poll(ctx)
		}
		if err != nil {
			if backoff *= 2; backoff > openCodeBackoffMax {
				backoff = openCodeBackoffMax
			}
		}
	}
}

// poll is the fallback and the seed both: GET /session/status answers with the
// sessions that are working, and an idle session is simply absent from the map.
func (w *openCodeWatcher) poll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/session/status?directory=%s",
		w.access.Port, neturl.QueryEscape(w.workdir))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.SetBasicAuth(w.access.Username, w.access.Password)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.mu.Lock()
		w.ok = false
		w.mu.Unlock()
		return
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		w.mu.Lock()
		w.ok = false
		w.mu.Unlock()
		return
	}
	var status map[string]struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&status); err != nil {
		return
	}
	busy := map[string]bool{}
	for id, entry := range status {
		if entry.Type == "busy" || entry.Type == "retry" {
			busy[id] = true
		}
	}
	w.mu.Lock()
	w.busy, w.ok = busy, true
	w.mu.Unlock()
}

// openCodeEvent is one `data:` line of the stream. The permission events were
// characterised in an earlier research pass with a `data` envelope and the
// status events with a `properties` one, so both are accepted.
type openCodeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Data       json.RawMessage `json:"data"`
}

type openCodeStatus struct {
	SessionID string `json:"sessionID"`
	Status    struct {
		Type string `json:"type"`
	} `json:"status"`
}

type openCodePermission struct {
	ID        string `json:"id"`
	RequestID string `json:"requestID"`
	SessionID string `json:"sessionID"`
}

// stream reads GET /event until it ends. It returns nil for a stream that ran
// and stopped, and an error for one that could not be opened.
func (w *openCodeWatcher) stream(ctx context.Context) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/event", w.access.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(w.access.Username, w.access.Password)
	req.Header.Set("Accept", "text/event-stream")
	// No client timeout: this connection is meant to stay open for the life of
	// the session, and the heartbeat is the server's own.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the OpenCode event stream answered %d", res.StatusCode)
	}
	reader := bufio.NewReader(io.LimitReader(res.Body, 1<<30))
	for {
		line, err := reader.ReadString('\n')
		if payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			w.event(strings.TrimSpace(payload))
		}
		if err != nil {
			return nil
		}
	}
}

func (w *openCodeWatcher) event(payload string) {
	var event openCodeEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return
	}
	body := event.Properties
	if len(body) == 0 {
		body = event.Data
	}
	switch event.Type {
	case "session.status":
		var status openCodeStatus
		if err := json.Unmarshal(body, &status); err != nil || status.SessionID == "" {
			return
		}
		w.mu.Lock()
		switch status.Status.Type {
		case "busy", "retry":
			w.busy[status.SessionID] = true
		default:
			delete(w.busy, status.SessionID)
		}
		w.ok = true
		w.mu.Unlock()
	case "permission.v2.asked":
		var perm openCodePermission
		if err := json.Unmarshal(body, &perm); err != nil {
			return
		}
		id := perm.ID
		if id == "" {
			id = perm.RequestID
		}
		if id == "" {
			return
		}
		w.mu.Lock()
		w.waiting[id] = true
		w.ok = true
		w.mu.Unlock()
	case "permission.v2.replied":
		var perm openCodePermission
		if err := json.Unmarshal(body, &perm); err != nil {
			return
		}
		id := perm.RequestID
		if id == "" {
			id = perm.ID
		}
		w.mu.Lock()
		if id == "" {
			// A reply nobody can match is still a reply: the prompt is gone.
			w.waiting = map[string]bool{}
		} else {
			delete(w.waiting, id)
		}
		w.ok = true
		w.mu.Unlock()
	}
}
