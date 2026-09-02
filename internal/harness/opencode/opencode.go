// Package opencode speaks OpenCode's HTTP and SSE server protocol.
//
// One `opencode serve` process runs per chat, on a port the operating system
// picks, behind a password this adapter generates and keeps in memory. The
// session lives in the user's own OpenCode data directory - Socrates sets no
// XDG variable and writes no opencode.json - which is what makes a resume
// after a restart work and what keeps the user's own OpenCode credentials the
// ones in play.
//
// The turn end is the one thing this protocol does not give away. There is no
// idle event and POST /wait is a 503 stub in 1.17.13, so a turn ends when
// step.ended{finish:"stop"} is confirmed by GET /api/session/active no longer
// listing the session, with a slower poll as a backstop for a stream that
// dies. Every trigger - the confirmation, the backstop, an interrupt, a server
// that stops answering, a server that dies - goes through closeTurn, which
// lets exactly one of them through.
//
// # Two event streams
//
// The adapter follows both of the server's SSE endpoints, because between them
// they carry different halves of the same turn (measured against opencode
// 1.17.13):
//
//   - GET /api/session/{id}/event carries only the durable events, and replays
//     the session's whole durable history on every connect. Every decision is
//     made on this one, filtered by the seq baseline described at Send.
//   - GET /api/event carries the same durable events plus the ephemeral
//     *.delta increments, which the session-scoped endpoint never publishes,
//     and replays nothing. The adapter takes the increments from here and
//     ignores everything else on it, including any session but its own - one
//     server runs per chat, but OpenCode opens an internal session of its own
//     to title a chat.
//
// Nothing depends on the second stream: an installation that does not serve it
// loses the streaming increments and still delivers every text block whole,
// which is what invariant 4 allows an adapter that cannot stream. One thing
// does live only there, though: permission.v2.asked is ephemeral, so the
// unattended fallback in mapping.go can in practice only fire off the wide
// stream. With OPENCODE_PERMISSION="allow" it never has to.
//
// # Two footnotes from the measurements
//
// --port 0 means "OpenCode's own default port if it is free, an operating
// system port otherwise": a lone chat usually lands on 4096, and concurrent
// starts fall back cleanly (fifteen starts, no collision). A chat therefore
// normally sits on the port a user's own `opencode serve` would want; nothing
// breaks either way, because the port is read back off the startup line.
//
// GET /config/providers answers 200 with an empty list for about a second
// after the server is healthy, while providers resolve their credentials, so
// Discover polls rather than asking once.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

func init() {
	harness.Register(harness.Descriptor{
		ID:            "opencode",
		Label:         "OpenCode",
		Binary:        "opencode",
		VersionArgs:   []string{"--version"},
		DefaultModel:  "",
		DefaultEffort: "",
		HasEffort:     true,
		Notes: "Socrates shows every model of every provider OpenCode has credentials for - the " +
			"same list as OpenCode's own picker - and does not change your opencode.json.",
		New:      func() harness.Adapter { return newAdapter() },
		Discover: Discover,
	})
}

// The turn-end timings. confirmInterval/confirmAbsent are the confirmation
// poll armed by finish:"stop" and by an interrupt; backstopInterval/
// backstopAbsent are the slow poll that runs for the whole turn in case the
// event stream dies; maxPollFailures is how many polls in a row may fail
// before the server counts as gone.
const (
	confirmInterval  = 400 * time.Millisecond
	confirmAbsent    = 2
	backstopInterval = 2 * time.Second
	backstopAbsent   = 3
	pollTimeout      = 5 * time.Second
	maxPollFailures  = 3
)

// New returns an adapter. The registry calls it; tests call it directly.
func New() harness.Adapter { return newAdapter() }

