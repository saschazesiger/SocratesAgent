package server

import (
	"context"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// The scheduler starts prepared sessions at the time they were given.
//
// It is a loop rather than a timer per session because the schedule lives in
// the rows, and the rows can change under it from anywhere - a PATCH, a
// delete, a restore from the bucket. Every pass reads them again and starts
// what is due; between passes it sleeps until the next due time, and never
// longer than scheduleTick, so a schedule written by something that did not
// poke it is still seen within seconds.
//
// What it starts, it starts through startPrepared, which is what the Start
// button calls: the same launch, the same draft, the same profile choice.
// The one thing it does first is clear the schedule, in the same step that
// claims the start, so a start that fails is reported once and then left for
// the person rather than retried every ten seconds for ever.
//
// A schedule that fell due while the server was down is due when it comes
// back, and is started by the first pass, once. A server that does not hold
// the state it shares starts nothing by itself: a mirror fenced off its
// bucket by another Socrates (that other Socrates is the one the schedule
// belongs to now), and a degraded one that has not reached its bucket in
// this run, or not for a whole lease TTL (whether another machine took the
// bucket meanwhile is what it cannot know; its overdue schedules start once
// it holds the lease again). See SetAutoStartGate.

// scheduleTick is the longest the scheduler sleeps between two passes.
const scheduleTick = 10 * time.Second

// scheduleMinWait keeps a pass that could not start a due session - it lost
// the claim to a change made at that very moment - from turning into a busy
// loop on a schedule that is still overdue. A shut gate never gets here: the
// loop then waits the whole scheduleTick (see start).
const scheduleMinWait = 500 * time.Millisecond

type scheduler struct {
	srv  *Server
	now  func() time.Time
	wake chan struct{}
	// gate says whether this server may start anything by itself right now;
	// nil error is yes. It is set from outside, by whoever knows about the
	// bucket.
	gate atomic.Pointer[func() error]
	// gateShut remembers that the gate refused, so that it is logged once
	// rather than on every pass.
	gateShut atomic.Bool
}

func newScheduler(s *Server) *scheduler {
	return &scheduler{srv: s, now: time.Now, wake: make(chan struct{}, 1)}
}

// SetAutoStartGate installs the check every scheduled start has to pass
// first. A non-nil error means "not now": the session stays scheduled and is
// tried again on a later pass, and nothing is claimed or cleared. The mirror
// sets it (Mirror.AutoStartAllowed) to refuse while it is fenced or does not
// hold its lease; nil removes the gate.
func (s *Server) SetAutoStartGate(gate func() error) {
	if gate == nil {
		s.sched.gate.Store(nil)
		return
	}
	s.sched.gate.Store(&gate)
	s.sched.poke()
}

// poke makes the loop look again now: a schedule was just set or moved.
func (sc *scheduler) poke() {
	if sc == nil {
		return
	}
	select {
	case sc.wake <- struct{}{}:
	default:
	}
}

// start runs the loop until ctx ends. The first pass runs at once, which is
// what starts the schedules that fell due while the server was down.
func (sc *scheduler) start(ctx context.Context) {
	go func() {
		for {
			_, blocked := sc.pass(ctx, sc.now())
			wait := scheduleTick
			// While the gate is shut an overdue schedule stays overdue, and
			// waking for it every scheduleMinWait would be a busy loop for
			// as long as the gate stays shut - for a fenced mirror, the rest
			// of the run. The tick notices a gate that opens again, and
			// SetAutoStartGate pokes.
			if next := sc.nextDue(); next > 0 && !blocked {
				until := time.UnixMilli(next).Sub(sc.now())
				wait = min(wait, max(until, scheduleMinWait))
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-sc.wake:
				timer.Stop()
			}
		}
	}()
}

// blocked is the gate's answer, logged when it changes.
func (sc *scheduler) blocked() error {
	gate := sc.gate.Load()
	if gate == nil {
		sc.gateShut.Store(false)
		return nil
	}
	err := (*gate)()
	if err == nil {
		if sc.gateShut.Swap(false) {
			log.Print("scheduled starts: allowed again")
		}
		return nil
	}
	if !sc.gateShut.Swap(true) {
		log.Printf("scheduled starts: held back: %v", err)
	}
	return err
}

// runDue starts every prepared session whose time is at or before now, one
// after the other, and reports how many it claimed.
func (sc *scheduler) runDue(ctx context.Context, now time.Time) int {
	claimed, _ := sc.pass(ctx, now)
	return claimed
}

// pass is runDue that also says whether the gate kept it from looking.
func (sc *scheduler) pass(ctx context.Context, now time.Time) (claimed int, blocked bool) {
	if sc.blocked() != nil {
		return 0, true
	}
	rows, err := sc.srv.store.ListSessions()
	if err != nil {
		log.Printf("scheduled starts: could not read the sessions: %v", err)
		return 0, false
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return claimed, false
		}
		if row.State != store.StatePrestart || row.StartAt <= 0 || row.StartAt > now.UnixMilli() {
			continue
		}
		if sc.startOne(ctx, row) {
			claimed++
		}
	}
	return claimed, false
}

// startOne starts one due session and tells every subscribed phone how it
// went. It reports whether it owned the start.
func (sc *scheduler) startOne(ctx context.Context, row store.Session) bool {
	one, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	next, _, err := sc.srv.startPrepared(one, row.ID, 0, 0, true)
	if errors.Is(err, errNotDue) {
		return false
	}
	// Started or put back, the row has moved, and nobody pressed anything
	// that would have told the other pages.
	sc.srv.events.poke()
	title := row.Title
	if next != nil {
		title = next.Title
	}
	if err != nil {
		log.Printf("scheduled start of session %s failed: %v", row.ID, err)
		sc.announce(row.ID, "Scheduled start failed: "+title, err.Error(), "high")
		return true
	}
	if next != nil && next.State != store.StatePrestart {
		log.Printf("scheduled start of session %s: started", row.ID)
		sc.announce(row.ID, "Started: "+title, "The scheduled start ran.", "normal")
	}
	return true
}

// nextDue is the earliest schedule of any prepared session, or zero.
func (sc *scheduler) nextDue() int64 {
	rows, err := sc.srv.store.ListSessions()
	if err != nil {
		return 0
	}
	var next int64
	for _, row := range rows {
		if row.State == store.StatePrestart && row.StartAt > 0 && (next == 0 || row.StartAt < next) {
			next = row.StartAt
		}
	}
	return next
}

// announce is a Web Push about one session, through the same subscriptions
// and the same service worker as "Finished" and "Needs an answer". The person
// who scheduled a start is, by definition, not watching for it. It is on the
// session's topic like the others: a phone that was off is told the latest
// about the session - the question its first turn asked, say - rather than
// that it started.
func (sc *scheduler) announce(id, title, body, urgency string) {
	note := pushNote{Title: title, Body: body, ID: id, Kind: pushStarted}
	go sc.srv.push.broadcast(note, webpush.Message{TTL: pushTTL, Urgency: urgency, Topic: pushTopic(id)})
}
