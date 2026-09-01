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
// which is what invariant 4 allows an adapter that cannot stream.
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
		Notes: "Socrates shows the models OpenCode reports as connected and does not change your " +
			"opencode.json. OpenRouter models need a provider override in this OpenCode release.",
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
}

func newAdapter() *adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &adapter{
		events:    make(chan harness.Event, 256),
		closed:    make(chan struct{}),
		frames:    make(chan frame, 1024),
		ops:       make(chan func(), 32),
		ctx:       ctx,
		cancel:    cancel,
		tools:     map[string]harness.Tool{},
		pending:   map[string]string{},
		seen:      map[string]struct{}{},
		lastFlush: map[string]time.Time{},
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

	// Invariant 1: session_id before the first turn_started, including when it
	// is the id that was passed in. Nothing else is running yet, so this is
	// the one event not written by the processor.
	a.emit(harness.Event{Kind: harness.KindSessionID, Session: session})

	a.wg.Add(4)
	go a.process()
	go a.stream(body)
	go a.deltaStream()
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
	a.lastErr = ""
	a.notices = 0
	a.usage = harness.Usage{}
	a.usageDirty = false
	a.tools = map[string]harness.Tool{}
	a.pending = map[string]string{}
	a.seen = map[string]struct{}{}
	a.blocks = nil
	a.lastFlush = map[string]time.Time{}
	a.mu.Unlock()

	admitted, err := a.cli.prompt(ctx, session, text)
	a.mu.Lock()
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
	if open {
		a.interrupted = true
	}
	a.mu.Unlock()
	if !open {
		return nil
	}
	err := a.cli.interrupt(ctx, session)
	a.armConfirm()
	return err
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
// a time, which is what invariant 4 allows an adapter that cannot stream.
func (a *adapter) deltaStream() {
	defer a.wg.Done()
	a.follow(nil, true, a.cli.openDeltaStream)
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
		}
		select {
		case <-time.After(streamBackoff(attempt)):
		case <-a.ctx.Done():
			return
		}
		next, err := open(a.ctx)
		if err != nil {
			if a.ctx.Err() != nil || errors.Is(err, errNoRoute) {
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
						a.notice("this turn was closed by the idle check rather than by the agent")
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

// isActive is one GET /api/session/active for this session.
func (a *adapter) isActive() (bool, error) {
	ctx, cancel := context.WithTimeout(a.ctx, pollTimeout)
	defer cancel()
	return a.cli.active(ctx, a.currentSession())
}

// ------------------------------------------------------------- the mapping

// handle is one SSE frame, on the processor goroutine.
func (a *adapter) handle(f frame) {
	a.waitGate()

	if f.global {
		// The server-wide stream is read for the increments the session's own
		// stream does not carry, and for nothing else: anything durable would
		// arrive twice, and one server also hosts the internal session
		// OpenCode opens to title a chat.
		if f.Durable != nil || f.sessionID() != a.currentSession() {
			return
		}
	}

	a.mu.Lock()
	if !a.open {
		// Nothing outside a turn is ours: on a resumed session the stream
		// replays every previous turn's text and every previous
		// step.ended{finish:"stop"}, and one of those would arm the
		// confirmation poll for a turn that has not started.
		a.mu.Unlock()
		return
	}
	if f.Durable != nil {
		if f.Durable.Seq <= a.lastSeq {
			a.mu.Unlock()
			return
		}
		a.lastSeq = f.Durable.Seq
	}
	turnID := a.turnID
	a.mu.Unlock()

	switch f.Type {
	case evPrompted:
		a.startTurn()

	case evTextDelta:
		var d textDeltaData
		if !decode(f.Data, &d) {
			return
		}
		a.addDelta(blockID(d.AssistantMessageID, d.TextID), d.Delta)

	case evTextEnded:
		var d textEndedData
		if !decode(f.Data, &d) {
			return
		}
		id := blockID(d.AssistantMessageID, d.TextID)
		a.flushDeltas(id)
		a.emit(harness.Event{Kind: harness.KindText, TurnID: turnID, ID: id, Text: d.Text})

	case evReasoningEnded:
		var d reasoningEndedData
		if !decode(f.Data, &d) {
			return
		}
		if strings.TrimSpace(d.Text) == "" {
			return
		}
		a.emit(harness.Event{Kind: harness.KindReasoning, TurnID: turnID,
			ID: blockID(d.AssistantMessageID, d.ReasoningID), Text: d.Text})

	case evToolCalled:
		var d toolCalledData
		if !decode(f.Data, &d) {
			return
		}
		tool := harness.Tool{
			Name:      d.Tool,
			Title:     toolTitle(d.Tool, d.Input),
			Input:     toolSummary(d.Tool, d.Input),
			InputJSON: compact(d.Input),
		}
		a.mu.Lock()
		a.tools[d.CallID] = tool
		a.mu.Unlock()
		a.emit(harness.Event{Kind: harness.KindToolStarted, TurnID: turnID, ID: d.CallID, Tool: &tool})

	case evToolSuccess:
		var d toolSuccessData
		if !decode(f.Data, &d) {
			return
		}
		var parts []string
		for _, c := range d.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		tool := a.finishedTool(d.CallID)
		tool.Output = harness.TruncateOutput(strings.Join(parts, "\n"))
		tool.OK = true
		if d.Structured.Exit != nil {
			tool.ExitCode = *d.Structured.Exit
		}
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: turnID, ID: d.CallID, Tool: &tool})

	case evToolFailed:
		var d toolFailedData
		if !decode(f.Data, &d) {
			return
		}
		tool := a.finishedTool(d.CallID)
		tool.Output = harness.TruncateOutput(d.Error.Message)
		tool.OK = false
		a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: turnID, ID: d.CallID, Tool: &tool})

	case evStepEnded:
		var d stepEndedData
		if !decode(f.Data, &d) {
			return
		}
		a.addUsage(d)
		a.emitUsage()
		if d.Finish == finishStop {
			// One step's "stop" is not the turn's end: a queued prompt can
			// start another step. The confirmation poll is what decides.
			a.armConfirm()
		}

	case evPermissionAsked:
		var d permissionAskedData
		if !decode(f.Data, &d) {
			return
		}
		a.notice("opencode asked for permission to " + orElse(d.Action, "run a tool") +
			"; Socrates answered for you, because it runs unattended")
		a.answerPermission(d.ID)

	case evSessionError:
		var d sessionErrorData
		if !decode(f.Data, &d) {
			return
		}
		d.Raw = f.Data
		msg := d.message()
		a.mu.Lock()
		a.lastErr = msg
		a.mu.Unlock()
		a.notice(msg)

	case evPromptAdmitted:
		// The admission is the HTTP response; the event carries nothing new.

	default:
		// server.connected, step.started, the tool.input.* trio, the reasoning
		// and text starts, permission.v2.replied and anything a later release
		// adds: dropped rather than guessed at.
	}
}