type adapter struct {
	// events is the one stream out. Everything on it is written by the
	// processor goroutine (or, before that goroutine exists, by Start), so the
	// order of what a reader sees is the order things happened in.
	events chan harness.Event
	// closed is shut when Events has been closed, so a late writer drops its
	// event instead of panicking.
	closed     chan struct{}
	finishOnce sync.Once
	closeOnce  sync.Once

	frames chan frame
	ops    chan func()

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	srv *server
	cli *client

	mu      sync.Mutex
	session string
	// lastSeq is the replay baseline (F-9): every durable event at or below it
	// has been seen, or belongs to a turn that is already in the transcript.
	lastSeq int64
	// gate is non-nil while a Send is being admitted. The processor waits on
	// it, so no event of the new turn is judged before its baseline is known.
	gate chan struct{}
	// stopping says Close asked the server to go away, so its exit is not a
	// death to report.
	stopping bool

	turnID      string
	turnOnce    *sync.Once
	startOnce   *sync.Once
	turnDone    chan struct{}
	open        bool
	interrupted bool
	// produced says this turn emitted a text block or started a tool. A turn
	// that ends having produced neither, with no error and no interrupt, did
	// not succeed quietly - it failed silently, and says so.
	produced bool
	// lastErr is the session.error remembered for this turn; it is what makes
	// the outcome "error" rather than "ok".
	lastErr string
	notices int

	usage      harness.Usage
	usageDirty bool

	tools     map[string]harness.Tool
	pending   map[string]string
	seen      map[string]struct{}
	blocks    []string
	lastFlush map[string]time.Time
	// closedBlocks are the block ids whose complete text has already gone out.
	// The two streams are independent connections, so a block's last delta can
	// arrive after the session stream's text.ended for it; emitting that delta
	// then would put a fragment after the finished text and the engine would
	// append it to the answer.
	closedBlocks map[string]struct{}

	// wideGone says the server-wide stream has stopped, and wideNoticed that
	// the transcript has been told about it once.
	wideGone    bool
	wideNoticed bool
}

func newAdapter() *adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &adapter{
		events:       make(chan harness.Event, 256),
		closed:       make(chan struct{}),
		frames:       make(chan frame, 1024),
		ops:          make(chan func(), 32),
		ctx:          ctx,
		cancel:       cancel,
		tools:        map[string]harness.Tool{},
		pending:      map[string]string{},
		seen:         map[string]struct{}{},
		lastFlush:    map[string]time.Time{},
		closedBlocks: map[string]struct{}{},
	}
}

// ------------------------------------------------------------------- Start

// Start brings one session up: the server, the session itself, its model, and
// the event stream. It returns only once a turn could be sent.
func (a *adapter) Start(ctx context.Context, spec harness.Spec) error {
	bin := spec.Binary
	if bin == "" {
		found, err := exec.LookPath("opencode")
		if err != nil {
			return fmt.Errorf("opencode: %w", err)
		}
		bin = found
	}
	model, err := parseModel(spec.Model, spec.Effort)
	if err != nil {
		return err
	}

	srv, cli, err := startServer(ctx, bin, spec.Cwd, spec.Env, spec.ExtraArgs)
	if err != nil {
		return err
	}
	a.srv, a.cli = srv, cli

	session := spec.SessionID
	if session == "" {
		// location.directory, not a top-level directory: the top-level field
		// is silently ignored and the agent's tools would run in the server's
		// own working directory.
		session, err = cli.createSession(ctx, spec.Cwd)
		if err != nil {
			srv.stop()
			return err
		}
	}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()

	if model != nil {
		if err := cli.setModel(ctx, session, *model); err != nil {
			srv.stop()
			return err
		}
	}

	body, err := cli.openStream(a.ctx, session)
	if err != nil {
		srv.stop()
		return err
	}
	// The wide stream has no replay, so it is opened here rather than by its
	// own goroutine a moment later: increments published in the gap would be
	// gone for good, and a short answer can be over inside it. Failing to
	// open it is not a reason to fail Start - the follower retries, and says
	// so once if the route is not there at all.
	wide, wideErr := cli.openDeltaStream(a.ctx)
	if wideErr != nil {
		wide = nil
	}

	// Invariant 1: session_id before the first turn_started, including when it
	// is the id that was passed in. Nothing else is running yet, so this is
	// the one event not written by the processor.
	a.emit(harness.Event{Kind: harness.KindSessionID, Session: session})

	a.wg.Add(4)
	go a.process()
	go a.stream(body)
	go a.deltaStream(wide)
	go a.watchExit()
	return nil
}

