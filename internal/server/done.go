package server

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// DoneRetention is how long a session is kept once it is done. Its terminal
// is closed the moment it is marked, so what the retention keeps is the row,
// the scrollback and the conversation id - enough to take it out of done
// again and carry on where it stopped.
const DoneRetention = 24 * time.Hour

// sweepEvery is how often done sessions are looked at. A deletion a minute
// late is nobody's problem, and a sweep is one read of the session table.
const sweepEvery = time.Minute

// park closes the terminal of a session that is done and tells every tab
// watching it that it is gone.
//
// An answer being typed into it is stopped first, as a delete and a restart
// stop it: the terminal it types into is about to close, and a dialog that
// went on saying "Choosing Postgres…" for a model step would be telling the
// person about a terminal that is not there any more.
func (s *Server) park(ctx context.Context, id string) error {
	s.answers.stop(id, reasonDone)
	if err := s.manager.Park(ctx, id); err != nil {
		return err
	}
	s.hub.endSession(id, "this session is done, so its terminal was closed")
	s.events.poke()
	return nil
}

// deleteSession removes a session and everything Socrates kept for it except
// the working directory. It is what the Delete button and the sweep share.
func (s *Server) deleteSession(ctx context.Context, id string) error {
	// A title run writing into a row that is being removed is nothing good.
	s.titles.forget(id)
	s.push.forget(id)
	// An answer being typed into a pane that is being torn down is the one
	// thing the operator could do that nobody asked for.
	s.answers.forget(id)
	if err := s.manager.Delete(ctx, id); err != nil {
		s.answers.revive(id)
		return err
	}
	harnesses.ForgetOpenCodeAccess(id)
	// After the delete rather than before it, so that a turn the detector
	// commits in between cannot start a digest of a session that is going.
	s.digests.forget(id)
	s.events.poke()
	return nil
}

// startSweep runs the sweep once straight away and then every sweepEvery,
// until ctx ends.
func (s *Server) startSweep(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(sweepEvery)
		defer ticker.Stop()
		for {
			s.sweep(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// sweep deletes the done sessions whose retention has run out, and closes
// the terminal of any done session that still has one. The second is what an
// upgrade from the archive needs - archived sessions kept running, and they
// are done now - and it is also the retry for a park that failed half way.
// Both are read from the row, so neither is lost to a restart.
func (s *Server) sweep(ctx context.Context, now time.Time) {
	rows, err := s.store.ListSessions()
	if err != nil {
		log.Printf("sweep: could not read the sessions: %v", err)
		return
	}
	for _, row := range rows {
		if row.Status != store.StatusDone {
			continue
		}
		one, cancel := context.WithTimeout(ctx, 30*time.Second)
		if now.UnixMilli() >= row.StatusAt+DoneRetention.Milliseconds() {
			if err := s.deleteSession(one, row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				log.Printf("sweep: could not delete done session %s: %v", row.ID, err)
			} else {
				s.hub.endSession(row.ID, "this session was deleted")
			}
		} else if row.State == store.StateRunning || row.State == store.StateStarting {
			if err := s.park(one, row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				log.Printf("sweep: could not close the terminal of done session %s: %v", row.ID, err)
			}
		}
		cancel()
	}
}
