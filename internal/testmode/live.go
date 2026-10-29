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

// peekLocked reads what a running attempt's tools wrote since it was last
// read, at most once every liveEvery. The new lines are taken into the
// attempt exactly as a call of its agent takes them (ingest): appended to
// its actions, with the offset moved on, while the number of the last
// action a judgement took (Judged) stays. So whatever a peek read is still
// among the actions since the previous judgement, which the next judgement
// takes as its evidence.
func (r *Runner) peekLocked(rs *runState, a *Attempt) {
	if rs.peeked == nil {
		rs.peeked = map[string]time.Time{}
	}
	now := r.now()
	if last, ok := rs.peeked[a.ID]; ok && now.Sub(last) < liveEvery {
		return
	}
	rs.peeked[a.ID] = now
	if n, _ := ingest(a, r.layout.EvidenceDir(a), rs.red); n > 0 {
		r.saveAttemptLocked(a)
	}
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
