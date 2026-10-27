package agenthost

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The watch on a running turn (AGENT-HOST.md §6, "Quiet turns"). The host
// knows when the agent last gave a sign of life - any event or delta of its
// adapter - and checks the running turn every watchEvery:
//
//   - a turn quiet for quietAfter while it waits on nobody is said to be
//     quiet (a quiet event with the last sign of life), and heard from again
//     at the agent's next sign;
//   - a turn quiet beyond its adapter's QuietLimit (agent.QuietLimiter) is
//     stopped, as Stop does it;
//   - a Stop - the person's or the watch's - that has not ended the turn
//     within stopWait ends the CLI: the turn is closed as interrupted, and a
//     new CLI resumes the conversation and takes up what waited. Nothing can
//     keep a chat busy for ever.
const (
	watchEvery = 5 * time.Second
	quietAfter = 5 * time.Minute
	stopWait   = 30 * time.Second
)

// watch is the host's record of the running turn's life, and its clock
// (Spec.Watch).
type watch struct {
	quietAfter, stopWait, quietLimit time.Duration

	// life is the agent's last sign of life.
	life time.Time
	// quiet is the turn the host has said is quiet, until the agent is
	// heard from again.
	quiet string
	// limited is the turn the watch stopped for being quiet too long.
	limited string
	// stopTurn and stopAt are the turn a Stop was asked for, and when.
	stopTurn string
	stopAt   time.Time
}

// newWatch is the watch with the spec's clock.
func newWatch(spec *WatchSpec) watch {
	w := watch{life: time.Now(), quietAfter: quietAfter, stopWait: stopWait}
	if spec != nil {
		ms := func(v int64, d *time.Duration) {
			if v > 0 {
				*d = time.Duration(v) * time.Millisecond
			}
		}
		ms(spec.QuietAfterMS, &w.quietAfter)
		ms(spec.StopWaitMS, &w.stopWait)
		ms(spec.QuietLimitMS, &w.quietLimit)
	}
	return w
}

// alive is a sign of life from the agent. A turn that was said to be quiet
// is heard from again, and the event saying so goes before the agent's.
func (h *host) alive() []agent.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.watch.life = time.Now()
	if h.watch.quiet == "" {
		return nil
	}
	turn := h.watch.quiet
	h.watch.quiet = ""
	return []agent.Event{{Type: agent.TypeQuiet, TurnID: turn}}
}

// stopAsked notes that the running turn was asked to stop now, so that the
// watch ends the CLI when the turn has not ended within stopWait.
func (h *host) stopAsked() {
	turn := ""
	h.rec.View(func(p *agent.Projection) {
		if p.Turn != nil {
			turn = p.Turn.ID
		}
	})
	if turn == "" {
		return
	}
	h.mu.Lock()
	if h.watch.stopTurn != turn {
		h.watch.stopTurn, h.watch.stopAt = turn, time.Now()
	}
	h.mu.Unlock()
}

// liveAdapter is the adapter, for the watch, which does not take the command
// lock: a command stuck in a CLI that does not answer holds it.
func (h *host) liveAdapter() agent.Adapter {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live
}

// setAdapter makes ad the host's adapter (nil: none), under the command
// lock.
func (h *host) setAdapter(ad agent.Adapter) {
	h.adapter = ad
	h.mu.Lock()
	h.live = ad
	h.mu.Unlock()
}

// watchLoop checks the running turn until the host ends.
func (h *host) watchLoop() {
	h.mu.Lock()
	every := min(watchEvery, h.watch.quietAfter/4, h.watch.stopWait/4)
	h.mu.Unlock()
	tick := time.NewTicker(max(every, 10*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-tick.C:
		}
		h.check(time.Now())
	}
}