// waitGate holds the processor while a Send is being admitted.
func (a *adapter) waitGate() {
	a.mu.Lock()
	gate := a.gate
	a.mu.Unlock()
	if gate == nil {
		return
	}
	select {
	case <-gate:
	case <-a.ctx.Done():
	}
}

// startTurn emits turn_started, once per turn. session.next.prompted is the
// trigger; closeTurn is the safety net for a turn whose prompted never
// arrived, because invariant 2 wants a turn_started before every
// turn_finished.
func (a *adapter) startTurn() {
	a.mu.Lock()
	once, turnID := a.startOnce, a.turnID
	a.mu.Unlock()
	if once == nil {
		return
	}
	once.Do(func() {
		a.emit(harness.Event{Kind: harness.KindTurnStarted, TurnID: turnID})
	})
}

// finishedTool is what a tool.success or tool.failed knows about its call: the
// name and title recorded when it was called, so the finished card does not
// lose the heading the started card had.
func (a *adapter) finishedTool(callID string) harness.Tool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tools[callID]
	if !ok {
		return harness.Tool{Name: "tool", Title: "Ran a tool"}
	}
	delete(a.tools, callID)
	return harness.Tool{Name: t.Name, Title: t.Title}
}

// answerPermission replies to a permission.v2.asked off the processor, so one
// slow HTTP call cannot hold up the event stream. With OPENCODE_PERMISSION
// set this never runs; an ask left unanswered blocks the turn forever, which
// is why it exists at all.
func (a *adapter) answerPermission(request string) {
	session := a.currentSession()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		if err := a.cli.replyPermission(ctx, session, request, "always"); err != nil {
			a.post(func() { a.notice("the permission reply failed: " + err.Error()) })
		}
	}()
}

// ------------------------------------------------------------- text deltas

