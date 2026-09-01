// Package codex speaks Codex's JSON-RPC app-server protocol.
//
// One `codex app-server` process runs per chat, driven over newline-delimited
// JSON-RPC 2.0 on stdio. Start does the initialize/initialized handshake and
// then either thread/start (a new chat) or thread/resume (a chat whose CLI
// died, or a host that was restarted); Send opens a turn with turn/start and
// the turn's progress arrives as notifications, which this package translates
// into the normalised harness.Event stream.
//
// The full unattended contract - approvalPolicy "never", sandbox
// "danger-full-access", the model and the reasoning effort - is passed
// explicitly at thread/start, and the model and effort go on every turn/start
// as well, so a resumed thread never silently runs on whatever model it was
// recorded with. No `-c` flags are used: nothing depends on argv construction
// being right.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/proc"
)

func init() {
	harness.Register(harness.Descriptor{
		ID:            "codex",
		Label:         "Codex",
		Binary:        "codex",
		VersionArgs:   []string{"--version"},
		DefaultModel:  "",
		DefaultEffort: "medium",
		HasEffort:     true,
		New:           func() harness.Adapter { return newAdapter() },
		Discover:      Discover,
	})
}

// Version is what the adapter calls itself in initialize's clientInfo. It is
// cosmetic - it ends up in the app-server's userAgent string and nowhere else
// - and a build that cares can overwrite it.
var Version = "dev"

// The three turn-end triggers' timings (§2.5.2). They are variables rather
// than constants so the tests can drive the watchdogs in milliseconds instead
// of waiting an hour for one.
var (
	// errorGrace is how long an `error` notification (or a systemError thread
	// status) waits for a turn/completed that research says never comes.
	errorGrace = 30 * time.Second
	// quietAfter is when one notice tells the user the agent has gone quiet.
	quietAfter = 10 * time.Minute
	// silenceAfter is when a silent turn is given up on entirely: a quiet
	// command can legitimately print nothing for ten minutes, so this is an
	// hour, and when it fires the process is killed rather than left to
	// deliver a late turn/completed that would end the turn twice.
	silenceAfter = 60 * time.Minute
)

// startGrace bounds the handshake when the caller passed a context without a
// deadline of its own. The host always passes one; a test might not.
const startGrace = 2 * time.Minute

type adapter struct {
	events chan harness.Event

	spec   harness.Spec
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tail
	rpc    *rpc

	// exited is closed by the reader when stdout reaches EOF, waited on by
	// Close so that the last events are through before Events is closed.
	exited chan struct{}

	mu       sync.Mutex
	threadID string
	turn     *turn
	closing  bool // Close was asked for, so EOF is not a death
	dead     bool

	closeOnce sync.Once
	finished  chan struct{}
	// watchers is every per-turn watchdog goroutine, joined by Close so that
	// nothing of this adapter outlives it.
	watchers sync.WaitGroup
}

func newAdapter() *adapter {
	return &adapter{
		events:   make(chan harness.Event, 128),
		exited:   make(chan struct{}),
		finished: make(chan struct{}),
	}
}

// New is the adapter constructor the registry hands out.
func New() harness.Adapter { return newAdapter() }

// turn is everything that belongs to one Send: the engine's id, the native
// turn id needed for turn/interrupt, the accumulators the item notifications
// fill in, and the single sync.Once through which every end trigger passes.
type turn struct {
	id string

	mu       sync.Mutex
	native   string
	lastErr  string
	notices  int
	errTimer *time.Timer

	startOnce sync.Once
	endOnce   sync.Once
	stop      chan struct{}
	bump      chan struct{}

	// Touched only by the reader goroutine.
	texts  map[string]*textBlock
	reason map[string]*strings.Builder
	output map[string]*strings.Builder
}

func newTurn(id string) *turn {
	return &turn{
		id:     id,
		stop:   make(chan struct{}),
		bump:   make(chan struct{}, 1),
		texts:  map[string]*textBlock{},
		reason: map[string]*strings.Builder{},
		output: map[string]*strings.Builder{},
	}
}

func (t *turn) nativeID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.native
}

func (t *turn) setNative(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	if t.native == "" {
		t.native = id
	}
	t.mu.Unlock()
}

// ------------------------------------------------------------------ Start