// check is one look at the running turn at now.
func (h *host) check(now time.Time) {
	if h.isEnding() {
		return
	}
	turn, waiting := "", false
	h.rec.View(func(p *agent.Projection) {
		if p.Turn == nil {
			return
		}
		turn = p.Turn.ID
		waiting = p.Summary().Activity == agent.ActivityWaiting
	})
	h.mu.Lock()
	w := &h.watch
	if turn == "" {
		// Between turns nothing is watched, and a Stop asked for a turn
		// that ended has done its work.
		w.stopTurn, w.quiet = "", ""
		h.mu.Unlock()
		return
	}
	if waiting {
		// A question for the person: the agent is rightly silent until it
		// is answered, and its time to be heard from starts then.
		w.life = now
	}
	quiet := now.Sub(w.life)
	sayQuiet := quiet >= w.quietAfter && w.quiet != turn
	if sayQuiet {
		w.quiet = turn
	}
	stuck := w.stopTurn == turn && now.Sub(w.stopAt) >= w.stopWait
	if stuck {
		w.stopTurn = ""
	}
	since := w.life
	ad := h.live
	limit := w.quietLimit
	if l, ok := ad.(agent.QuietLimiter); ok && limit == 0 {
		limit = l.QuietLimit()
	}
	wait := w.stopWait
	tooLong := !stuck && limit > 0 && quiet >= limit && w.limited != turn
	if tooLong {
		w.limited = turn
		w.stopTurn, w.stopAt = turn, now
	}
	h.mu.Unlock()

	if sayQuiet {
		log.Printf("turn %s: nothing from the agent for %s", turn, quiet.Round(time.Second))
		h.record(agent.Event{Type: agent.TypeQuiet, TurnID: turn, Since: since.UnixMilli()})
	}
	switch {
	case stuck:
		go h.restartUnresponsive(turn, "The agent did not stop within "+seconds(wait)+
			" of being asked, so it was ended and started again on the same conversation.")
	case tooLong:
		log.Printf("turn %s: quiet for %s, beyond the agent's limit: stopping it", turn, quiet.Round(time.Second))
		h.record(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeUnresponsive,
			Text: "The agent gave no sign of life for " + minutes(quiet) + ", so its turn was stopped."})
		go h.interruptQuiet(turn, wait)
	}
}

// interruptQuiet stops a turn that was quiet too long, as a command: one
// at a time with the others. It is not journaled as a command - nobody asked
// for it, so the turn's end is news as any other - and a CLI that does not
// act on it is started again (check, stopWait).
func (h *host) interruptQuiet(turn string, wait time.Duration) {
	h.cmdMu.Lock()
	defer h.cmdMu.Unlock()
	running := false
	h.rec.View(func(p *agent.Projection) { running = p.Turn != nil && p.Turn.ID == turn })
	if !running || h.adapter == nil || h.isEnding() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := h.adapter.Interrupt(ctx); err != nil {
		log.Printf("interrupt of a quiet turn: %v", err)
	}
}

// restartUnresponsive replaces a CLI that does not act on a Stop: it is
// killed, the turn closed as interrupted with why, what it left running
// closed, and a new CLI resumes the conversation on the same account and is
// handed what the old one had not taken up. A CLI that does not come back
// ends the host; the next message starts a new one.
func (h *host) restartUnresponsive(turn, why string) {
	log.Printf("turn %s: the agent did not stop when asked; starting it again", turn)
	// Killed first: a command that waits on the CLI holds the command lock,
	// and lets go of it once the CLI is gone.
	if ad := h.liveAdapter(); ad != nil {
		_ = ad.Stop()
	}
	h.cmdMu.Lock()
	defer h.cmdMu.Unlock()
	if h.isEnding() || h.adapter == nil {
		return
	}
	h.setAdapter(nil)
	h.cliPID.Store(0)
	var events []agent.Event
	h.rec.View(func(p *agent.Projection) {
		if p.Turn != nil {
			events = append(events, agent.Event{Type: agent.TypeTurnEnded, TurnID: p.Turn.ID,
				Outcome: agent.OutcomeInterrupted, Text: why})
		}
		left, _ := leftovers(p)
		events = append(events, left...)
	})
	events = append(events,
		agent.Event{Type: agent.TypeNotice, Code: agent.NoticeUnresponsive, Text: why},
		agent.Event{Type: agent.TypeProcess, State: agent.ProcessExited, Detail: "unresponsive"})
	if _, err := h.rec.EmitDurable(events...); err != nil {
		log.Printf("journal: %v", err)
	}
	h.mu.Lock()
	h.watch = newWatch(h.spec.Watch)
	account, env := h.account, h.env
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), startWait)
	defer cancel()
	if err := h.startAdapter(ctx, account, env); err != nil {
		log.Printf("the agent did not start again: %v", err)
		if h.beginEnding() {
			_, _ = h.rec.EmitDurable(agent.Event{Type: agent.TypeProcess, State: agent.ProcessFailed, Detail: err.Error()})
			time.AfterFunc(failLinger, h.finish)
		}
		return
	}
	h.resendOwed(ctx)
}

// minutes is a quiet stretch in words: "30 min".
func minutes(d time.Duration) string {
	if d < time.Minute {
		return seconds(d)
	}
	return strconv.Itoa(int(d/time.Minute)) + " min"
}

// seconds is a short wait in words: "30 s".
func seconds(d time.Duration) string {
	return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + " s"
}