// parseModel splits spec.Model into OpenCode's ModelRef. The id is written
// <providerID>|<modelID> and split on the first pipe - a pipe, not a slash,
// because model ids contain slashes ("anthropic/claude-haiku-4.5").
//
// An id without a pipe is refused rather than guessed at: OpenCode needs both
// halves, and silently running on the server's default model would be a
// wrong answer wearing the right label.
func parseModel(model, effort string) (*modelRef, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil
	}
	provider, id, ok := strings.Cut(model, "|")
	if !ok || provider == "" || id == "" {
		return nil, fmt.Errorf("opencode: the model %q must be written as <provider>|<model>, for example opencode|big-pickle", model)
	}
	return &modelRef{ID: id, ProviderID: provider, Variant: effort}, nil
}

// -------------------------------------------------------------------- Send

// Send admits one user message and opens a turn.
func (a *adapter) Send(ctx context.Context, turnID, text string) error {
	a.mu.Lock()
	select {
	case <-a.closed:
		a.mu.Unlock()
		return errors.New("opencode: the session is closed")
	default:
	}
	if a.turnID != "" {
		a.mu.Unlock()
		return errors.New("opencode: a turn is already running")
	}
	session := a.session
	if session == "" {
		a.mu.Unlock()
		return errors.New("opencode: the session has not started")
	}
	// The gate holds the processor at the door until the baseline is known,
	// so that this turn's own prompted/text/step events - which the server
	// may have emitted before it answered the POST - are judged against
	// admittedSeq rather than against the previous turn's.
	gate := make(chan struct{})
	a.gate = gate
	a.turnID = turnID
	a.turnOnce = &sync.Once{}
	a.startOnce = &sync.Once{}
	a.turnDone = make(chan struct{})
	a.interrupted = false
	a.produced = false
	a.lastErr = ""
	a.notices = 0
	a.wideNoticed = false
	a.usage = harness.Usage{}
	a.usageDirty = false
	a.tools = map[string]harness.Tool{}
	a.pending = map[string]string{}
	a.seen = map[string]struct{}{}
	a.blocks = nil
	a.lastFlush = map[string]time.Time{}
	a.closedBlocks = map[string]struct{}{}
	a.mu.Unlock()

	admitted, err := a.cli.prompt(ctx, session, text)
	a.mu.Lock()
	if err == nil && a.turnOnce == nil {
		// The server died while the prompt was in flight, and die() has
		// already ended this turn and closed Events. Opening it again here
		// would leave a poll running against a corpse and promise a
		// turn_finished nothing can deliver.
		err = errors.New("opencode: the server stopped while the message was being delivered")
	}
	if err != nil {
		a.turnID, a.turnOnce, a.startOnce = "", nil, nil
		a.turnDone, a.gate = nil, nil
		a.mu.Unlock()
		close(gate)
		return err
	}
	// F-9: admittedSeq is the durable seq of this turn's prompt.admitted, so
	// everything strictly after it belongs to this turn and everything at or
	// below it is history the stream replayed.
	if admitted > 0 {
		a.lastSeq = admitted - 1
	}
	a.open = true
	a.gate = nil
	done := a.turnDone
	a.mu.Unlock()
	close(gate)

	a.wg.Add(1)
	go a.poll(done)
	return nil
}

// --------------------------------------------------------------- Interrupt

// Interrupt cancels the turn in flight. It arms the fast confirmation poll
// directly, so a cancel lands in well under a second instead of waiting out
// the backstop.
func (a *adapter) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	session, open := a.session, a.open
	a.mu.Unlock()
	if !open {
		return nil
	}
	if err := a.cli.interrupt(ctx, session); err != nil {
		// The cancel did not land, so the turn is still the agent's. Saying
		// "interrupted" for a turn that then finishes on its own would be a
		// wrong answer about the user's own action.
		return err
	}
	a.mu.Lock()
	a.interrupted = true
	a.mu.Unlock()
	a.armConfirm()
	return nil
}

// ------------------------------------------------------------------ Events

func (a *adapter) Events() <-chan harness.Event { return a.events }

// ------------------------------------------------------------------- Close

