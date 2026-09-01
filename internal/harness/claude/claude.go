// Package claude speaks Claude Code's stream-json protocol.
//
// One long-lived `claude -p --output-format stream-json --input-format
// stream-json` process serves one chat for the whole life of its agent-host.
// A turn is one line written to stdin; the `result` line is its end. Every
// other line is translated into a harness.Event by stream.go, or dropped when
// it has no normalised meaning - never guessed at.
//
// The wire protocol is pinned by scratchpad/research/claude-code.md, verified
// against claude 2.1.252, and the mapping by DESIGN.md §2.5.1.
package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/proc"
)

func init() {
	harness.Register(harness.Descriptor{
		ID:            "claude",
		Label:         "Claude Code",
		Binary:        "claude",
		VersionArgs:   []string{"--version"},
		DefaultModel:  "sonnet",
		DefaultEffort: "medium",
		HasEffort:     true,
		New:           New,
		Discover:      Discover,
	})
}

// New is the Descriptor's constructor. It is exported because the live test
// builds an adapter without going through the registry.
func New() harness.Adapter { return newAdapter() }

// launchGrace is how long Start watches a freshly launched CLI before calling
// it ready.
//
// It cannot wait for `system/init`: contrary to research §1, under
// --input-format stream-json that line is not emitted at process start at all
// but only once the first user message has been written - verified live
// against 2.1.252, where a process left idle for 20 seconds produced zero
// bytes and then emitted init the instant a turn arrived. What Start can
// prove is that the CLI accepted its argv: a rejected flag exits 1 with zero
// bytes on stdout in about a second - measured 0.57-0.91 s on an idle machine
// (F-4). Three seconds leaves room for a loaded one; it is paid once per host,
// not per turn.
//
// It is a var only so the tests can shorten it; nothing outside this package
// writes it.
var launchGrace = 3 * time.Second

// resumeGrace is how long a *resumed* Start additionally waits before calling
// the session good.
//
// A --resume of a session the CLI cannot read does not fail the exec: the CLI
// starts, writes `result{error_during_execution, is_error:true, num_turns:0}`
// unprompted and exits 1 with "No conversation found with session ID" on
// stderr. Measured at 1.3-3.2 s to that line and 1.8-3.9 s to the exit across
// six runs on two machines, so no wait can be a guarantee - which is why the
// R-3 relaunch is armed from eof() and Send waits on one in flight. This
// window only buys the common case: the failure resolves inside Start and the
// host never hands the engine a session that is already dead.
var resumeGrace = 3 * time.Second

// retryWait bounds everything the R-3 relaunch waits for, so a CLI that ever
// stopped exiting after that result cannot leave a turn open forever with no
// turn_finished (invariant 2).
const retryWait = 10 * time.Second

// interruptDeadline is how long Interrupt waits for the control_response. The
// CLI answers promptly (research §3); the wait exists so that a lost response
// cannot hang the host, not because it is expected to be slow.
const interruptDeadline = 10 * time.Second

// stderrKeep is how much of stderr is remembered for the death message. The
// CLI writes nothing at all on a healthy run (research §8), so this only ever
// holds a diagnostic.
const stderrKeep = 8 << 10

