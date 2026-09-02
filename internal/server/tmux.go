package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The terminal engine card of the dashboard: whether tmux is on this machine,
// and the install that puts it there with its output visible while it runs.
//
// The install is deliberately not a request that blocks: a package manager
// takes minutes, a phone on a train does not hold a connection for minutes,
// and the person watching has to be able to reload the page and still find out
// what happened. So POST starts it, an event stream carries the lines, and the
// tail of the log plus the exit code are written to the database - which is
// what the card shows after a reload, and what makes the outcome survive the
// browser being closed entirely.

// installLogKey is where the last install's output and exit code are kept.
const installLogKey = "tmux_install_log"

// installLog is that record.
type installLog struct {
	Lines []string `json:"lines"`
	Exit  int      `json:"exit"`
	At    int64    `json:"at"`
}

// detectEvery is how long a detection stands. The card is refreshed on load,
// on every wake and whenever the output is opened, and each detection runs
// `tmux -V` and, on a machine that is not root, `sudo -n true`. A few seconds
// of memory turns that into one pair of subprocesses per visit without ever
// showing an answer from before something changed: every path that changes it
// - the install - clears the cache itself.
const detectEvery = 5 * time.Second

// tmuxEvent is one frame of the progress stream. The type field is what the
// browser switches on; the LiveStream in net.js reads unnamed SSE events, so
// the kind of frame travels in the payload rather than in an event name.
type tmuxEvent struct {
	Type string `json:"type"`
	Line string `json:"line,omitempty"`
	Exit int    `json:"exit,omitempty"`
	OK   bool   `json:"ok,omitempty"`
}

// tmuxAdmin is the state of the installer as the dashboard sees it. Its zero
// value is a server that has installed nothing yet, which is why it needs no
// constructor and no line in New.
type tmuxAdmin struct {
	installer termux.Installer

	mu       sync.Mutex
	running  bool
	lines    []string
	exit     int
	done     bool
	subs     map[chan tmuxEvent]struct{}
	cached   termux.Report
	cachedAt time.Time
}

// detect answers the card's question, from memory when the answer is seconds
// old. force is what the install uses to make sure the page never sees the
// machine as it was before it ran.
func (t *tmuxAdmin) detect(ctx context.Context, force bool) termux.Report {
	t.mu.Lock()
	if !force && !t.cachedAt.IsZero() && time.Since(t.cachedAt) < detectEvery {
		report := t.cached
		t.mu.Unlock()
		return report
	}
	t.mu.Unlock()

	report := t.installer.Detect(ctx)

	t.mu.Lock()
	t.cached, t.cachedAt = report, time.Now()
	t.mu.Unlock()
	return report
}

// forget throws the cached detection away, which is what has to happen the
// moment anything could have changed the machine.
func (t *tmuxAdmin) forget() {
	t.mu.Lock()
	t.cachedAt = time.Time{}
	t.mu.Unlock()
}

// subscribe joins the stream, and is handed everything the run has printed so
// far: a page opened halfway through an install must show the whole log, not
// only the rest of it.
func (t *tmuxAdmin) subscribe() (chan tmuxEvent, []tmuxEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ch := make(chan tmuxEvent, 256)
	if t.subs == nil {
		t.subs = map[chan tmuxEvent]struct{}{}
	}
	t.subs[ch] = struct{}{}
	backlog := make([]tmuxEvent, 0, len(t.lines)+1)
	for _, line := range t.lines {
		backlog = append(backlog, tmuxEvent{Type: "line", Line: line})
	}
	if t.done {
		backlog = append(backlog, tmuxEvent{Type: "done", Exit: t.exit, OK: t.exit == 0})
	}
	return ch, backlog
}

func (t *tmuxAdmin) unsubscribe(ch chan tmuxEvent) {
	t.mu.Lock()
	delete(t.subs, ch)
	t.mu.Unlock()
}

