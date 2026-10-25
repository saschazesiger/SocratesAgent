package server

import (
	"log"
	"slices"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The asking set: every session that is waiting for an answer from the
// person, as Home's Questions card counts them.
//
// It is the number on the app icon, and the list a phone closes the stale
// questions in its notification centre by. Both are worked out on the page
// too (home.js cardsOf and questionQueue), and the server's count is the one
// that reaches a phone whose page is not running - so it is the page's rule
// exactly, read from the same rows and the same readings the page is sent. A
// badge that says three where Home shows two is a badge nobody trusts.
//
// Only a chat asks: its question is an open ask in its journal that it waits
// on (REBUILD.md §5.4). A Shell and an editor take no turns and ask nothing.

// askingStates is every row state that can be asking: home.js LIVE_STATES.
// "resuming" is a state only a browser draws, kept here so that the two
// lists read the same.
var askingStates = map[string]bool{
	store.StateRunning:  true,
	store.StateStarting: true,
	"resuming":          true,
}

// askingSet is the ids of every session that is asking, sorted.
//
// It reads the chats' briefs under their own lock, so it is never called with
// digester.mu or pusher.mu held. The pusher calls it on the goroutine that
// sends, after every lock has been let go.
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
	out := []string{}
	for i := range rows {
		row := &rows[i]
		if row.IsChat() && askable(row) && s.chats.waiting(row.ID) {
			out = append(out, row.ID)
		}
	}
	slices.Sort(out)
	return out
}

// askable is whether a session can be asking at all: not done, and live. A session filed background is counted, as Home counts it: it is
// only kept from chiming and from notifying (DEVIATIONS.md).
func askable(row *store.Session) bool {
	if row.Status == store.StatusDone {
		return false
	}
	return askingStates[row.State]
}