func (a *adapter) Start(ctx context.Context, spec harness.Spec) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, startGrace)
		defer cancel()
	}
	a.spec = spec

	bin := strings.TrimSpace(spec.Binary)
	if bin == "" {
		bin = "codex"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("codex is not on this machine: %w", err)
	}

	args := append([]string{"app-server", "--listen", "stdio://"}, spec.ExtraArgs...)
	cmd := exec.Command(path, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = append(os.Environ(), spec.Env...)
	proc.Configure(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	a.stderr = newTail(8 << 10)
	cmd.Stderr = a.stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting codex: %w", err)
	}
	a.cmd = cmd
	a.stdin = stdin
	a.rpc = newRPC(stdin)
	go a.read(stdout)

	if err := a.handshake(ctx); err != nil {
		// A half-started process must not be left behind for the host to
		// wonder about.
		a.mu.Lock()
		a.closing = true
		a.mu.Unlock()
		_ = a.stdin.Close()
		_ = proc.Terminate(cmd)
		// The reader owns cmd.Wait, so this waits for it rather than reaping
		// the process a second time.
		select {
		case <-a.exited:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
		return err
	}
	return nil
}

// handshake is initialize + initialized + thread/start or thread/resume. It
// returns only once the session can take a turn, which is what Start promises.
func (a *adapter) handshake(ctx context.Context) error {
	if _, err := a.rpc.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "socrates", "version": Version},
	}); err != nil {
		return fmt.Errorf("codex refused the handshake: %w", err)
	}
	if err := a.rpc.notify("initialized", map[string]any{}); err != nil {
		return err
	}

	var raw json.RawMessage
	var err error
	if a.spec.SessionID == "" {
		params := map[string]any{
			"cwd":            a.spec.Cwd,
			"model":          a.spec.Model,
			"approvalPolicy": "never",
			"sandbox":        "danger-full-access",
		}
		if a.spec.Effort != "" {
			params["config"] = map[string]any{"model_reasoning_effort": a.spec.Effort}
		}
		raw, err = a.rpc.call(ctx, "thread/start", params)
	} else {
		// excludeTurns: the transcript already lives in Socrates' store, so
		// hydrating it again would only cost time.
		raw, err = a.rpc.call(ctx, "thread/resume", map[string]any{
			"threadId":     a.spec.SessionID,
			"cwd":          a.spec.Cwd,
			"excludeTurns": true,
		})
	}
	if err != nil {
		return err
	}

	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("codex answered with a thread this adapter cannot read: %w", err)
	}
	id := res.Thread.ID
	if id == "" {
		id = a.spec.SessionID
	}
	if id == "" {
		return errors.New("codex started a thread without an id")
	}
	a.mu.Lock()
	a.threadID = id
	a.mu.Unlock()
	// Invariant 1: session_id before the first turn_started, including when it
	// is the id that was passed in. It comes from the RPC result only - the
	// thread/started notification carries the same id and is dropped, so the
	// host does not rewrite spec.json twice (F-7).
	a.emit(harness.Event{Kind: harness.KindSessionID, Session: id})
	return nil
}

// ------------------------------------------------------------------- Send

func (a *adapter) Send(ctx context.Context, turnID, text string) error {
	a.mu.Lock()
	if a.dead {
		a.mu.Unlock()
		return errClosed
	}
	if a.turn != nil {
		a.mu.Unlock()
		return errors.New("a turn is already running")
	}
	thread := a.threadID
	if thread == "" {
		a.mu.Unlock()
		return errors.New("the codex session has not been started")
	}
	t := newTurn(turnID)
	a.turn = t
	a.mu.Unlock()

	// F-5: the model and the effort go on every turn, not only at
	// thread/start, so that a resumed thread and a changed model are both
	// deterministic rather than whatever the rollout was recorded with.
	params := map[string]any{
		"threadId": thread,
		"input":    []any{map[string]any{"type": "text", "text": text}},
		"model":    a.spec.Model,
	}
	if a.spec.Effort != "" {
		params["effort"] = a.spec.Effort
	}
	raw, err := a.rpc.call(ctx, "turn/start", params)
	if err != nil {
		a.mu.Lock()
		if a.turn == t {
			a.turn = nil
		}
		a.mu.Unlock()
		return err
	}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &res); err == nil {
		t.setNative(res.Turn.ID)
	}
	a.startTurn(t)
	a.watchers.Add(1)
	go a.watch(t)
	return nil
}

// startTurn emits turn_started exactly once, whether the turn/start result or
// the turn/started notification got here first.
func (a *adapter) startTurn(t *turn) {
	t.startOnce.Do(func() {
		a.emit(harness.Event{Kind: harness.KindTurnStarted, TurnID: t.id})
	})
}

// closeTurn ends the turn in flight. Every trigger - turn/completed, the error
// grace timer, the silence watchdog, an interrupt, the death of the process -
// calls it, and only the first one gets through (invariant 7).
func (a *adapter) closeTurn(t *turn, outcome, errText string) {
	if t == nil {
		return
	}
	t.endOnce.Do(func() {
		a.emit(harness.Event{Kind: harness.KindTurnFinished, TurnID: t.id, Outcome: outcome, Error: errText})
		close(t.stop)
		t.mu.Lock()
		if t.errTimer != nil {
			t.errTimer.Stop()
			t.errTimer = nil
		}
		t.mu.Unlock()
		a.mu.Lock()
		if a.turn == t {
			a.turn = nil
		}
		a.mu.Unlock()
	})
}

// current is the turn in flight, or nil.
func (a *adapter) current() *turn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turn
}

// ---------------------------------------------------------------- Interrupt