type adapter struct {
	out *outbox

	mu            sync.Mutex
	spec          harness.Spec
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	session       string
	resolvedModel string

	// The turn in flight. turnOnce is a fresh sync.Once per Send, which is
	// what makes "exactly one turn_finished per turn" true whatever raced to
	// produce it (invariant 7).
	turnID   string
	turnOnce *sync.Once

	// interruptSent is set when a control_request went out and cleared by
	// closeTurn. It is the primary interrupt signal, checked before the
	// terminal_reason, because an interrupt during text streaming (as opposed
	// to during a tool call) has never been observed and must not be reported
	// back to the user as a failure of their own cancel (F-3).
	interruptSent bool

	// pendingUsage is the usage read off `result`, emitted by closeTurn just
	// before turn_finished, so a turn ended by any trigger still carries its
	// cost.
	pendingUsage *harness.Usage

	notices  int // notices emitted in this turn, capped at MaxNoticesPerTurn
	badLines int // undecodable or unknown lines in this turn

	// Block bookkeeping, per API message id. See stream.go.
	streamMsg   map[string]string      // parent tool id -> the open message id
	streamBlock map[string]map[int]int // message id -> wire index -> arrival index
	streamNext  map[string]int         // message id -> next arrival index
	assistantN  map[string]int         // message id -> next arrival index
	deltas      map[string]*deltaBuf   // block id -> coalesced text_delta state

	// tools remembers what each tool_use id was, so tool_finished can carry
	// the name and title again: the engine patches the card with whatever the
	// finish event says, and an empty title blanks a header the start event
	// had filled in.
	tools   map[string]*harness.Tool
	sawTask bool

	// Interrupt correlation.
	reqN     int
	controls map[string]chan struct{}

	stderr *ring

	// The R-3 state machine. sessionUUID is the id Socrates owns for this
	// chat: generated on a fresh chat, passed in on a resume, and the same one
	// either way after a relaunch. The rest tracks one non-repeating
	// transition, resumed -> resumeFailed -> retried:
	//
	//	resumed      this process carries --resume, so it can fail that way
	//	resumeFailed the ghost signature was seen: is_error with num_turns 0
	//	             and no init ever having arrived
	//	retried      the one relaunch this adapter is allowed has been claimed
	//	retrying     that relaunch is in flight right now, so the death that
	//	             triggered it is expected and is not a fatal
	//	retryDone    closed when the relaunch has finished, either way; Start
	//	             and Send wait on it rather than racing it
	sessionUUID  string
	resumed      bool
	resumeFailed bool
	retried      bool
	retrying     bool
	retryDone    chan struct{}

	turnText string // the text of the turn in flight, for the R-3 replay

	// pendingNotice is said at the start of the next turn. A notice with no
	// turn id, journalled before any run exists, is one the engine has no
	// place to show.
	pendingNotice string

	closing   bool // Close was called: an EOF is expected, not fatal
	dead      bool // the process is gone: Send says so instead of failing a write
	ready     bool // system/init arrived, so the CLI really opened the session
	startedOK bool // Start returned nil, so a death from here on is a real one
	exited    chan struct{}
	eofDone   chan struct{}
	// spoke is closed by the reader the first time the CLI writes anything.
	// It is the fast path out of launchGrace for an agent that does announce
	// itself at start.
	spoke   chan struct{}
	waitErr error
}

func newAdapter() *adapter {
	return &adapter{
		out:         newOutbox(),
		streamMsg:   map[string]string{},
		streamBlock: map[string]map[int]int{},
		streamNext:  map[string]int{},
		assistantN:  map[string]int{},
		deltas:      map[string]*deltaBuf{},
		tools:       map[string]*harness.Tool{},
		controls:    map[string]chan struct{}{},
		stderr:      &ring{limit: stderrKeep},
		exited:      make(chan struct{}),
		eofDone:     make(chan struct{}),
		spoke:       make(chan struct{}),
	}
}

func (a *adapter) Events() <-chan harness.Event { return a.out.ch }

// ------------------------------------------------------------------- start

func (a *adapter) Start(ctx context.Context, spec harness.Spec) error {
	id := spec.SessionID
	if id == "" {
		// Socrates owns the uuid so that a resume target is deterministic;
		// --continue and --fork-session are never used (DESIGN §2.5.1).
		id = uuid.NewString()
	}
	a.mu.Lock()
	a.spec, a.sessionUUID = spec, id
	a.mu.Unlock()

	err := a.launch(ctx, spec, id, spec.SessionID != "")
	if err != nil {
		// A resumed launch that died may be R-3's ghost rather than a broken
		// install. eof() is the one place that arms the relaunch, so Start
		// waits for its verdict instead of racing it.
		if !a.awaitRetry(ctx) {
			return err
		}
	}
	// Invariant 1: session_id before the first turn_started, including when
	// it is the id that was passed in. It is the id Socrates generated rather
	// than one read off the wire, because the CLI says nothing at all until
	// the first turn - and it is the id the CLI echoes back, since
	// --fork-session, the one flag that reallocates it, is never used. If the
	// CLI ever disagreed, the correction would come from the init line inside
	// the first turn (stream.go) - so it would carry that turn's id, not this
	// moment's.
	a.mu.Lock()
	a.startedOK = true
	a.mu.Unlock()
	a.out.emit(harness.Event{Kind: harness.KindSessionID, Session: id})
	return nil
}

