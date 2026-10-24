package server

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/webpush"
)

// The scheduler starts prepared chats at the time they were given.
//
// It is a loop rather than a timer per session because the schedule lives in
// the rows, and the rows can change under it from anywhere - a PATCH, a
// delete, another device. Every pass reads them again and starts
// what is due; between passes it sleeps until the next due time, and never
// longer than scheduleTick, so a schedule written by something that did not
// poke it is still seen within seconds.
//
// What it starts, it starts through startPrepared, which is what the Start
// button calls: the same start, the same draft as the first message, the same
// account choice.
// The one thing it does first is clear the schedule, in the same step that
// claims the start, so a start that fails is reported once and then left for
// the person rather than retried every ten seconds for ever.
//
// A schedule that fell due while the server was down is due when it comes
// back, and is started by the first pass, once.

// scheduleTick is the longest the scheduler sleeps between two passes.
const scheduleTick = 10 * time.Second

// scheduleMinWait keeps a pass that could not start a due session - it lost
// the claim to a change made at that very moment - from turning into a busy
// loop on a schedule that is still overdue.
const scheduleMinWait = 500 * time.Millisecond

type scheduler struct {
	srv  *Server
	now  func() time.Time
	wake chan struct{}
}

func newScheduler(s *Server) *scheduler {
	return &scheduler{srv: s, now: time.Now, wake: make(chan struct{}, 1)}
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
			sc.runDue(ctx, sc.now())
			wait := scheduleTick
			if next := sc.nextDue(); next > 0 {
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

// runDue starts every prepared session whose time is at or before now, one
// after the other, and reports how many it claimed.
func (sc *scheduler) runDue(ctx context.Context, now time.Time) (claimed int) {
	rows, err := sc.srv.store.ListSessions()
	if err != nil {
		log.Printf("scheduled starts: could not read the sessions: %v", err)
		return 0
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return claimed
		}
		if row.State != store.StatePrestart || row.StartAt <= 0 || row.StartAt > now.UnixMilli() {
			continue
		}
		if sc.startOne(ctx, row) {
			claimed++
		}
	}
	return claimed
}

// startOne starts one due session and tells every subscribed phone how it
// went. It reports whether it owned the start.
func (sc *scheduler) startOne(ctx context.Context, row store.Session) bool {
	one, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	next, _, err := sc.srv.startPrepared(one, row.ID, true)
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
