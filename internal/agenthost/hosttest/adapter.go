// Package hosttest is the scripted, in-process harness.Adapter that the host
// and the engine are tested against.
//
// It is not a test file because two packages need it: internal/agenthost tests
// the host around it, and internal/engine tests the run lifecycle around a
// real host process running it. It is registered by this package's init, so it
// only ever exists in a test binary that imports it - the Socrates binary
// never does, and /api/agents therefore never lists it.
package hosttest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// The host and the engine cannot be tested at all without a real adapter
// behind a real host process: the fakes belong to WP6 and the three protocol
// adapters to WP2/3/4. So this is one of our own - in-process, scripted by an
// environment variable in the same shape as the fakes' script, and registered
// under the id "test".
//
// It doubles as the reference implementation of the invariants in
// harness/harness.go: session_id before the first turn_started, exactly one
// turn_finished per Send whatever raced to produce it, Events closed once.

// ScriptEnv is read from the Spec's Env - so each host can be scripted
// separately - and falls back to the process environment.
const ScriptEnv = "SOCRATES_TEST_SCRIPT"

func init() {
	harness.Register(harness.Descriptor{
		ID:           "test",
		Label:        "Test adapter",
		Binary:       "true",
		VersionArgs:  []string{"--version"},
		DefaultModel: "scripted",
		HasEffort:    true,
		New:          func() harness.Adapter { return newTestAdapter() },
		Discover: func(ctx context.Context, bin string) (harness.Catalog, error) {
			return harness.Catalog{Static: true, Models: []harness.Model{
				{ID: "scripted", Label: "Scripted", Default: true, Efforts: []string{"low", "medium", "high"}},
			}}, nil
		},
	})
}