// awaitRetry runs, or waits out, the R-3 relaunch that the death of a resumed
// process arms, and reports whether the adapter came back alive.
//
// It does not wait for eof to get there first: the ghost result is decoded
// before the stream ends, so resumeFailed is already set by the time Start
// sees the process go, and claimRetry - one mutex, one winner - decides
// between the two of them. Whoever loses waits on the winner's channel.
func (a *adapter) awaitRetry(ctx context.Context) bool {
	run, done := a.takeRetry()
	switch {
	case run:
		// No turn can be open yet: Start has not returned.
		a.retryWithoutResume("", "")
	case done != nil:
		if !waitFor(ctx, done, 3*retryWait) {
			return false
		}
	default:
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stdin != nil && !a.dead
}

// waitFor blocks on ch, reporting whether it closed before ctx or the cap.
func waitFor(ctx context.Context, ch <-chan struct{}, cap time.Duration) bool {
	t := time.NewTimer(cap)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	case <-t.C:
		return false
	}
}

// launch starts one process and returns once it has accepted its argv.
func (a *adapter) launch(ctx context.Context, spec harness.Spec, sessionID string, resume bool) error {
	bin := spec.Binary
	if bin == "" {
		bin = "claude"
	}
	args := Argv(spec, sessionID, resume)

	cmd := exec.Command(bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = append(append(os.Environ(), "IS_SANDBOX=1"), spec.Env...)
	proc.Configure(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("claude: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("claude: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude: starting %s: %w", bin, err)
	}

	a.mu.Lock()
	a.cmd, a.stdin, a.resumed, a.ready, a.dead = cmd, stdin, resume, false, false
	exited, eofDone, spoke := a.exited, a.eofDone, a.spoke
	a.mu.Unlock()

	go a.drainStderr(stderr)
	// One goroutine owns the whole life of this process: it reads stdout to
	// EOF, reaps the process, and then reports the death. Nothing else ends a
	// turn or closes Events while a process exists, which is what keeps
	// "exactly one turn_finished" and "Events closed after the last event"
	// true when a Close races the CLI's own exit.
	go func() {
		a.readLoop(stdout)
		werr := cmd.Wait()
		a.mu.Lock()
		a.waitErr = werr
		a.mu.Unlock()
		close(exited)
		a.eof()
		close(eofDone)
	}()

	// Start returns only when the session can take a turn. There is nothing
	// to read that proves it - see launchGrace - so what is proven instead is
	// that the CLI did not reject its argv and walk straight back out.
	window := launchGrace
	if resume {
		// A resumed launch's first line can be the ghost death itself, so
		// speaking is not proof of health here and the window is the longer
		// one.
		window = resumeGrace
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case <-exited:
			return fmt.Errorf("claude: %s exited before it was ready%s", bin, a.stderrSuffix())
		case <-spoke:
			if resume {
				// Keep waiting: this line may be the death, and the caller
				// gets its verdict from awaitRetry either way.
				spoke = nil
				continue
			}
			// It wrote something, so it is past its argv parsing.
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
}

// Argv is the exact production command line. It is built in one place so that
// live_test.go can run it verbatim: of these flags only
// --replay-user-messages was live-tested by the research, and a rejected flag
// kills the process at start - which would kill every chat (F-4).
//
// F-4 is now closed: this exact argv, --name, --setting-sources and --effort
// included, was run against claude 2.1.252 by live_test.go and accepted. None
// of the flags had to be dropped.
func Argv(spec harness.Spec, sessionID string, resume bool) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", "bypassPermissions",
		"--setting-sources", "project,local",
		"--replay-user-messages",
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if name := strings.TrimSpace(spec.ChatTitle); name != "" {
		args = append(args, "--name", name)
	} else if spec.ChatID != "" {
		args = append(args, "--name", spec.ChatID)
	}
	return append(args, spec.ExtraArgs...)
}

// takeRetry settles a caller's part in the one R-3 relaunch this adapter is
// allowed, under a single lock: run it, wait for the one already running, or
// neither. Start and eof both race for it - eof runs the moment the ghost
// process is reaped, and the relaunch it starts clears resumeFailed - so
// reading the flags and claiming them has to be one indivisible step or Start
// asks a question whose answer has already been erased.
//
// done is non-nil once anyone has claimed, and stays non-nil, closed, after
// the relaunch is over: a later caller waiting on it is told "already
// handled" immediately.
func (a *adapter) takeRetry() (run bool, done chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.resumed {
		return false, nil
	}
	if a.retryDone != nil {
		return false, a.retryDone
	}
	if !a.resumeFailed || a.retried || a.closing {
		return false, nil
	}
	a.retried, a.retrying = true, true
	a.retryDone = make(chan struct{})
	return true, a.retryDone
}

// stopOldProcess makes sure the process being replaced is really gone and its
// reader really finished, without waiting on it forever: a CLI that stopped
// exiting after the ghost result would otherwise strand the turn.
func (a *adapter) stopOldProcess() {
	a.mu.Lock()
	cmd, exited, eofDone := a.cmd, a.exited, a.eofDone
	a.mu.Unlock()
	if cmd == nil {
		return
	}
	select {
	case <-exited:
		// Already reaped. Signalling its group now would signal whatever
		// inherited the id.
	default:
		_ = proc.Terminate(cmd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), retryWait)
	defer cancel()
	waitFor(ctx, eofDone, retryWait)
}

// resetForRelaunch clears the per-process state so the relaunch starts from a
// clean slate. The outbox survives: it is the adapter's one event stream and
// the caller may already be ranging over it.
func (a *adapter) resetForRelaunch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmd, a.stdin, a.session, a.resolvedModel, a.waitErr = nil, nil, "", "", nil
	a.ready, a.dead, a.resumeFailed = false, false, false
	a.exited = make(chan struct{})
	a.spoke = make(chan struct{})
	a.eofDone = make(chan struct{})
	a.stderr = &ring{limit: stderrKeep}
}

// retryWithoutResume is R-3, armed by the signature of the failure rather than
// by its timing.
//
// The design put the fallback at Start, on the assumption that a --resume of a
// session the CLI cannot read fails the launch. It does not: the exec
// succeeds, and the CLI then writes
// result{subtype:"error_during_execution", is_error:true, num_turns:0}
// unprompted and exits 1 with "No conversation found with session ID" on
// stderr - measured between 1.3 s and 3.9 s after launch, which is before,
// during or after the first Send depending on nothing the adapter controls.
// So the arm is that signature (an error result with zero turns, on a resumed
// process that never saw init), the trigger is eof(), and a turn that was
// already open is replayed on the fresh process under the same turn id.
func (a *adapter) retryWithoutResume(turnID, text string) {
	defer a.finishRetry()

	a.stopOldProcess()
	a.resetForRelaunch()

	a.mu.Lock()
	spec, id := a.spec, a.sessionUUID
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*retryWait)
	defer cancel()
	if err := a.launch(ctx, spec, id, false); err != nil {
		msg := "the previous session could not be resumed and a fresh one could not be started: " + err.Error()
		a.closeTurn(harness.OutcomeError, msg)
		a.out.emit(harness.Event{Kind: harness.KindFatal, Error: msg})
		a.out.finish()
		return
	}
	a.out.emit(harness.Event{Kind: harness.KindSessionID, Session: id})

	const lost = "the previous session could not be resumed; this chat continues without its earlier history"
	if turnID == "" {
		// There is no run to hang it on yet, and a notice journalled before
		// any run exists is one the engine has nowhere to show. It is said at
		// the start of the next turn instead.
		a.mu.Lock()
		a.pendingNotice = lost
		a.mu.Unlock()
		return
	}
	a.notice(turnID, lost)

	// No second turn_started: this is the same turn, on a new process.
	if err := a.writeTurn(text); err != nil {
		a.closeTurn(harness.OutcomeError, "replaying the turn after the restart: "+err.Error())
	}
}

