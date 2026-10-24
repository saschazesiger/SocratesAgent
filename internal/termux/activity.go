package termux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The activity detector: what every session is doing, in one picture the
// browser, the pusher and Home all read.
//
// A chat says it itself - its journal has every turn's start and end and every
// question (ReportActivity) - so the detector is its bookkeeping: the unread
// mark, the row moving, the filing. A terminal says nothing, and is read once
// a second: a Shell from its foreground process, VS Code from whether it
// prints. The layers that read the agents' own screens and side channels went
// with the agents' terminals (REBUILD.md §7).

// State is what a session is doing, as far as anything outside this package is
// concerned. It is not the store's `state`: a session can be `running` and any
// of these four.
type State string

const (
	// StateUnknown is no usable evidence yet, and is where every session
	// starts and where a dead pane ends.
	StateUnknown State = "unknown"
	// StateBusy is a session working: a chat's turn, or a Shell running a
	// program.
	StateBusy State = "busy"
	// StateIdle is a session waiting for a new instruction.
	StateIdle State = "idle"
	// StateWaiting is a chat that needs the person: a question, or a
	// permission it asks for. Only a chat's journal says it; a terminal
	// never does.
	StateWaiting State = "waiting"
	// StateTasks is a chat whose turn has ended while something it started
	// is still running: a background shell, a monitor, a background
	// sub-agent. It is not finished - the agent takes the turn up again the
	// moment that work reports back - and it is not working either, so it is
	// neither the spinner nor the quiet of a session that is done.
	StateTasks State = "tasks"
)

// working is whether a state is one in which the work somebody asked for is
// still going on, in the foreground or behind it. Leaving it is what a turn
// that finished is: the unread mark, the chime and the row moving up all wait
// for it, so a turn that ends with a shell still running announces itself once,
// when the shell is done and the agent has said its last word, and not twice.
func working(s State) bool { return s == StateBusy || s == StateTasks }

// Activity is one session's answer, as the browser receives it.
//
// State lives only in memory, like sizeOwner: a terminal's is re-derived from
// its live pane every second and a chat's is reported by its journal, so a
// persisted copy could only ever be stale. Unread is the one field that is
// written down, because "this finished while you were not looking" is exactly
// the fact a restart must not lose.
type Activity struct {
	State  State  `json:"state"`
	Unread bool   `json:"unread"`
	Since  int64  `json:"since"`          // unix ms of the last committed change
	Source string `json:"source"`         // "exact"|"quiet"|"pane" - hover-only detail
	Note   string `json:"note,omitempty"` // e.g. the program a Shell runs - hover-only
	// Seq orders every picture of every session's activity: it moves on each
	// time a change is published, whether that is a new state, a note or the
	// unread mark. A browser hears the same session from several places at
	// once - the event stream, a terminal socket, a list read over HTTP - and
	// since, which only moves with the state, cannot tell which of two
	// pictures of the same commit is the later one. It starts from the clock
	// (microseconds when the process started), so a seq from before a restart
	// is never above one after it, and stays below 2^53, the largest whole
	// number a browser reads exactly. Zero, and left out, for a session the
	// detector has published nothing about.
	Seq int64 `json:"seq,omitempty"`
	// Asked is whether anybody had typed at the session when this change was
	// committed (track.asked). It is for the server's own subscribers - the
	// first busy that was asked for is the first prompt being taken up - and
	// never goes to a browser.
	Asked bool `json:"-"`
}

// Where an answer came from, which the page shows only on hover.
const (
	sourceExact = "exact"
	sourceQuiet = "quiet"
	sourcePane  = "pane"
)

// The detector's whole clock, in one place.
//
// The settle counts are anti-flicker and nothing else: busy has to appear at
// once because the spinner is the only feedback a typed command gets, and
// idle has to wait two ticks because a script between two programs is briefly
// at its prompt and a sidebar that blinks on every command is worse than one
// that is a second late.
const (
	// ActivityInterval is one tick: one tmux command for every session.
	ActivityInterval = 1 * time.Second
	// IdleSettle and UnknownSettle are consecutive ticks before those two
	// states are committed.
	IdleSettle    = 2
	UnknownSettle = 3
	// ExactMissTicks is how many ticks an exact answer is reused after its
	// source stops answering.
	ExactMissTicks = 3
	// HardQuiet and QuietBusy are the two ends of output quiescence: no bytes
	// at all for HardQuiet is idle, bytes within QuietBusy is busy, and
	// anything between is no answer.
	HardQuiet = 30 * time.Second
	QuietBusy = 2 * time.Second
	// SortWithin is how long the detector may take to see a session left
	// unsorted by the upgrade that introduced statuses working, before it is
	// filed as pending.
	SortWithin = 30 * time.Second
)