func blockID(message, block string) string {
	// text-0 restarts in every step, and a tool-using turn has several steps,
	// so the assistant message id is what keeps two different blocks apart.
	if message == "" {
		return block
	}
	return message + ":" + block
}

func (a *adapter) addDelta(id, delta string) {
	if delta == "" {
		return
	}
	a.mu.Lock()
	if _, seen := a.seen[id]; !seen {
		a.seen[id] = struct{}{}
		a.blocks = append(a.blocks, id)
	}
	a.pending[id] += delta
	last := a.lastFlush[id]
	a.mu.Unlock()
	if time.Since(last) >= harness.TextDeltaFlush {
		a.flushDeltas(id)
	}
}

// flushDeltas emits the buffered increments of one block, or of every block
// when id is empty. Deltas are coalesced to one event per TextDeltaFlush per
// block, so a fast stream does not put one event per token in the journal.
func (a *adapter) flushDeltas(id string) {
	a.mu.Lock()
	turnID := a.turnID
	ids := a.blocks
	if id != "" {
		ids = []string{id}
	}
	type out struct{ id, text string }
	var flush []out
	now := time.Now()
	for _, b := range ids {
		text := a.pending[b]
		if text == "" {
			continue
		}
		delete(a.pending, b)
		a.lastFlush[b] = now
		flush = append(flush, out{b, text})
	}
	a.mu.Unlock()
	for _, f := range flush {
		a.emit(harness.Event{Kind: harness.KindTextDelta, TurnID: turnID, ID: f.id, Text: f.text})
	}
}

// ------------------------------------------------------------------- usage

// addUsage accumulates one step's tokens into the turn's running total.
// Invariant 5: a usage event carries the total so far, not the step's own
// numbers, and OpenCode reports per step.
func (a *adapter) addUsage(d stepEndedData) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage.Input += d.Tokens.Input
	a.usage.Output += d.Tokens.Output
	a.usage.Reasoning += d.Tokens.Reasoning
	a.usage.Cached += d.Tokens.Cache.Read
	a.usage.CostUSD += d.Cost
	a.usageDirty = true
}

func (a *adapter) emitUsage() {
	a.mu.Lock()
	if !a.usageDirty {
		a.mu.Unlock()
		return
	}
	a.usageDirty = false
	u, turnID := a.usage, a.turnID
	a.mu.Unlock()
	a.emit(harness.Event{Kind: harness.KindUsage, TurnID: turnID, Usage: &u})
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
	a.mu.Unlock()
	if open {
		a.closeTurn(harness.OutcomeError, reason)
	}
	a.emit(harness.Event{Kind: harness.KindFatal, Error: reason})
	a.finish()
}

// notice is a one-liner for the transcript, capped so a chatty stream cannot
// flood it.
func (a *adapter) notice(text string) {
	if text == "" {
		return
	}
	a.mu.Lock()
	a.notices++
	n, turnID := a.notices, a.turnID
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

// ------------------------------------------------------------- tool wording

// toolTitle is the heading of a tool card, written for a human.
func toolTitle(name string, input json.RawMessage) string {
	switch name {
	case "bash":
		return "Ran a command"
	case "read":
		return titleWith("Read", input, "filePath", "path")
	case "write":
		return titleWith("Wrote", input, "filePath", "path")
	case "edit", "apply_patch":
		return titleWith("Edited", input, "filePath", "path")
	case "glob", "grep":
		return "Searched the code"
	case "list":
		return titleWith("Listed", input, "path")
	case "webfetch":
		return titleWith("Fetched", input, "url")
	case "websearch":
		return "Searched the web"
	case "todowrite", "todoread":
		return "Updated the plan"
	case "task":
		return "Ran a subagent"
	case "question":
		return "Asked a question"
	case "skill":
		return "Used a skill"
	case "":
		return "Ran a tool"
	default:
		return "Ran " + name
	}
}

func titleWith(verb string, input json.RawMessage, keys ...string) string {
	if v := field(input, keys...); v != "" {
		return verb + " " + v
	}
	return verb + " a file"
}

// toolSummary is the one-line input shown on the card. It prefers the field a
// person would recognise and falls back to the compact arguments.
func toolSummary(name string, input json.RawMessage) string {
	if v := field(input, "command", "filePath", "path", "pattern", "url", "query", "description", "prompt"); v != "" {
		return oneLine(v)
	}
	return oneLine(compact(input))
}

// field returns the first of keys that is a non-empty string in input.
func field(input json.RawMessage, keys ...string) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