// finishRetry releases everyone waiting on the relaunch.
func (a *adapter) finishRetry() {
	a.mu.Lock()
	done := a.retryDone
	a.retrying = false
	a.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	default:
		close(done)
	}
}

// pendingRetry returns the channel of an R-3 relaunch in flight, or nil.
func (a *adapter) pendingRetry() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.retrying {
		return nil
	}
	return a.retryDone
}

// writeTurn puts one user line on stdin. Send and the R-3 replay share it, so
// "the session is not running" reads the same either way.
func (a *adapter) writeTurn(text string) error {
	line, err := userLine(text)
	if err != nil {
		return err
	}
	a.mu.Lock()
	stdin, dead := a.stdin, a.dead
	a.mu.Unlock()
	if stdin == nil || dead {
		return errNotRunning
	}
	if _, err := stdin.Write(line); err != nil {
		return fmt.Errorf("claude: writing the turn: %w", err)
	}
	return nil
}

// errNotRunning is what every method says once the CLI is gone, instead of
// leaking a pipe's "file already closed" to a caller who cannot act on it.
var errNotRunning = errors.New("claude: the session is not running")

// modelMatches reports whether the model the CLI resolved is the one that was
// asked for. An alias resolves to a canonical dated id ("haiku" ->
// "claude-haiku-4-5-20251001", research §6), so an exact comparison would put
// a notice on every healthy session; a containment test recognises every
// alias and every full id passed through verbatim.
func modelMatches(want, got string) bool {
	w, g := strings.ToLower(want), strings.ToLower(got)
	return strings.Contains(g, w) || strings.Contains(w, g)
}