// unreadKey is where the unread marks live in the store's key/value table. A
// map of session id to the moment it was marked, so that a row that no longer
// exists can be dropped on the way in.
const unreadKey = "activity.unread"

// sinceKey is where the moment each session entered its committed state
// lives, so that a restart of the server does not restart every clock: a
// question that has been waiting for an hour is not "just now", and the
// oldest question is still the oldest. It describes a pane on this machine,
// and changes on every commit, so it never goes to the bucket.
const sinceKey = "activity.since"

// sinceMark is one session's entry under sinceKey.
type sinceMark struct {
	State State `json:"state"`
	Since int64 `json:"since"`
}

// ActivityChange is one committed change, queued under the detector's lock and
// handed to Config.OnActivity outside it.
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
}

// The command comes last, so that SplitN hands it back whole whatever a
// program called itself. Every field before it is pipe-free by construction.
const activityPaneFormat = "#{session_name}|#{pane_dead}|#{pane_pid}|#{window_activity}|#{pane_current_command}"

func parseActivityPanes(out string) map[string]paneLine {
	panes := map[string]paneLine{}
	for _, line := range Lines(out) {
		f := strings.SplitN(line, "|", 5)
		if len(f) < 5 {
			continue
		}
		p := paneLine{session: f[0], dead: f[1] == "1", command: f[4]}
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

// --------------------------------------------------------------- the tracker

// track is the detector's memory of one session. Only the tick and
// ReportActivity write it, under the activity lock.
type track struct {
	committed Activity
	running   bool
	// status is the row's status as the last tick read it, which is what
	// decides whether starting to work files the session as in progress.
	status string

	// The candidate state and how many consecutive ticks have proposed it.
	pending      State
	pendingCount int

	// The last exact answer and how many ticks have gone without one.
	lastExact observation
	exactMiss int

	// inputAt is when a keystroke or a message last reached the session,
	// which is what track.asked turns on.
	inputAt time.Time

	// committedOnce is whether this process has committed a state for the
	// track. The first commit of a session that was in the same state
	// before the restart keeps the moment it entered it (sinceKey).
	committedOnce bool
}

// asked reports whether anybody has typed at this session since it was
// launched. Until somebody has, nothing the program does is news: it is
// starting up, and being told that a window finished opening is the one
// notification nobody wants.
//
// A relaunch clears it with the rest of the track (ResetActivity): a new
// process has a new start-up, and the turn that was in flight before it
// belongs to a program that is no longer there.
func (tr *track) asked() bool { return !tr.inputAt.IsZero() }

// activity is the whole detector: the per session tracks, the unread marks
// and the moments each session entered its state. It has a lock of its own
// rather than the Manager's, because the tick holds it across a read of /proc
// and the Manager's lock is on the path of every attach.
type activity struct {
	m *Manager

	mu      sync.Mutex
	tracks  map[string]*track
	unread  map[string]int64
	since   map[string]sinceMark
	loaded  bool
	pending []ActivityChange
	started bool
	// firstTick is when the detector first looked, which is where the time an
	// unsorted session gets to show that it is working starts.
	firstTick time.Time

	// shell is the Shell's exact layer (readShell), and now is the clock the
	// out-of-band writers - a keystroke, a chat's report - read. Both are
	// fields so that the table tests can script a sequence of ticks without a
	// process to look at and without waiting for real seconds to pass.
	shell func(snapshot) observation
	now   func() time.Time

	// seq is the last Activity.Seq handed out, under mu.
	seq int64
}

func newActivity(m *Manager) *activity {
	return &activity{
		m:      m,
		tracks: map[string]*track{},
		unread: map[string]int64{},
		since:  map[string]sinceMark{},
		shell:  readShell,
		now:    time.Now,
		seq:    time.Now().UnixMicro(),
	}
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

// NoteInput records a keystroke that reached the pane from a person. It is
// what tells the detector that this session has been asked something (see
// track.asked), and it clears the unread mark, because typing at a session is
// the whole of having seen it.
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
// and an old answer that is about to be wrong.
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
	// A new process has entered no state yet, whatever the old one was in.
	if _, ok := a.since[id]; ok {
		delete(a.since, id)
		a.saveSince()
	}
	if !fresh {
		a.publish(id, a.tracks[id])
	}
	// A relaunch may have been planned differently - a new argv, a different
	// working directory - and the Shell's layer reads the plan.
	a.m.forgetPlan(id)
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// ReportActivity commits a state the caller knows exactly, with no
// debounce: a chat's journal says when a turn starts and ends and when a
// question is open, so there is no screen to second-guess. asked is whether
// anybody has sent the chat anything, which the unread mark and the filing
// wait for as they do for a terminal (track.asked). Everything after the
// observation - the unread rule, the row moving, the filing back into
// progress, the frames - is the same as a terminal's.
func (m *Manager) ReportActivity(id string, state State, note string, asked bool) {
	row, err := m.st.GetSession(id)
	if err != nil {
		return
	}
	a := m.act
	a.mu.Lock()
	now := a.now()
	tr := a.trackOf(id)
	tr.running = true
	tr.status = row.Status
	if asked && tr.inputAt.IsZero() {
		tr.inputAt = now
	}
	obs := seen(state, note)
	if state == tr.committed.State {
		a.commit(id, tr, obs, sourceExact, now)
	} else {
		a.settle(id, tr, obs, sourceExact, now)
	}
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// -------------------------------------------------------------------- the tick

// tick asks tmux once what every session is doing and moves each row to the
// answer.
func (a *activity) tick(ctx context.Context, now time.Time) {
	if a.m.Available() != nil {
		return
	}
	rows, err := a.m.st.ListSessions()
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
	if a.firstTick.IsZero() {
		a.firstTick = now
	}
	seenRows := map[string]bool{}
	for i := range rows {
		row := rows[i]
		if row.IsChat() {
			// A chat has no pane. Its activity is reported by the chat's
			// journal (ReportActivity); the tick only has to keep its track.
			seenRows[row.ID] = true
			continue
		}
		if row.State == store.StatePrestart {
			// A prepared session has no pane and does no work. It is not
			// even filed by the detector: it is where the person put it.
			continue
		}
		running := row.State == store.StateRunning || row.State == store.StateStarting
		_, tracked := a.tracks[row.ID]
		if !running && !tracked {
			// Nothing was ever said about this row, and nothing has to be -
			// except where it is filed, if nobody has filed it yet: a session
			// that is not running is not working.
			if row.Status == store.StatusUnsorted {
				a.file(row.ID, store.StatusPending)
			}
			continue
		}
		seenRows[row.ID] = true
		pane, ok := panes[row.TmuxName]
		a.evaluate(ctx, row, pane, ok, running, now)
		if row.Status == store.StatusUnsorted {
			a.sort(row.ID, now)
		}
		// A session that has ended and settled on unknown is no longer the
		// detector's business; its unread mark is the store's.
		if tr := a.tracks[row.ID]; !running && tr.committed.State == StateUnknown && tr.pendingCount == 0 {
			delete(a.tracks, row.ID)
			delete(seenRows, row.ID)
		}
	}
	// A row that is gone from the store entirely - deleted while the tick was
	// running - leaves nothing behind.
	for id := range a.tracks {
		if !seenRows[id] {
			delete(a.tracks, id)
		}
	}
	changes := a.take()
	a.mu.Unlock()
	a.fire(changes)
}

// evaluate runs the ladder for one terminal. The activity lock is held: the
// Shell's layer reads /proc, and the alternative - copying every track in and
// out - buys nothing on a lock nothing else holds for long.
func (a *activity) evaluate(_ context.Context, row store.Session, pane paneLine, alive, running bool, now time.Time) {
	tr := a.trackOf(row.ID)
	tr.running = running
	tr.status = row.Status

	// 1. The pane is gone or dead. Nothing else is asked.
	if !alive || pane.dead {
		a.commit(row.ID, tr, observation{state: StateUnknown, ok: true}, sourcePane, now)
		return
	}

	// 2. The exact layer, with a short memory: a foreground command that
	//    blinks out for a tick or two is tmux between two reads, not the
	//    program ending.
	snap := snapshot{row: row, pane: pane, plan: a.m.planFor(row.ID), now: now}
	obs, from := a.exact(harnesses.Kind(row.Harness), tr, snap)

	// 3. Quiescence, for a terminal with no exact layer - VS Code - or one
	//    whose layer had nothing to say.
	if !obs.ok {
		if quiet := quiescence(pane, now); quiet.ok {
			obs, from = quiet, sourceQuiet
		}
	}
	// 4. Nothing at all.
	if !obs.ok {
		obs, from = observation{state: StateUnknown, ok: true}, ""
	}
	a.commit(row.ID, tr, obs, from, now)
}

// exact reads the terminal's own signal, and reuses the last one for a few
// ticks when it does not answer. Only a Shell has one: its foreground
// process.
func (a *activity) exact(kind harnesses.Kind, tr *track, snap snapshot) (observation, string) {
	if kind != harnesses.KindShell {
		return missing, ""
	}
	if obs := a.shell(snap); obs.ok {
		tr.lastExact, tr.exactMiss = obs, 0
		return obs, sourceExact
	}
	tr.exactMiss++
	if tr.lastExact.ok && tr.exactMiss <= ExactMissTicks {
		return tr.lastExact, sourceExact
	}
	return missing, ""
}

// quiescence is the layer every terminal has: a program that prints is
// working, and thirty seconds without a byte is a fact about the program and
// not about the terminal.
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
	case StateIdle, StateTasks:
		// The anti-flicker between two commands. A turn that ends with a
		// shell left running ends the same way one without it does.
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
	a.settle(id, tr, obs, from, now)
}

// settle commits a state change that needs no more evidence: the debounce of
// commit has passed, or the source is exact by construction, as a chat's
// journal is.
func (a *activity) settle(id string, tr *track, obs observation, from string, now time.Time) {
	next := obs.state
	prev := tr.committed
	tr.pending, tr.pendingCount = "", 0
	since := now.UnixMilli()
	// A session found in the state it was in when the last process stopped
	// has been in it since then, not since this tick: the restarted
	// detector is catching up, the session did nothing.
	if mark, ok := a.since[id]; ok && !tr.committedOnce && prev.State == StateUnknown &&
		mark.State == next && mark.Since > 0 && mark.Since <= since {
		since = mark.Since
	}
	tr.committedOnce = true
	tr.committed = Activity{
		State: next, Unread: prev.Unread, Since: since, Source: from, Note: obs.note,
	}
	if next != StateUnknown {
		a.since[id] = sinceMark{State: next, Since: since}
		a.saveSince()
	}
	// Work finished, or the agent started needing you. Unknown counts as "not
	// busy", so a pane that dies mid-turn also marks unread: something ended
	// and nobody saw it.
	//
	// Only for a session that has been asked something. Every program is busy
	// while it starts up and idle the moment it is ready, so without this a
	// session that has just been created ends its first second by marking
	// itself unread - and the browser, which chimes and notifies on this same
	// edge, announces that a window opened as work that finished.
	//
	// A turn that ends with something still running in the background has not
	// finished: the mark is set when that work is over too (see working).
	finished := working(prev.State) && !working(next)
	needsYou := prev.State != StateWaiting && next == StateWaiting
	if tr.asked() && (finished || needsYou) {
		tr.committed.Unread = true
		a.unread[id] = now.UnixMilli()
		a.saveUnread()
	}
	// A session that was put aside and has been given work again is in
	// progress again. Only work somebody asked for counts, for the reason the
	// unread mark has the same test: every program is busy while it starts,
	// so a resume or a restarted detector would otherwise file every pending
	// session as in progress. A done session has no terminal to work in, a
	// background one is a service that is meant to keep working where it is
	// filed, and an unsorted one is sorted below.
	if tr.asked() && next == StateBusy && prev.State != StateBusy &&
		(tr.status == store.StatusPending || tr.status == store.StatusReview) {
		a.file(id, store.StatusInProgress)
		tr.status = store.StatusInProgress
	}
	// The same three changes, plus starting to work, are what move a session
	// up the list. Anything else - a restarted detector finding a pane idle,
	// a read that failed for a tick - is the machine catching up, not the
	// session doing something, and must not shuffle the sidebar.
	if MovesRow(prev.State, next) {
		if err := a.m.st.NoteSessionActivity(id, now.UnixMilli()); err != nil {
			a.m.logf("could not record the activity of %s: %v", id, err)
		}
	}
	a.publish(id, tr)
}

// MovesRow says whether a committed change from one state to another is a
// status change in the sense the list orders by: the session started working,
// finished, or began needing an answer.
//
// It is the one rule, and the browser applies the same one to the frames it
// hears, so a row moves at the same moment on both sides of the socket. A
// change into idle from anything but busy is not on the list on purpose: it
// is what every running session reports when the detector comes up after a
// restart, and forty rows moving to "now" at once would be an order nobody
// chose - and for the same reason neither is a change into tasks from
// anything but busy. A change out of tasks is the end of work, as one out of
// busy is.
func MovesRow(prev, next State) bool {
	if prev == next {
		return false
	}
	return next == StateBusy || prev == StateBusy || prev == StateTasks || next == StateWaiting
}

// sort files a session the upgrade left unsorted, as soon as the detector
// can tell whether it is working: a session that is working is in progress,
// and one that is idle, waiting for an answer, or still undecided once
// SortWithin has passed is pending. The activity lock is held.
func (a *activity) sort(id string, now time.Time) {
	tr := a.tracks[id]
	if tr == nil {
		return
	}
	switch {
	case working(tr.committed.State):
		a.file(id, store.StatusInProgress)
	case tr.committed.State != StateUnknown, now.Sub(a.firstTick) >= SortWithin:
		a.file(id, store.StatusPending)
	}
}

// file writes a status the detector decided on. A session deleted in the
// meantime is not worth a line in the log.
func (a *activity) file(id, status string) {
	if tr := a.tracks[id]; tr != nil {
		tr.status = status
	}
	if err := a.m.st.SetSessionStatus(id, status); err != nil && !errors.Is(err, store.ErrNotFound) {
		a.m.logf("could not file session %s as %s: %v", id, status, err)
	}
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
//
// Every published picture gets the next seq, and so does the committed state
// that ActivityOf and Activities hand out from then on: a snapshot carries the
// seq of the last frame about it, and a frame published after the snapshot
// was taken carries a higher one.
func (a *activity) publish(id string, tr *track) {
	a.seq++
	tr.committed.Seq = a.seq
	change := ActivityChange{SessionID: id, Activity: tr.committed}
	change.Activity.Asked = tr.asked()
	a.pending = append(a.pending, change)
}

func (a *activity) take() []ActivityChange {
	out := a.pending
	a.pending = nil
	return out
}

// fire delivers what publish queued, outside the lock: the callback ends in a
// WebSocket write, and nothing that writes a socket may hold it.
func (a *activity) fire(changes []ActivityChange) {
	if len(changes) == 0 || a.m.cfg.OnActivity == nil {
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
	rows, err := a.m.st.ListSessions()
	if err != nil {
		return
	}
	known := map[string]bool{}
	for i := range rows {
		known[rows[i].ID] = true
	}
	marks := map[string]int64{}
	if err := a.m.st.GetJSON(unreadKey, &marks); err == nil {
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
	since := map[string]sinceMark{}
	if err := a.m.st.GetJSON(sinceKey, &since); err == nil {
		changed := false
		for id, mark := range since {
			if known[id] {
				a.since[id] = mark
				continue
			}
			changed = true
		}
		if changed {
			a.saveSince()
		}
	}
}

func (a *activity) saveUnread() {
	if err := a.m.st.SetJSON(unreadKey, a.unread); err != nil {
		a.m.logf("could not record the unread sessions: %v", err)
	}
}

func (a *activity) saveSince() {
	if err := a.m.st.SetJSON(sinceKey, a.since); err != nil {
		a.m.logf("could not record when the sessions entered their states: %v", err)
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
	if _, ok := a.since[id]; ok {
		delete(a.since, id)
		a.saveSince()
	}
	a.mu.Unlock()
}

// planFor is the launch plan next to a session, remembered between ticks: the
// shell's own name comes out of it, and reading a file per session per second
// for that would be a fork's worth of work for a value that never changes.
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

// ---------------------------------------------------------------- the Shell

// readShell answers from the pane's own foreground process.
//
// The children check is not optional: `bash script.sh`, `sh -c …`, a
// `#!/bin/bash` script and a nested interactive shell all report `bash` as the
// foreground command and would otherwise read as idle for their whole run. A
// nested idle shell reads as busy under this rule; that is the cheaper mistake.
// shellNames is what "this is the shell itself" means when the launch plan
// cannot be read.
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
	"ksh": true, "ash": true, "busybox": true, "login": true,
}

func readShell(s snapshot) observation {
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
