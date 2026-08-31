// Package term gives Socrates its own interactive terminal. A session is a
// command running behind a pseudo terminal: Socrates types into it, reads the
// screen the command paints, and presses keys, exactly like a person sitting
// in front of it. That is how the coding agents (Claude Code, Codex,
// OpenCode) are driven - and equally how any other program is run.
package term

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hinshun/vt10x"
)

// Defaults for a new session. The window is deliberately large: agent CLIs
// paint their whole conversation into it, and a taller window means less of it
// scrolls out of reach.
const (
	DefaultCols = 160
	DefaultRows = 48

	maxRawBytes    = 512 << 10
	maxOutputBytes = 256 << 10
)

// ErrClosed is returned when a session has already finished.
var ErrClosed = errors.New("this terminal session is closed")

// Spec describes a session to start.
type Spec struct {
	// Command is the program to run. Empty means the user's login shell,
	// which is the normal case: Socrates gets a shell and takes it from there.
	Command string
	Args    []string
	Dir     string
	Env     []string
	Cols    int
	Rows    int
	// Meta is carried with the session and handed back on reconnect, so the
	// caller can remember which tool a session belongs to.
	Meta map[string]string
}

// State is a snapshot of a session for the API and the web UI.
type State struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ChatID   string `json:"chat_id"`
	Command  string `json:"command"`
	Dir      string `json:"dir"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Running  bool   `json:"running"`
	ExitCode int    `json:"exit_code"`
	Started  int64  `json:"started_at"`
	Ended    int64  `json:"ended_at"`
	// Revision counts screen changes, so a client can tell whether anything
	// happened without comparing the whole screen.
	Revision int64  `json:"revision"`
	Screen   string `json:"screen"`
	Detached bool   `json:"detached"`
}

// Session is one running command behind a pseudo terminal.
type Session struct {
	id      string
	name    string
	chatID  string
	command string
	dir     string

	term *terminal

	mu       sync.Mutex
	vt       vt10x.Terminal
	cols     int
	rows     int
	raw      []byte   // recent bytes exactly as the program wrote them
	journal  *Journal // the same output as readable plain text
	revision int64
	lastOut  time.Time
	running  bool
	exitCode int
	started  time.Time
	ended    time.Time

	// waiters are woken whenever output arrives or the session ends.
	waiters map[chan struct{}]struct{}
	done    chan struct{}
}

// Start launches a session. It never returns a half started session: on error
// nothing is left running.
func Start(id, name, chatID string, spec Spec) (*Session, error) {
	command := strings.TrimSpace(spec.Command)
	args := spec.Args
	if command == "" {
		command = loginShell()
		args = loginShellArgs()
	}
	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}

	dir := spec.Dir
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create working directory %s: %w", dir, err)
		}
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	cmd.Env = Environ(spec.Env, cols, rows)

	t, err := startTerminal(cmd, cols, rows)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("command %q was not found - check the path in the admin dashboard", command)
		}
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	s := &Session{
		id:      id,
		name:    name,
		chatID:  chatID,
		command: strings.TrimSpace(command + " " + strings.Join(args, " ")),
		dir:     dir,
		term:    t,
		cols:    cols,
		rows:    rows,
		running: true,
		started: time.Now(),
		lastOut: time.Now(),
		journal: NewJournal(maxOutputBytes),
		waiters: map[chan struct{}]struct{}{},
		done:    make(chan struct{}),
	}
	// vt10x answers the terminal capability queries a TUI sends on startup;
	// giving it the master side means those answers reach the program.
	//
	// It is used rather than a newer emulator because it has no dependencies
	// of its own and its Write never blocks. An emulator that buffers damage
	// events for a consumer that does not exist stalls this reader, the pipe
	// fills, and the program being driven stops dead - which is exactly what
	// happened while choosing one.
	s.vt = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithWriter(t))

	go s.pump(cmd)
	return s, nil
}

// pump copies the program's output into the screen and the buffers until the
// program exits.
func (s *Session) pump(cmd *exec.Cmd) {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.term.Read(buf)
		if n > 0 {
			s.consume(buf[:n])
		}
		if err != nil {
			break
		}
	}
	err := cmd.Wait()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	s.mu.Lock()
	s.running = false
	s.exitCode = code
	s.ended = time.Now()
	s.wakeLocked()
	s.mu.Unlock()
	close(s.done)
}

func (s *Session) consume(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vt.Write(chunk)
	s.raw = appendCapped(s.raw, chunk, maxRawBytes)
	s.journal.Write(chunk)
	s.revision++
	s.lastOut = time.Now()
	s.wakeLocked()
}

// wakeLocked releases every goroutine waiting for activity.
func (s *Session) wakeLocked() {
	for ch := range s.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Session) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.waiters[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Session) unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	delete(s.waiters, ch)
	s.mu.Unlock()
}

// ID, Name and ChatID identify the session.
func (s *Session) ID() string     { return s.id }
func (s *Session) Name() string   { return s.name }
func (s *Session) ChatID() string { return s.chatID }

// Done is closed when the program has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// Running reports whether the program is still alive.
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Screen renders what a person would see right now, with trailing blank lines
// and trailing spaces removed so it reads well in a chat transcript.
func (s *Session) Screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return trimScreen(s.vt.String())
}

// Output returns the tail of everything the program has written, with escape
// sequences removed. For ordinary commands this is the full transcript; a full
// screen program repaints itself, so for those Screen is the useful view.
func (s *Session) Output(maxBytes int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.journal.Tail(maxBytes)
}

// State snapshots the session.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{
		ID: s.id, Name: s.name, ChatID: s.chatID, Command: s.command, Dir: s.dir,
		Cols: s.cols, Rows: s.rows, Running: s.running, ExitCode: s.exitCode,
		Revision: s.revision, Screen: trimScreen(s.vt.String()),
	}
	if !s.started.IsZero() {
		st.Started = s.started.UnixMilli()
	}
	if !s.ended.IsZero() {
		st.Ended = s.ended.UnixMilli()
	}
	return st
}

// Type sends text as if it had been typed. It is not a shell command runner:
// what the text means is up to whatever is currently on screen.
func (s *Session) Type(text string) error {
	if !s.Running() {
		return ErrClosed
	}
	_, err := s.term.Write([]byte(text))
	return err
}

// SendKeys presses the named keys in order.
func (s *Session) SendKeys(names []string) error {
	seq, err := Keys(names)
	if err != nil {
		return err
	}
	return s.Type(seq)
}

// Resize changes the window size the program sees.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("window size must be positive")
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.vt.Resize(cols, rows)
	s.revision++
	s.mu.Unlock()
	return s.term.Resize(cols, rows)
}

// WaitIdle blocks until the program has produced no output for quiet, which is
// the practical signal that an agent has finished its turn and is waiting for
// the next instruction. It returns false if limit passed first.
func (s *Session) WaitIdle(ctx context.Context, quiet, limit time.Duration) (bool, error) {
	if quiet <= 0 {
		quiet = 2 * time.Second
	}
	deadline := time.Now().Add(limit)
	ch := s.subscribe()
	defer s.unsubscribe(ch)

	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		s.mu.Lock()
		idleFor := time.Since(s.lastOut)
		running := s.running
		s.mu.Unlock()
		if !running {
			return true, nil
		}
		if idleFor >= quiet {
			return true, nil
		}
		if limit > 0 && time.Now().After(deadline) {
			return false, nil
		}
		wait := quiet - idleFor
		if limit > 0 {
			if left := time.Until(deadline); left < wait {
				wait = left
			}
		}
		if wait <= 0 {
			wait = time.Millisecond
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-s.done:
			return true, nil
		case <-ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

// WaitFor blocks until the screen matches pattern. It returns false on timeout
// so the caller can look at the screen and decide what to do.
func (s *Session) WaitFor(ctx context.Context, pattern string, limit time.Duration) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	deadline := time.Now().Add(limit)
	ch := s.subscribe()
	defer s.unsubscribe(ch)

	for {
		if re.MatchString(s.Screen()) {
			return true, nil
		}
		if !s.Running() {
			// One last look: the match may have arrived with the final output.
			return re.MatchString(s.Screen()), nil
		}
		wait := time.Until(deadline)
		if limit > 0 && wait <= 0 {
			return false, nil
		}
		if limit <= 0 {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-ch:
		case <-s.done:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Interrupt presses Ctrl+C in the session.
func (s *Session) Interrupt() error { return s.Type("\x03") }

// Close ends the session. It first asks the program to go away the way a user
// would - Ctrl+C twice, then a hangup - because agent CLIs write state on exit
// and a hard kill leaves them confused about their own last run.
func (s *Session) Close(grace time.Duration) error {
	if !s.Running() {
		return nil
	}
	if grace <= 0 {
		grace = 3 * time.Second
	}
	_ = s.Type("\x03")
	if s.waitGone(grace / 3) {
		return s.finish()
	}
	_ = s.Type("\x03")
	if s.waitGone(grace / 3) {
		return s.finish()
	}
	_ = s.term.Signal(os.Interrupt)
	if s.waitGone(grace / 3) {
		return s.finish()
	}
	_ = s.term.Signal(os.Kill)
	s.waitGone(2 * time.Second)
	return s.finish()
}

func (s *Session) finish() error {
	err := s.term.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	return err
}

func (s *Session) waitGone(d time.Duration) bool {
	select {
	case <-s.done:
		return true
	case <-time.After(d):
		return false
	}
}

func appendCapped(dst, src []byte, max int) []byte {
	dst = append(dst, src...)
	if len(dst) > max {
		dst = append([]byte(nil), dst[len(dst)-max:]...)
	}
	return dst
}

// trimScreen removes trailing padding so a screen reads like text.
func trimScreen(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