func (a *adapter) Interrupt(ctx context.Context) error {
	t := a.current()
	if t == nil {
		return nil
	}
	native := t.nativeID()
	if native == "" {
		// Nothing to name in turn/interrupt yet. The turn is a few
		// milliseconds old at most and the engine's stop button will be
		// pressed again if it is still running.
		return nil
	}
	a.mu.Lock()
	thread := a.threadID
	a.mu.Unlock()
	_, err := a.rpc.call(ctx, "turn/interrupt", map[string]any{
		"threadId": thread, "turnId": native,
	})
	if err != nil {
		return err
	}
	// The turn ends on the turn/completed{interrupted} that follows; nothing
	// is closed here, so a turn that was finishing anyway keeps its outcome.
	return nil
}

// ------------------------------------------------------------------ Events

func (a *adapter) Events() <-chan harness.Event { return a.events }

// ------------------------------------------------------------------- Close

func (a *adapter) Close(ctx context.Context, grace time.Duration) error {
	a.mu.Lock()
	if a.cmd == nil {
		a.mu.Unlock()
		a.finish()
		a.joinWatchers()
		return nil
	}
	a.closing = true
	cmd := a.cmd
	a.mu.Unlock()

	// Closing stdin is the clean exit: research §7 measured rc=0 in ~0.1s.
	if a.stdin != nil {
		_ = a.stdin.Close()
	}
	select {
	case <-a.exited:
	case <-time.After(grace):
		_ = proc.Terminate(cmd)
		select {
		case <-a.exited:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-ctx.Done():
		}
	case <-ctx.Done():
		_ = proc.Terminate(cmd)
	}
	a.finish()
	a.joinWatchers()
	return nil
}

// joinWatchers waits for the per-turn watchdogs, which finish() has just told
// to stop. It is bounded so that Close can never be the thing that hangs.
func (a *adapter) joinWatchers() {
	done := make(chan struct{})
	go func() { a.watchers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// kill is what a watchdog does: a still-living process could deliver a late
// turn/completed that would be a second turn end.
func (a *adapter) kill() {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = proc.Terminate(cmd)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// emit is the one place events leave the adapter. It drops anything written
// after Events was closed rather than panicking on a late writer.
func (a *adapter) emit(ev harness.Event) {
	defer func() { _ = recover() }()
	select {
	case <-a.finished:
	default:
		select {
		case a.events <- ev:
		case <-a.finished:
		}
	}
}

// notice emits one notice, up to MaxNoticesPerTurn per turn, so that a chatty
// warning stream cannot flood a transcript.
func (a *adapter) notice(t *turn, text string) {
	if t == nil {
		a.emit(harness.Event{Kind: harness.KindNotice, Error: text})
		return
	}
	t.mu.Lock()
	t.notices++
	n := t.notices
	t.mu.Unlock()
	switch {
	case n < harness.MaxNoticesPerTurn:
		a.emit(harness.Event{Kind: harness.KindNotice, TurnID: t.id, Error: text})
	case n == harness.MaxNoticesPerTurn:
		a.emit(harness.Event{Kind: harness.KindNotice, TurnID: t.id,
			Error: "the agent reported too many warnings in one turn; the rest are not shown"})
	}
}

// finish closes Events exactly once, after the last event.
func (a *adapter) finish() {
	a.closeOnce.Do(func() {
		close(a.finished)
		close(a.events)
	})
}

// ---------------------------------------------------------------- watchdog

// watch is the silence watchdog of one turn. Any frame for this thread resets
// it: at quietAfter it says out loud that the agent has gone quiet, at
// silenceAfter it gives up, kills the process and reports a fatal.
func (a *adapter) watch(t *turn) {
	defer a.watchers.Done()
	quiet := time.NewTimer(quietAfter)
	hard := time.NewTimer(silenceAfter)
	defer quiet.Stop()
	defer hard.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-a.finished:
			return
		case <-t.bump:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(quietAfter)
			if !hard.Stop() {
				select {
				case <-hard.C:
				default:
				}
			}
			hard.Reset(silenceAfter)
		case <-quiet.C:
			a.notice(t, "the agent has been quiet for 10 minutes")
		case <-hard.C:
			a.closeTurn(t, harness.OutcomeError, "the agent stopped reporting")
			a.mu.Lock()
			a.closing = true
			a.mu.Unlock()
			a.kill()
			a.emit(harness.Event{Kind: harness.KindFatal, Error: "the agent stopped reporting and was killed"})
			a.markDead()
			a.finish()
			return
		}
	}
}

// bump tells the watchdog a frame arrived.
func (t *turn) touch() {
	select {
	case t.bump <- struct{}{}:
	default:
	}
}

// armErrorGrace starts the 30 second wait for a turn/completed that research
// says never follows an error notification or a systemError status. Only the
// first trigger of a turn arms it; the message the turn ends with is the one
// remembered on the turn.
func (a *adapter) armErrorGrace(t *turn) {
	t.mu.Lock()
	if t.errTimer != nil {
		t.mu.Unlock()
		return
	}
	t.errTimer = time.AfterFunc(errorGrace, func() {
		t.mu.Lock()
		msg := t.lastErr
		t.mu.Unlock()
		if msg == "" {
			msg = "codex reported an error and the turn never completed"
		}
		a.closeTurn(t, harness.OutcomeError, msg)
	})
	t.mu.Unlock()
}

func (a *adapter) markDead() {
	a.mu.Lock()
	a.dead = true
	a.mu.Unlock()
}