// Close stops the server and closes Events. It is idempotent.
//
// A turn still open when Close arrives ends as interrupted, through the same
// closeTurn as every other trigger and before Events closes: invariant 2
// admits no third outcome, and a caller that closed a host mid-turn is owed
// the answer rather than left to infer it from a shut channel.
func (a *adapter) Close(ctx context.Context, grace time.Duration) error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.stopping = true
		srv := a.srv
		a.mu.Unlock()

		a.closeTurn(harness.OutcomeInterrupted, "")
		a.cancel()
		if srv != nil {
			srv.stopWithin(ctx, grace)
		}
		// A reader that has stopped reading must not keep the adapter alive,
		// so the wait is bounded; emit survives the close either way.
		done := make(chan struct{})
		go func() { a.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(grace):
		case <-ctx.Done():
		}
		a.finish()
	})
	return nil
}

// ---------------------------------------------------------- the event loops

// process is the single writer of Events: every frame, every poll result and
// every death is handled here, so what a reader sees is one ordered stream.
func (a *adapter) process() {
	defer a.wg.Done()
	tick := time.NewTicker(harness.TextDeltaFlush)
	defer tick.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.closed:
			return
		case op := <-a.ops:
			op()
		case f := <-a.frames:
			a.handle(f)
		case <-tick.C:
			a.flushDeltas("")
		}
	}
}

// post hands work to the processor. It never blocks for long: the queue is
// small, and an adapter that is shutting down drops what is left.
func (a *adapter) post(op func()) {
	select {
	case a.ops <- op:
	case <-a.ctx.Done():
	case <-a.closed:
	}
}

// stream follows the session-scoped stream, the one every decision rests on.
// There is no separate reconnect handling: it replays the whole durable
// history on every connect, and the seq baseline filters it the same way on
// the first connect as on the tenth.
func (a *adapter) stream(first io.ReadCloser) {
	defer a.wg.Done()
	a.follow(first, false, func(ctx context.Context) (io.ReadCloser, error) {
		return a.cli.openStream(ctx, a.currentSession())
	})
}

// deltaStream follows the server-wide stream for the ephemeral increments the
// session-scoped one does not carry. Nothing depends on it: if it never
// connects, the turn still runs and its text still arrives whole, one block at
// a time, which is what invariant 4 allows an adapter that cannot stream. The
// transcript is told once either way, because "the answer stopped appearing as
// it was typed" is otherwise indistinguishable from a stalled agent.
func (a *adapter) deltaStream(first io.ReadCloser) {
	defer a.wg.Done()
	a.follow(first, true, a.cli.openDeltaStream)
}

// wideStreamLost journals one notice per turn - or one outside a turn - about
// the increments no longer arriving.
func (a *adapter) wideStreamLost(text string) {
	a.mu.Lock()
	if a.wideNoticed {
		a.mu.Unlock()
		return
	}
	a.wideNoticed = true
	a.wideGone = true
	turnID := a.turnID
	a.mu.Unlock()
	a.post(func() { a.noticeFor(turnID, text) })
}

// follow keeps one SSE connection open, re-opening it with backoff when it
// drops, until the adapter is done or the endpoint turns out not to exist.
func (a *adapter) follow(first io.ReadCloser, global bool, open func(context.Context) (io.ReadCloser, error)) {
	body := first
	attempt := 0
	for {
		if body != nil {
			readStream(a.ctx, body, global, a.frames)
			body.Close()
			body = nil
			attempt = 0
			if global && a.ctx.Err() == nil {
				a.wideStreamLost("live typing was interrupted; the answer will still arrive whole")
			}
		}
		select {
		case <-time.After(streamBackoff(attempt)):
		case <-a.ctx.Done():
			return
		}
		next, err := open(a.ctx)
		if err != nil {
			if a.ctx.Err() != nil {
				return
			}
			if errors.Is(err, errNoRoute) {
				if global {
					a.wideStreamLost("live typing is unavailable: this OpenCode does not publish /api/event")
				}
				return
			}
			attempt++
			continue
		}
		body = next
	}
}

