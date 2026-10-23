package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The terminal engine card of the dashboard: whether tmux is on this machine
// and whether sessions are possible at all.
//
// It only reports. scripts/install.sh installs tmux, so a machine without it is
// an installation that was not finished, and running the script again is what
// puts it right.

// detectEvery is how long a detection stands. The card is refreshed on load,
// on every wake and whenever it is opened, and each detection runs `tmux -V`.
// A few seconds of memory turns that into one subprocess per visit.
const detectEvery = 5 * time.Second

// tmuxAdmin is the card's state. Its zero value is a server that has detected
// nothing yet, which is why it needs no constructor and no line in New.
type tmuxAdmin struct {
	detector termux.Detector

	mu       sync.Mutex
	cached   termux.Report
	cachedAt time.Time
}

// detect answers the card's question, from memory when the answer is seconds
// old.
func (t *tmuxAdmin) detect(ctx context.Context) termux.Report {
	t.mu.Lock()
	if !t.cachedAt.IsZero() && time.Since(t.cachedAt) < detectEvery {
		report := t.cached
		t.mu.Unlock()
		return report
	}
	t.mu.Unlock()

	report := t.detector.Detect(ctx)

	t.mu.Lock()
	t.cached, t.cachedAt = report, time.Now()
	t.mu.Unlock()
	return report
}

// handleTmux is the terminal engine card: what tmux is here, and whether the
// engine it drives can actually run a session.
func (s *Server) handleTmux(w http.ResponseWriter, r *http.Request) {
	report := s.tmuxAdmin.detect(r.Context())
	// The binary can be perfect and the engine still unusable - a data
	// directory too long for a Unix socket is the case that put `ok: true` on
	// this card while every session died on a bind. The card answers for the
	// engine, so it answers with whatever stops it running.
	ok, reason := report.OK, report.Reason
	if err := s.manager.Available(); err != nil && ok {
		ok, reason = false, err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": report.Installed,
		"path":      report.Path,
		"version":   report.Version,
		"ok":        ok,
		"min":       report.Min,
		"reason":    reason,
	})
}
