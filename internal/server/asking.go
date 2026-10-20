package server

import (
	"log"
	"slices"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The asking set: every session that is waiting for an answer from the
// person, as Home's Questions card counts them.
//
// It is the number on the app icon, and the list a phone closes the stale
// questions in its notification centre by. Both are worked out on the page
// too (home.js cardsOf and questionQueue), and the server's count is the one
// that reaches a phone whose page is not running - so it is the page's rule
// exactly, line for line, read from the same rows, the same committed
// activity and the same digests the page is sent. A badge that says three
// where Home shows two is a badge nobody trusts.

// askingStates is every row state that has a live pane to be asking from:
// home.js LIVE_STATES. "resuming" is a state only a browser draws, kept here
// so that the two lists read the same.
var askingStates = map[string]bool{
	store.StateRunning:  true,
	store.StateStarting: true,
	"resuming":          true,
}

// askingSet is the ids of every session that is asking, sorted.
//
// It reads the digester and the detector, each under its own lock, so it is
// never called with digester.mu or pusher.mu held: the digester calls into
// the pusher with its lock held, and a pusher that called back would wait on
// the very lock that is waiting on it. The pusher calls it on the goroutine
// that sends, after every lock has been let go.
//
// It answers nil when the rows cannot be read: a set that could not be
// counted is no set, and a badge cleared because a query failed would say
// nothing is waiting when something may be.
func (s *Server) askingSet() []string {
	rows, err := s.store.ListSessions()
	if err != nil {
		log.Printf("push: could not count the questions: %v", err)
		return nil
	}
	seen, prose := s.digests.askingView()
	live := s.manager.Activities()
	out := []string{}
	for i := range rows {
		row := &rows[i]
		a, ok := committedActivity(row.ID, live, seen)
		if ok && asking(row, a, prose[row.ID]) {
			out = append(out, row.ID)
		}
	}
	slices.Sort(out)
	return out
}

// committedActivity is a session's last committed activity: the later of the
// detector's own picture and the one the digester was last told of, by Seq.
// The detector commits before it tells anybody, so its picture is never the
// older one - except for a session it has no track for, which a test that
// feeds the callback by hand is. On a tie the digester's is taken, which is
// the same commit.
func committedActivity(id string, live, seen map[string]termux.Activity) (termux.Activity, bool) {
	l, hasLive := live[id]
	s, hasSeen := seen[id]
	switch {
	case hasLive && hasSeen:
		if l.Seq > s.Seq {
			return l, true
		}
		return s, true
	case hasLive:
		return l, true
	case hasSeen:
		return s, true
	}
	return termux.Activity{}, false
}

// asking is cardsOf's "questions" for one session with an activity.
//
// Not done (a status the page does not know reads as in progress, which is
// not done either); not a tool, which has no turn to ask in; not prepared;
// live; and either on a prompt - a question and a permission prompt are both
// the waiting state - or idle at the end of an answer that asked something in
// prose. A session filed background is counted, as Home counts it: it is
// only kept from chiming and from notifying (DEVIATIONS.md).
func asking(row *store.Session, a termux.Activity, inProse bool) bool {
	if row.Status == store.StatusDone {
		return false
	}
	if row.Harness == config.HarnessShell || row.Harness == config.HarnessVSCode {
		return false
	}
	if row.State == store.StatePrestart || !askingStates[row.State] {
		return false
	}
	switch a.State {
	case termux.StateWaiting:
		return true
	case termux.StateIdle:
		return inProse
	}
	return false
}

// askingView is what the asking set reads from the digester, in one look:
// the last committed activity of every session it was told of, and every
// session whose digest holds a question asked in prose (home.js textQuestion:
// any phase, as long as the question is there - the digester takes it out the
// moment the session leaves the state it was read in).
func (d *digester) askingView() (map[string]termux.Activity, map[string]bool) {
	seen := map[string]termux.Activity{}
	prose := map[string]bool{}
	if d == nil {
		return seen, prose
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, a := range d.seen {
		seen[id] = a
	}
	for id, e := range d.entries {
		if e.published && e.d.Question != nil && e.d.Question.Source == questionText {
			prose[id] = true
		}
	}
	return seen, prose
}