// watchExit turns the server process dying into a fatal. A dead server cannot
// finish the turn, and the poll below would only find out six seconds later.
func (a *adapter) watchExit() {
	defer a.wg.Done()
	select {
	case <-a.srv.exited:
	case <-a.ctx.Done():
		return
	}
	a.mu.Lock()
	stopping := a.stopping
	a.mu.Unlock()
	if stopping {
		return
	}
	msg := "opencode serve exited"
	if a.srv.exitErr != nil {
		msg += " (" + a.srv.exitErr.Error() + ")"
	}
	if tail := a.srv.tail(); tail != "" {
		msg += ": " + lastLine(tail)
	}
	a.post(func() { a.die(msg) })
}

// poll is one turn's backstop: a slow watch on GET /api/session/active that
// runs for the whole turn, in case the event stream dies and no step.ended
// ever arms a confirmation. It is also the one place a server that has stopped
// answering is noticed.
func (a *adapter) poll(done <-chan struct{}) {
	defer a.wg.Done()

	absent, fails := 0, 0
	tick := time.NewTicker(backstopInterval)
	defer tick.Stop()

	for {
		select {
		case <-done:
			return
		case <-a.ctx.Done():
			return
		case <-tick.C:
			running, err := a.isActive()
			switch {
			case err != nil:
				// A dead or wedged server errors rather than answering with
				// an empty map; three of those in a row is the end of it.
				absent = 0
				fails++
				if fails >= maxPollFailures {
					a.post(func() { a.die("opencode stopped answering") })
					return
				}
			case running:
				fails, absent = 0, 0
			default:
				fails = 0
				absent++
				if absent >= backstopAbsent {
					a.post(func() {
						a.notice(backstopNotice + a.serverSaid())
						a.closeTurn("", "")
					})
					return
				}
			}
		}
	}
}

// armConfirm starts one confirmation watch: the fast poll that turns a
// step.ended{finish:"stop"}, or an interrupt, into the end of the turn once
// GET /api/session/active has stopped listing the session twice in a row.
//
// Each trigger gets its own watch, because each is its own claim that the turn
// is over. Two step.ended{finish:"stop"} frames a moment apart therefore
// produce two watches that both reach closeTurn, which is exactly the race the
// sync.Once in there exists for.
func (a *adapter) armConfirm() {
	a.mu.Lock()
	done := a.turnDone
	a.mu.Unlock()
	if done == nil {
		return
	}
	a.wg.Add(1)
	go a.confirmEnd(done)
}

func (a *adapter) confirmEnd(done <-chan struct{}) {
	defer a.wg.Done()

	absent := 0
	tick := time.NewTicker(confirmInterval)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-a.ctx.Done():
			return
		case <-tick.C:
			running, err := a.isActive()
			if err != nil {
				// A failing poll is the backstop's business, not this watch's.
				absent = 0
				continue
			}
			if running {
				absent = 0
				continue
			}
			absent++
			if absent >= confirmAbsent {
				a.post(func() { a.closeTurn("", "") })
				return
			}
		}
	}
}

// backstopNotice is what a turn closed by the idle poll rather than by a
// step.ended{finish:"stop"} says in the transcript, so a wrong guess is
// visible instead of silent.
const backstopNotice = "this turn was closed by the idle check rather than by the agent"

// serverSaid appends the server's own last error line when there is one. On
// the failure this notice usually accompanies - a model the server cannot run
// - nothing reaches the wire at all, but the reason is printed on the
// server's own output, and it is the only "why" the user can be given.
func (a *adapter) serverSaid() string {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return ""
	}
	line := srv.errorLine()
	if line == "" {
		return ""
	}
	return "; opencode said: " + line
}

// isActive is one GET /api/session/active for this session.
func (a *adapter) isActive() (bool, error) {
	ctx, cancel := context.WithTimeout(a.ctx, pollTimeout)
	defer cancel()
	return a.cli.active(ctx, a.currentSession())
}

// ------------------------------------------------------------- ending a turn