// Step is one instruction of the script. The shape is the fakes' FAKE_SCRIPT,
// so a test written against one reads the same as a test written against the
// other.
type Step struct {
	Do      string `json:"do"`
	Text    string `json:"text"`
	Name    string `json:"name"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	Exit    int    `json:"exit"`
	MS      int    `json:"ms"`
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
	// Twice makes the step try to end the turn a second time, which is how the
	// sync.Once in closeTurn is tested rather than assumed.
	Twice bool `json:"twice"`
	// Count repeats an event step, for the tests that need a long stream.
	Count int `json:"count"`
}

type testAdapter struct {
	events chan harness.Event
	script []Step

	mu        sync.Mutex
	session   string
	turnID    string
	turnOnce  *sync.Once
	cancel    context.CancelFunc
	closed    bool
	closeOnce sync.Once
	running   sync.WaitGroup
}

func newTestAdapter() *testAdapter {
	return &testAdapter{events: make(chan harness.Event, 64)}
}

func (a *testAdapter) Start(ctx context.Context, spec harness.Spec) error {
	raw := ""
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, ScriptEnv+"="); ok {
			raw = v
		}
	}
	if raw == "" {
		raw = os.Getenv(ScriptEnv)
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &a.script); err != nil {
			return fmt.Errorf("the test script is not valid JSON: %w", err)
		}
	}
	for _, s := range a.script {
		if s.Do == "failstart" {
			return fmt.Errorf("the test adapter was told to fail its start")
		}
	}
	session := spec.SessionID
	if session == "" {
		session = "sess_" + spec.ChatID
	}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	// Invariant 1: session_id before the first turn_started, including when it
	// is the id that was passed in.
	a.emit(harness.Event{Kind: harness.KindSessionID, Session: session})
	return nil
}

func (a *testAdapter) Send(ctx context.Context, turnID, text string) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("the session is closed")
	}
	if a.turnID != "" {
		a.mu.Unlock()
		return fmt.Errorf("a turn is already running")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a.turnID, a.turnOnce, a.cancel = turnID, &sync.Once{}, cancel
	a.mu.Unlock()

	a.emit(harness.Event{Kind: harness.KindTurnStarted, TurnID: turnID})
	a.running.Add(1)
	go func() {
		defer a.running.Done()
		a.play(runCtx, turnID)
	}()
	return nil
}

// play walks the script once per turn, from the start.
func (a *testAdapter) play(ctx context.Context, turnID string) {
	for i, s := range a.script {
		if ctx.Err() != nil {
			a.closeTurn(harness.OutcomeInterrupted, "")
			return
		}
		count := s.Count
		if count <= 0 {
			count = 1
		}
		for n := 0; n < count; n++ {
			id := fmt.Sprintf("%d-%d", i, n)
			switch s.Do {
			case "text":
				a.emit(harness.Event{Kind: harness.KindTextDelta, TurnID: turnID, ID: "text-" + id, Text: s.Text})
				a.emit(harness.Event{Kind: harness.KindText, TurnID: turnID, ID: "text-" + id, Text: s.Text})
			case "reason":
				a.emit(harness.Event{Kind: harness.KindReasoning, TurnID: turnID, ID: "reason-" + id, Text: s.Text})
			case "tool":
				a.emit(harness.Event{Kind: harness.KindToolStarted, TurnID: turnID, ID: "tool-" + id,
					Tool: &harness.Tool{Name: s.Name, Title: "Ran a command", Input: s.Input, InputJSON: `{"command":"` + s.Input + `"}`}})
				a.emit(harness.Event{Kind: harness.KindToolFinished, TurnID: turnID, ID: "tool-" + id,
					Tool: &harness.Tool{Name: s.Name, Title: "Ran a command", Output: s.Output, OK: s.Exit == 0, ExitCode: s.Exit}})
			case "subagent":
				a.emit(harness.Event{Kind: harness.KindSubagentStarted, TurnID: turnID, ID: "sub-" + id,
					Tool: &harness.Tool{Name: "Task", Title: "Started a subagent", Input: s.Input}})
				a.emit(harness.Event{Kind: harness.KindSubagentFinished, TurnID: turnID, ID: "sub-" + id,
					Tool: &harness.Tool{Name: "Task", Title: "Subagent finished", Output: s.Output, OK: true}})
			case "usage":
				a.emit(harness.Event{Kind: harness.KindUsage, TurnID: turnID,
					Usage: &harness.Usage{Input: 100, Output: 20, Total: 120, CostUSD: 0.001}})
			case "notice":
				a.emit(harness.Event{Kind: harness.KindNotice, TurnID: turnID, Error: s.Text})
			case "sleep":
				select {
				case <-time.After(time.Duration(s.MS) * time.Millisecond):
				case <-ctx.Done():
				}
			case "hang":
				// Never end the turn. Interrupt and Close still work, which is
				// the whole point of the step.
				<-ctx.Done()
				a.closeTurn(harness.OutcomeInterrupted, "")
				return
			case "die":
				// What the host sees when the CLI dies mid-turn.
				a.closeTurn(harness.OutcomeError, "the agent exited mid-turn")
				a.emit(harness.Event{Kind: harness.KindFatal, Error: "the agent exited mid-turn"})
				a.finish()
				return
			case "end":
				outcome := s.Outcome
				if outcome == "" {
					outcome = harness.OutcomeOK
				}
				a.closeTurn(outcome, s.Error)
				if s.Twice {
					a.closeTurn(harness.OutcomeError, "a second end that must not get through")
				}
				return
			}
		}
	}
	a.closeTurn(harness.OutcomeOK, "")
}

// closeTurn ends the turn in flight. Every trigger calls it; only the first
// one gets through.
func (a *testAdapter) closeTurn(outcome, errText string) {
	a.mu.Lock()
	once, turnID := a.turnOnce, a.turnID
	a.mu.Unlock()
	if once == nil {
		return
	}
	once.Do(func() {
		a.emit(harness.Event{Kind: harness.KindTurnFinished, TurnID: turnID, Outcome: outcome, Error: errText})
		a.mu.Lock()
		a.turnID, a.turnOnce = "", nil
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
		}
		a.mu.Unlock()
	})
}

func (a *testAdapter) Interrupt(ctx context.Context) error {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (a *testAdapter) Events() <-chan harness.Event { return a.events }

func (a *testAdapter) Close(ctx context.Context, grace time.Duration) error {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { a.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
	case <-ctx.Done():
	}
	a.finish()
	return nil
}

// emit is the one place events leave the adapter, and it drops nothing after
// the channel is closed rather than panicking on a late writer.
func (a *testAdapter) emit(ev harness.Event) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return
	}
	defer func() { _ = recover() }()
	a.events <- ev
}

// finish closes Events exactly once, after the last event.
func (a *testAdapter) finish() {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		close(a.events)
	})
}
