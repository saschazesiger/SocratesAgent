package termux

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrClosed is returned by a viewer whose pseudo terminal has gone.
var ErrClosed = errors.New("this viewer is closed")

// liveSession is what the Manager knows about a running terminal that the
// database row does not: who is watching it, and whose size it is wearing.
//
// The size policy lives here because tmux has no policy worth having. Under
// window-size latest the window follows whichever client last *typed*, so two
// devices alternating make the program re-lay out on every keystroke; under
// manual it follows resize-window and nothing else, which is the only policy
// that can be stated in a sentence: the window is sized to the most recently
// connected or explicitly resized viewer, and typing never changes it.
type liveSession struct {
	id       string
	tmuxName string
	cols     int
	rows     int
	// viewers is ordered by how recently each one attached or resized, most
	// recent last, so the owner is always the tail and a hand-over is just a
	// step backwards.
	viewers []*Viewer
}

func (s *liveSession) owner() *Viewer {
	if len(s.viewers) == 0 {
		return nil
	}
	return s.viewers[len(s.viewers)-1]
}

func (s *liveSession) promote(v *Viewer) {
	s.remove(v)
	s.viewers = append(s.viewers, v)
}

// reclaim puts a viewer back at the head of the queue without touching tmux,
// and reports whether it could: only a window that is already exactly this
// size can be taken over silently.
func (m *Manager) reclaim(v *Viewer, cols, rows int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := m.live[v.SessionID]
	if live == nil || live.cols != cols || live.rows != rows {
		return false
	}
	live.promote(v)
	return true
}

func (s *liveSession) remove(v *Viewer) {
	for i, existing := range s.viewers {
		if existing == v {
			s.viewers = append(s.viewers[:i], s.viewers[i+1:]...)
			return
		}
	}
}

// Viewer is one browser's window onto a session: a `tmux attach` client of our
// own, running on a pseudo terminal we hold the master of.
//
// Its output goes into a ring rather than to a socket, so a reader that stops
// reading falls behind in a fixed amount of memory instead of stalling the
// pane or growing the process.
type Viewer struct {
	// ID is the browser's viewer identity, which is what makes a reconnect
	// recognisable as the same window rather than a new one.
	ID        string
	SessionID string

	m      *Manager
	cmd    *exec.Cmd
	master *os.File
	// tty is the slave name, which is also what tmux calls this client, and
	// therefore how we detach exactly ours.
	tty  string
	ring *Ring

	mu   sync.Mutex
	cols int
	rows int
	// closed is set by Close, ended by the pseudo terminal reaching its end.
	// They are different: a client that tmux detached from outside has ended
	// but still has a process to wait for and a file to close.
	closed bool
	ended  bool

	done chan struct{}
	err  error
}

// Ring is the viewer's replay buffer.
func (v *Viewer) Ring() *Ring { return v.ring }

// TTY is the name tmux knows this client by.
func (v *Viewer) TTY() string { return v.tty }

// Size is what the viewer last said it was.
func (v *Viewer) Size() (cols, rows int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cols, v.rows
}

// Done is closed when the pseudo terminal has ended, whether because the
// viewer was closed or because tmux let go of it.
func (v *Viewer) Done() <-chan struct{} { return v.done }

// Err is why the viewer ended, once Done is closed.
func (v *Viewer) Err() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.err
}

// Write sends keystrokes to the pane.
func (v *Viewer) Write(p []byte) (int, error) {
	v.mu.Lock()
	gone := v.closed || v.ended
	v.mu.Unlock()
	if gone {
		return 0, ErrClosed
	}
	return v.master.Write(p)
}

// Resize tells tmux how big this viewer's window is, and makes it the one the
// session is sized to. An explicit resize is an act of the user - a rotated
// phone, a keyboard opening - so it takes ownership the same way attaching
// does.
func (v *Viewer) Resize(ctx context.Context, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	v.mu.Lock()
	if v.closed || v.ended {
		v.mu.Unlock()
		return ErrClosed
	}
	v.cols, v.rows = cols, rows
	v.mu.Unlock()
	// The pseudo terminal size keeps the tmux client honest about what it can
	// paint; resize-window is what decides the geometry under the manual
	// policy.
	if err := setPTYSize(v.master, cols, rows); err != nil {
		return err
	}
	return v.m.own(ctx, v)
}

// Retake makes this viewer the one the session is sized to again, after its
// socket came back inside the grace.
//
// It exists because resize-window is not free: tmux repaints the whole window
// for every attached client, so re-issuing it at a size the window is already
// wearing turns a reconnect - which is meant to be a pure gap in a byte stream
// - into a full redraw and a visible flash. A viewer that comes back at a
// different size, because the phone was rotated while it was away, resizes for
// real.
func (v *Viewer) Retake(ctx context.Context, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	v.mu.Lock()
	if v.closed || v.ended {
		v.mu.Unlock()
		return ErrClosed
	}
	same := v.cols == cols && v.rows == rows
	v.mu.Unlock()
	if same && v.m.reclaim(v, cols, rows) {
		return nil
	}
	return v.Resize(ctx, cols, rows)
}

// Idle says that the socket driving this viewer has gone, while the terminal
// itself is kept for the ninety seconds a reconnect has to come back in.
//
// It is the moment ownership of the window size moves on, and that moment is
// the socket and not the expiry: a phone that drove out of coverage must not
// pin a laptop's window to sixty columns for a minute and a half. A viewer
// that comes back re-takes ownership like any other attaching one.
func (v *Viewer) Idle() { v.m.forget(v) }

// Close detaches this viewer and lets the session carry on without it.
//
// detach-client rather than closing the master: a detached client exits 0,
// while one whose terminal disappeared exits 1. The session survives either
// way, but only one of the two is quiet in the logs.
func (v *Viewer) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		<-v.done
		return nil
	}
	v.closed = true
	v.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if v.tty != "" {
		_, _ = v.m.tmux.Run(ctx, "detach-client", "-t", v.tty)
	}
	waited := make(chan struct{})
	go func() {
		defer close(waited)
		_ = v.cmd.Wait()
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		if v.cmd.Process != nil {
			_ = v.cmd.Process.Kill()
		}
		<-waited
	}
	err := v.master.Close()
	<-v.done
	v.m.forget(v)
	return err
}

// pump moves the pane's bytes into the ring, answering on the way the colour
// questions tmux asks a client when it attaches.
//
// When the pseudo terminal ends - the ordinary case being that somebody
// detached this client from outside - the viewer stops being a viewer at once:
// it hands the window size on rather than holding a laptop's window at a phone
// size until whoever owns the socket gets round to closing it.
func (v *Viewer) pump(resp *Responder) {
	defer close(v.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := v.master.Read(buf)
		if n > 0 {
			v.ring.Append(resp.Filter(buf[:n]))
		}
		if err != nil {
			v.mu.Lock()
			if !errors.Is(err, io.EOF) && !v.closed {
				v.err = err
			}
			wasClosed := v.closed
			v.ended = true
			v.mu.Unlock()
			if !wasClosed {
				v.m.forget(v)
			}
			return
		}
	}
}