// closeTurn ends the turn in flight. Every trigger calls it; only the first
// one gets through. An empty outcome means "work it out": interrupted when we
// sent the interrupt, error when a session.error was remembered, else ok.
func (a *adapter) closeTurn(outcome, errText string) {
	a.mu.Lock()
	once := a.turnOnce
	a.mu.Unlock()
	if once == nil {
		return
	}
	once.Do(func() {
		a.mu.Lock()
		turnID := a.turnID
		startOnce := a.startOnce
		if outcome == "" {
			switch {
			case a.interrupted:
				outcome = harness.OutcomeInterrupted
			case a.lastErr != "":
				outcome, errText = harness.OutcomeError, a.lastErr
			case !a.produced:
				// session.error does not fire on the failure paths of this
				// build: a turn on a model the server cannot run simply
				// stops, and the backstop closes it. Calling that ok would
				// show an empty assistant message under a green tick.
				outcome, errText = harness.OutcomeError, "the agent produced no answer"
			default:
				outcome = harness.OutcomeOK
			}
		}
		done := a.turnDone
		a.turnID, a.turnOnce, a.startOnce = "", nil, nil
		a.turnDone = nil
		a.open = false
		a.mu.Unlock()

		// A turn that ended before its prompted arrived still owes one
		// turn_started (invariant 2).
		if startOnce != nil {
			startOnce.Do(func() {
				a.emit(harness.Event{Kind: harness.KindTurnStarted, TurnID: turnID})
			})
		}
		a.flushDeltas("")
		a.emitUsage()
		a.emit(harness.Event{Kind: harness.KindTurnFinished, TurnID: turnID,
			Outcome: outcome, Error: errText})
		if done != nil {
			close(done)
		}
	})
}

// die ends a turn that cannot finish, then reports the adapter as gone. After
// a fatal, Events is closed and nothing further arrives (invariant 6).
func (a *adapter) die(reason string) {
	a.mu.Lock()
	open := a.open || a.turnOnce != nil
	srv := a.srv
	// Whatever happens next, the server's exit is not news any more.
	a.stopping = true
	a.mu.Unlock()
	if open {
		a.closeTurn(harness.OutcomeError, reason)
	}
	a.emit(harness.Event{Kind: harness.KindFatal, Error: reason})
	a.finish()
	// A server that stopped answering is still running, and the host only
	// calls Close when it decides to; a wedged `opencode serve` must not
	// outlive the chat it belonged to. Off this goroutine, because stopping
	// waits for the process and this one is the writer of Events.
	if srv != nil {
		go srv.stop()
	}
}

// notice is a one-liner for the transcript, attributed to the turn in flight.
func (a *adapter) notice(text string) {
	a.mu.Lock()
	turnID := a.turnID
	a.mu.Unlock()
	a.noticeFor(turnID, text)
}

// noticeFor is notice for a turn named by the caller, so a message written by
// something that started during a turn - a permission reply, a stream that
// dropped - still names it when it arrives after the turn has closed. It is
// capped so a chatty stream cannot flood a transcript.
func (a *adapter) noticeFor(turnID, text string) {
	if text == "" {
		return
	}
	a.mu.Lock()
	a.notices++
	n := a.notices
	a.mu.Unlock()
	switch {
	case n < harness.MaxNoticesPerTurn:
		a.emit(harness.Event{Kind: harness.KindNotice, TurnID: turnID, Error: text})
	case n == harness.MaxNoticesPerTurn:
		a.emit(harness.Event{Kind: harness.KindNotice, TurnID: turnID,
			Error: "further notices from opencode in this turn were dropped"})
	}
}

// ------------------------------------------------------------------- plumbing

// emit is the one place events leave the adapter.
func (a *adapter) emit(ev harness.Event) {
	select {
	case <-a.closed:
		return
	default:
	}
	// Only reachable if Events was closed between the check above and the
	// send below, which Close's ordering already rules out; the recover is
	// there so that a mistake is a dropped event and not a crashed host.
	defer func() { _ = recover() }()
	select {
	case a.events <- ev:
	case <-a.closed:
	}
}

// finish closes Events exactly once, after the last event.
func (a *adapter) finish() {
	a.finishOnce.Do(func() {
		close(a.closed)
		close(a.events)
	})
}

func (a *adapter) currentSession() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

func decode(raw json.RawMessage, into any) bool {
	if len(raw) == 0 {
		return false
	}
	return json.Unmarshal(raw, into) == nil
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