// publish fans one frame out. A subscriber whose buffer is full is one that
// stopped reading; the frame is dropped for it rather than held for everybody.
func (t *tmuxAdmin) publish(ev tmuxEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ev.Type == "line" {
		t.lines = append(t.lines, ev.Line)
		if over := len(t.lines) - termux.InstallLogLines; over > 0 {
			t.lines = append([]string(nil), t.lines[over:]...)
		}
	}
	for ch := range t.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// snapshot is the log as the status endpoint reports it.
func (t *tmuxAdmin) snapshot() (running bool, lines []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running, append([]string(nil), t.lines...)
}

// handleTmux is the terminal engine card: what tmux is here, what could
// install it, and what the last install printed.
func (s *Server) handleTmux(w http.ResponseWriter, r *http.Request) {
	report := s.tmuxAdmin.detect(r.Context(), false)
	running, lines := s.tmuxAdmin.snapshot()
	// The exit code of the last install is as much of the result as the log
	// is: a page reloaded after a failure has to be able to say that the
	// install failed, and when, rather than only show output.
	var stored installLog
	if err := s.store.GetJSON(installLogKey, &stored); err != nil {
		stored = installLog{Exit: -1}
	}
	if len(lines) == 0 {
		lines = stored.Lines
	}
	if lines == nil {
		lines = []string{}
	}
	// The binary can be perfect and the engine still unusable - a data
	// directory too long for a Unix socket is the case that put `ok: true` on
	// this card while every session died on a bind. The card answers for the
	// engine, so it answers with whatever stops it running.
	ok, reason := report.OK, report.Reason
	if err := s.manager.Available(); err != nil && ok {
		ok, reason = false, err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":   report.Installed,
		"path":        report.Path,
		"version":     report.Version,
		"ok":          ok,
		"min":         report.Min,
		"manager":     report.Manager,
		"privileged":  report.Privileged,
		"can_install": report.CanInstall,
		"command":     report.Command,
		"reason":      reason,
		"installing":  running,
		"log":         lines,
		"last_exit":   stored.Exit,
		"last_at":     stored.At,
	})
}

// handleTmuxInstall starts the package manager and answers straight away. The
// output goes to the event stream and to the database, never to this response:
// a browser that gives up on a five-minute request must not be able to cancel
// an install that is halfway through changing the machine.
func (s *Server) handleTmuxInstall(w http.ResponseWriter, r *http.Request) {
	t := &s.tmuxAdmin
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		writeError(w, http.StatusConflict, termux.ErrInstalling.Error())
		return
	}
	t.running, t.done, t.exit, t.lines = true, false, 0, nil
	t.mu.Unlock()

	go func() {
		// The install outlives the request on purpose, so it gets a context of
		// its own; the five-minute ceiling lives inside Install.
		ctx := context.Background()
		exit, err := t.installer.Install(ctx, func(line string) {
			t.publish(tmuxEvent{Type: "line", Line: line})
		})
		if err != nil {
			t.publish(tmuxEvent{Type: "line", Line: err.Error()})
			if exit == 0 {
				exit = -1
			}
		}
		t.forget()
		// The install is only finished when sessions are possible. Without
		// this the card would say tmux is ready while the manager still holds
		// the "tmux is not installed" it decided at start-up, the sheet's
		// Start would stay disabled, and the page would contradict itself
		// until somebody restarted Socrates. The outcome is a line of the log
		// like any other, so it is written before the log is kept.
		if exit == 0 {
			if err := s.manager.Redetect(ctx); err != nil {
				t.publish(tmuxEvent{Type: "line",
					Line: "tmux is installed, but sessions are still not possible: " + err.Error()})
			} else {
				t.publish(tmuxEvent{Type: "line", Line: "tmux is ready; sessions can be started now."})
			}
		}
		// The record is written before the run is marked finished: a page
		// that polls the card the moment `installing` goes false has to find
		// the exit code already there.
		t.mu.Lock()
		lines := append([]string(nil), t.lines...)
		t.mu.Unlock()
		if err := s.store.SetJSON(installLogKey, installLog{
			Lines: lines, Exit: exit, At: time.Now().UnixMilli(),
		}); err != nil {
			log.Printf("tmux install: could not record the log: %v", err)
		}
		t.mu.Lock()
		t.running, t.done, t.exit = false, true, exit
		t.mu.Unlock()
		t.publish(tmuxEvent{Type: "done", Exit: exit, OK: exit == 0})
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"installing": true})
}

// handleTmuxEvents is the progress stream. It is Server-Sent Events rather
// than a second WebSocket because the page already has a client for this shape
// - LiveStream, with its backoff and its watchdog - and a log that streams
// one-way is exactly what that client is for.
func (s *Server) handleTmuxEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "this server cannot stream")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Nginx and every other buffering proxy hold a stream until it ends, which
	// for a progress log means showing it all at once when it no longer
	// matters.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// The headers have to reach the client before the first line does. Without
	// this flush net/http holds them until something is written, so a stream
	// opened before an install starts would not be an open stream at all - the
	// browser would sit in "connecting" until the first heartbeat.
	flusher.Flush()

	ch, backlog := s.tmuxAdmin.subscribe()
	defer s.tmuxAdmin.unsubscribe(ch)

	send := func(ev tmuxEvent) bool {
		payload, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, ev := range backlog {
		if !send(ev) {
			return
		}
	}
	// The heartbeat is what the client's watchdog measures itself against: it
	// gives up on a stream that has said nothing for two and a half beats.
	beat := time.NewTicker(10 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !send(ev) {
				return
			}
		case <-beat.C:
			if !send(tmuxEvent{Type: "ping"}) {
				return
			}
		}
	}
}