// -------------------------------------------------------------------- send

func (a *adapter) Send(ctx context.Context, turnID, text string) error {
	// A ghost resume can die at any point up to several seconds after Start,
	// so a Send may land while the R-3 relaunch is in flight. Waiting for it
	// turns what would be a failed run into a turn on the fresh process.
	if done := a.pendingRetry(); done != nil {
		if !waitFor(ctx, done, retryWait) {
			return errNotRunning
		}
	}

	a.mu.Lock()
	if a.stdin == nil || a.dead {
		a.mu.Unlock()
		return errNotRunning
	}
	if a.turnID != "" {
		a.mu.Unlock()
		return errors.New("claude: a turn is already running")
	}
	a.turnID, a.turnOnce, a.turnText = turnID, &sync.Once{}, text
	a.pendingUsage, a.notices, a.badLines = nil, 0, 0
	a.sawTask = false
	pending := a.pendingNotice
	a.pendingNotice = ""
	a.mu.Unlock()

	if err := a.writeTurn(text); err != nil {
		a.mu.Lock()
		a.turnID, a.turnOnce = "", nil
		a.pendingNotice = pending
		a.mu.Unlock()
		return err
	}

	// F-1: Claude has no turn-start message of its own - system/status fires
	// per API request, not per turn - and invariant 2 requires one, so the
	// adapter synthesises it the moment the line is on the wire. The
	// --replay-user-messages echo is deliberately not the trigger: it is one
	// more thing that can fail to arrive.
	a.out.emit(harness.Event{Kind: harness.KindTurnStarted, TurnID: turnID})
	if pending != "" {
		// An R-3 relaunch that happened before any run existed says so here,
		// where the transcript has a turn to show it in.
		a.notice(turnID, pending)
	}
	return nil
}

// --------------------------------------------------------------- interrupt

