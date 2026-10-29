package testmode

import (
	"time"
)

// A running cell, live (TEST-MODE.md, "Revision: the reworked Tests page"):
// the step it is on, what its tools did last and its latest screenshot,
// read from the evidence its tools write while it runs.

// liveEvery is how often the evidence of a running attempt is read for its
// live view at most.
var liveEvery = 2 * time.Second

// Live is how a running attempt stands: the first step not judged yet (0
// once every step is), how many steps it has, the last action's summary,
// its latest screenshot and when that action was.
type Live struct {
	Step     int    `json:"step"`
	Steps    int    `json:"steps"`
	Activity string `json:"activity,omitempty"`
	Shot     string `json:"shot,omitempty"`
	At       int64  `json:"at,omitempty"`
}

// A peek reads what a running attempt's tools wrote since it was last read,
// for its live view and its attempt's view. The new lines are taken into
// the attempt exactly as a call of its agent takes them (ingest): appended
// to its actions, with the offset moved on, while the number of the last
// action a judgement took (Judged) stays. So whatever a peek read is still
// among the actions since the previous judgement, which the next judgement
// takes as its evidence.
//
// The page reads the cells every few seconds while a run is active, and
// every call of an agent takes the runner's lock: the file is read without
// it. peeksLocked notes what to read (the attempt, its offset and the
// redactor), readPeeks reads it, and applyPeeksLocked takes it in under the
// lock again, only where nothing was read into the attempt meanwhile.
type peek struct {
	rs  *runState
	a   *Attempt
	dir string
	red *Redactor
	off int64
	got actionsRead
	err error
}

// peeksLocked is the peeks of the running attempts among list: each at
// most once every liveEvery unless always, none of a run whose secrets are
// not read (unredactable).
func (r *Runner) peeksLocked(rs *runState, list []*Attempt, always bool) []*peek {
	if rs.unredactable() || rs.deleting {
		return nil
	}
	if rs.peeked == nil {
		rs.peeked = map[string]time.Time{}
	}
	now := r.now()
	var out []*peek
	for _, a := range list {
		if a == nil || a.State != AttemptRunning {
			continue
		}
		if last, ok := rs.peeked[a.ID]; ok && !always && now.Sub(last) < liveEvery {
			continue
		}
		rs.peeked[a.ID] = now
		out = append(out, &peek{rs: rs, a: a, dir: r.layout.EvidenceDir(a), red: rs.red, off: a.Offset,
			got: actionsRead{have: len(a.Actions)}})
	}
	return out
}

// readPeeks reads the files of the peeks: without the runner's lock.
func readPeeks(list []*peek) {
	for _, p := range list {
		p.got, p.err = readActions(p.dir, p.off, p.got.have, p.red)
	}
}

// applyPeeksLocked takes what the peeks read into their attempts: where the
// attempt still runs in its run, nothing was read into it meanwhile (its
// offset and actions are as they were) and its run's redactor is the one the
// read used.
func (r *Runner) applyPeeksLocked(list []*peek) {
	for _, p := range list {
		a, rs := p.a, p.rs
		if p.err != nil || len(p.got.actions) == 0 && p.got.to == p.off {
			continue
		}
		if r.runs[a.Run] != rs || rs.attempts[a.ID] != a || a.State != AttemptRunning || rs.unredactable() || rs.red != p.red ||
			a.Offset != p.off || len(a.Actions) != p.got.have {
			continue
		}
		p.got.apply(a)
		if len(p.got.actions) > 0 {
			r.saveAttemptLocked(a)
		}
	}
}

// peekLocked runs the peeks pick names (with the lock held): the lock is let
// go while their files are read and held again when it returns, so that what
// the caller read under it before has to be read again.
func (r *Runner) peekLocked(pick func() []*peek) {
	list := pick()
	if len(list) == 0 {
		return
	}
	r.mu.Unlock()
	readPeeks(list)
	r.mu.Lock()
	r.applyPeeksLocked(list)
}

// liveOf is a running attempt's live view from what was read of it.
func liveOf(a *Attempt) *Live {
	l := &Live{Steps: len(a.Steps), At: a.StartedAt}
	for _, s := range a.Steps {
		if s.Verdict == "" {
			l.Step = s.N
			break
		}
	}
	if n := len(a.Actions); n > 0 {
		last := a.Actions[n-1]
		l.Activity = last.Summary
		if t, err := time.Parse(time.RFC3339Nano, last.At); err == nil {
			l.At = t.UnixMilli()
		}
	}
	l.Shot = lastShot(a)
	return l
}

// stepShots is a step's screenshots: its actions', then the images attached
// to it.
func stepShots(a *Attempt, s *Step) []string {
	var out []string
	for _, n := range s.Actions {
		if n >= 1 && n <= len(a.Actions) && a.Actions[n-1].Screenshot != "" {
			out = append(out, a.Actions[n-1].Screenshot)
		}
	}
	for _, name := range s.Attach {
		if isImage(name) {
			out = append(out, name)
		}
	}
	return out
}

// lastShot is an attempt's latest screenshot: its last action's that has
// one, else the last image attached to a step.
func lastShot(a *Attempt) string {
	for i := len(a.Actions) - 1; i >= 0; i-- {
		if a.Actions[i].Screenshot != "" {
			return a.Actions[i].Screenshot
		}
	}
	for i := len(a.Steps) - 1; i >= 0; i-- {
		for j := len(a.Steps[i].Attach) - 1; j >= 0; j-- {
			if isImage(a.Steps[i].Attach[j]) {
				return a.Steps[i].Attach[j]
			}
		}
	}
	return ""
}

// bestShot is the screenshot that shows an attempt best: the first of its
// first judged step that did not pass (as it counts), else its last.
func bestShot(a *Attempt) string {
	for i := range a.Steps {
		s := &a.Steps[i]
		if v := s.StepVerdict(); v == "" || passed(v) {
			continue
		}
		if shots := stepShots(a, s); len(shots) > 0 {
			return shots[0]
		}
		break
	}
	return lastShot(a)
}