func (a *adapter) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	if a.turnID == "" || a.stdin == nil || a.dead {
		// A no-op when nothing is running, per the Adapter contract.
		a.mu.Unlock()
		return nil
	}
	a.reqN++
	id := "socrates-" + strconv.Itoa(a.reqN)
	done := make(chan struct{})
	a.controls[id] = done
	a.interruptSent = true
	stdin, exited := a.stdin, a.exited
	a.mu.Unlock()

	line, err := controlLine(id)
	if err != nil {
		return err
	}
	if _, err := stdin.Write(line); err != nil {
		// NIT-7: the flag was set before the write, and a request that never
		// left must not make the next result read as the user's own cancel.
		a.mu.Lock()
		a.interruptSent = false
		delete(a.controls, id)
		a.mu.Unlock()
		return errNotRunning
	}

	timer := time.NewTimer(interruptDeadline)
	defer timer.Stop()
	var ctxErr error
	select {
	case <-done:
	case <-exited:
	case <-ctx.Done():
		ctxErr = ctx.Err()
	case <-timer.C:
		// The `result` is what actually ends the turn; a missing receipt is
		// not itself a failure worth reporting.
	}
	a.mu.Lock()
	delete(a.controls, id)
	a.mu.Unlock()
	return ctxErr
}

// ------------------------------------------------------------------- close

func (a *adapter) Close(ctx context.Context, grace time.Duration) error {
	// Setting closing first stops claimRetry, so at most the relaunch already
	// in flight has to be waited out - and it must be, or Close would leave
	// its process running behind a stream it had already finished.
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()
	if done := a.pendingRetry(); done != nil {
		waitFor(ctx, done, retryWait)
	}

	a.mu.Lock()
	stdin, cmd, exited, eofDone := a.stdin, a.cmd, a.exited, a.eofDone
	a.mu.Unlock()

	if cmd == nil {
		a.closeTurn(harness.OutcomeInterrupted, "")
		a.out.finish()
		return nil
	}
	// Closing stdin is the documented clean shutdown: the CLI exits 0 on EOF
	// (research §8).
	if stdin != nil {
		_ = stdin.Close()
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exited:
	case <-ctx.Done():
		_ = proc.Terminate(cmd)
	case <-timer.C:
		_ = proc.Terminate(cmd)
		hard := time.NewTimer(2 * time.Second)
		defer hard.Stop()
		select {
		case <-exited:
		case <-hard.C:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-exited
		}
	}

	// The reader goroutine ends the open turn and closes Events; waiting for
	// it here is what keeps Close from racing that last event out of the
	// stream. It has already seen the process exit, so this is short.
	select {
	case <-eofDone:
	case <-time.After(5 * time.Second):
	}
	// Only reached when there was no reader to do it: a launch that never
	// became a session. A turn still open still owes its one turn_finished
	// (invariant 2), and Events is closed exactly once either way.
	a.closeTurn(harness.OutcomeInterrupted, "")
	a.out.finish()
	return nil
}

// ---------------------------------------------------------------- turn end

// closeTurn ends the turn in flight. Every trigger calls it; only the first
// one gets through (invariant 7).
func (a *adapter) closeTurn(outcome, errText string) {
	a.mu.Lock()
	once, turnID := a.turnOnce, a.turnID
	a.mu.Unlock()
	if once == nil {
		return
	}
	once.Do(func() {
		a.mu.Lock()
		usage := a.pendingUsage
		a.turnID, a.turnOnce, a.pendingUsage = "", nil, nil
		a.interruptSent = false
		// NIT-9: the block and tool bookkeeping is per turn by construction -
		// an API message never spans a result - so it is cleared here rather
		// than growing by a few entries per turn for the life of the process.
		a.streamMsg = map[string]string{}
		a.streamBlock = map[string]map[int]int{}
		a.streamNext = map[string]int{}
		a.assistantN = map[string]int{}
		a.tools = map[string]*harness.Tool{}
		a.mu.Unlock()

		a.dropDeltas()
		if usage != nil {
			a.out.emit(harness.Event{Kind: harness.KindUsage, TurnID: turnID, Usage: usage})
		}
		a.out.emit(harness.Event{
			Kind: harness.KindTurnFinished, TurnID: turnID, Outcome: outcome, Error: errText,
		})
	})
}

// eof runs when stdout ends: the CLI is gone. A turn in flight ends as an
// error, a fatal follows, and Events is closed - after which nothing arrives
// (invariant 6).
func (a *adapter) eof() {
	a.mu.Lock()
	closing, retrying, open, werr := a.closing, a.retrying, a.turnID != "", a.waitErr
	turnID, text := a.turnID, a.turnText
	// The process is gone: a Send from here on says so instead of failing on
	// a closed pipe (SHOULD-FIX-6).
	a.dead, a.stdin = true, nil
	a.mu.Unlock()

	if retrying {
		// The R-3 fallback is already relaunching this session under the same
		// id. This death is expected and the replacement owns the stream.
		return
	}
	// BLOCKER-1: this is the one place that knows the process is really gone,
	// so it is where R-3 is armed - whether the ghost result arrived before
	// Start returned, between Start and Send, or inside a turn.
	if run, _ := a.takeRetry(); run {
		go a.retryWithoutResume(turnID, text)
		return
	}
	a.mu.Lock()
	startedOK := a.startedOK
	a.mu.Unlock()
	if closing || !startedOK {
		// NIT-8: a process that died before Start ever returned is Start's
		// error to report, not a fatal on a stream nobody is pumping yet.
		a.closeTurn(harness.OutcomeInterrupted, "")
		a.out.finish()
		return
	}
	msg := "claude exited" + exitSuffix(werr) + a.stderrSuffix()
	if open {
		a.closeTurn(harness.OutcomeError, msg+" mid-turn")
	}
	a.out.emit(harness.Event{Kind: harness.KindFatal, Error: msg})
	a.out.finish()
}

func exitSuffix(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return " (code " + strconv.Itoa(ee.ExitCode()) + ")"
	}
	return ""
}

func (a *adapter) stderrSuffix() string {
	a.mu.Lock()
	r := a.stderr
	a.mu.Unlock()
	s := strings.TrimSpace(r.String())
	if s == "" {
		return ""
	}
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return ": " + strings.ReplaceAll(s, "\n", " ")
}

func (a *adapter) drainStderr(r io.Reader) {
	a.mu.Lock()
	ring := a.stderr
	a.mu.Unlock()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			ring.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// notice emits one dim one-liner, capped per turn so a chatty warning stream
// cannot flood a transcript.
func (a *adapter) notice(turnID, text string) {
	a.mu.Lock()
	a.notices++
	n := a.notices
	a.mu.Unlock()
	switch {
	case n < harness.MaxNoticesPerTurn:
		a.out.emit(harness.Event{Kind: harness.KindNotice, TurnID: turnID, Error: text})
	case n == harness.MaxNoticesPerTurn:
		a.out.emit(harness.Event{Kind: harness.KindNotice, TurnID: turnID,
			Error: "further notices in this turn are not shown"})
	}
}

// ------------------------------------------------------------------ outbox

// outbox is the adapter's single ordered event stream. Events are queued
// under a lock and forwarded by one goroutine, so emit never blocks: the
// stdout reader can never stall an Interrupt, and a slow consumer can never
// deadlock the reader.
type outbox struct {
	ch chan harness.Event

	mu   sync.Mutex
	cond *sync.Cond
	q    []harness.Event
	done bool
}

func newOutbox() *outbox {
	o := &outbox{ch: make(chan harness.Event)}
	o.cond = sync.NewCond(&o.mu)
	go o.run()
	return o
}

func (o *outbox) emit(ev harness.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return
	}
	o.q = append(o.q, ev)
	o.cond.Signal()
}

// finish closes Events exactly once, after the last queued event.
func (o *outbox) finish() {
	o.mu.Lock()
	o.done = true
	o.cond.Broadcast()
	o.mu.Unlock()
}

func (o *outbox) run() {
	for {
		o.mu.Lock()
		for len(o.q) == 0 && !o.done {
			o.cond.Wait()
		}
		if len(o.q) == 0 {
			o.mu.Unlock()
			close(o.ch)
			return
		}
		batch := o.q
		o.q = nil
		o.mu.Unlock()
		for _, ev := range batch {
			o.ch <- ev
		}
	}
}

// -------------------------------------------------------------------- ring

// ring keeps the tail of stderr, which is where a death gets its explanation.
type ring struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		r.buf = r.buf[len(r.buf)-r.limit:]
	}
	return len(p), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// scan wraps stdout in a scanner sized for the very long lines a big tool
// result produces.
func scan(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	return sc
}
