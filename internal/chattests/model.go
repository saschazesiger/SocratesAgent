// Package chattests keeps the tests a chat writes and runs itself
// (docs/design/CHAT-TESTS.md): the model, the store of every chat's tests,
// each operation the socrates-tests MCP server and the HTTP API share, and
// the Markdown answers the tools give.
//
// A test (T1, T2 ... per chat) is a flow of numbered steps, each an action and
// what must be seen. A run is the chat's agent working through those steps
// and judging each on evidence: what it observed, with screenshots and other
// files. Every run keeps a copy of the steps it was started with, so editing a
// test never rewrites what an earlier run judged.
//
// Everything of a chat sits in one folder, removed and backed up with the
// chat:
//
//	<chat>/tests/tests.json                 {"version":1,"next":4,"rev":17,"tests":[...]}
//	<chat>/tests/files/T1/r2/03-home.png    evidence, per test and run
//
// tests.json is written whole and atomically (a temporary file, a sync and a
// rename) under one lock per chat, and created only by the first change: a
// chat that never had a test has no folder. next is the number of the next
// test, so an id is never given twice, not even after a delete; rev grows
// with every change, so that a page can tell whether what it shows is
// current.
//
// The derived facts (a test's status, whether it is stale, its latest and its
// open run) are methods on Test, and View turns a test into what the API
// sends. Errors are sentences an agent can act on, since the tools hand them
// on as they are.
package chattests

import (
	"time"
)

// By is who did something to a test: the chat's agent or the person.
type By string

const (
	ByAgent  By = "agent"
	ByPerson By = "person"
)

// The verdicts of a step. A run's verdict is one of the first four.
const (
	Pass    = "pass"
	Issue   = "issue"
	Fail    = "fail"
	Blocked = "blocked"
	NA      = "na"
)

// The statuses of a test. An override takes one of the last five.
const (
	StatusDraft   = "draft"
	StatusRunning = "running"
	StatusPassed  = "passed"
	StatusIssue   = "issue"
	StatusFailed  = "failed"
	StatusBlocked = "blocked"
	StatusSkipped = "skipped"
)

// The kinds of evidence, told by a file's extension.
const (
	KindImage = "image"
	KindText  = "text"
	KindVideo = "video"
	KindFile  = "file"
)

// What an event records.
const (
	EventCreated         = "created"
	EventEdited          = "edited"
	EventRun             = "run"
	EventFinished        = "finished"
	EventOverride        = "override"
	EventOverrideRemoved = "override-removed"
	EventSkip            = "skip"
	EventUnskip          = "unskip"
)

// Limits. The sizes are bytes; MaxTitle counts characters.
const (
	MaxTests     = 200
	MaxSteps     = 50
	KeepRuns     = 20
	MaxStepFiles = 40
	MaxFileSize  = 20 << 20
	MaxChatSize  = 500 << 20

	MaxTitle       = 200
	MaxGoal        = 20 << 10
	MaxEnvironment = 200
	MaxTags        = 20
	MaxTag         = 50
	MaxStepText    = 2000
	MaxObserved    = 10 << 10
	MaxNote        = 10 << 10
	MaxSummary     = 4 << 10
	MaxFindings    = 50 << 10
	MaxCaption     = 300

	// keepEvents is how many events a test keeps; the oldest go first.
	keepEvents = 200
)

// Test is one test of a chat.
type Test struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Goal        string    `json:"goal,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Steps       []Step    `json:"steps"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// StepsAt is when the steps last changed, which makes the test stale if
	// that was after its latest run began.
	StepsAt  time.Time `json:"steps_at"`
	Skip     *Skip     `json:"skip,omitempty"`
	Override *Override `json:"override,omitempty"`
	// Runs are in the order they were started, the latest last.
	Runs   []Run   `json:"runs,omitempty"`
	Events []Event `json:"events,omitempty"`
}

// Step is what a person does and what they must then see.
type Step struct {
	Action string `json:"action"`
	Expect string `json:"expect"`
}

// Run is one execution of a test. Its steps are copies of the test's steps
// as they were when it began, with their judgements.
type Run struct {
	N         int        `json:"n"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// Environment is where it ran: what the agent named, else the test's.
	Environment string `json:"environment,omitempty"`
	// Verdict is the worst step's once the run is finished, empty while it
	// is open.
	Verdict  string     `json:"verdict,omitempty"`
	Steps    []Judged   `json:"steps,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	Findings string     `json:"findings,omitempty"`
	// Cursors are, per server recording a chat's actions, the line up to
	// which its actions were taken as this run's evidence.
	Cursors map[string]int `json:"cursors,omitempty"`
}

// Judged is a step of a run with its judgement. Verdict is empty until the
// step is judged.
type Judged struct {
	Action   string     `json:"action"`
	Expect   string     `json:"expect"`
	Verdict  string     `json:"verdict,omitempty"`
	Observed string     `json:"observed,omitempty"`
	Note     string     `json:"note,omitempty"`
	At       *time.Time `json:"at,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Evidence is a file kept with a run, on one of its steps or on the run.
type Evidence struct {
	// File is the path under the test's folder, files/<test>/, with
	// forward slashes: "r2/03-home.png".
	File    string `json:"file"`
	Caption string `json:"caption,omitempty"`
	Kind    string `json:"kind"`
	// Source is "agent", or the name of the server whose recording it is.
	Source string    `json:"source"`
	At     time.Time `json:"at"`
	Size   int64     `json:"size"`
}

// Skip sets a test aside until it is removed.
type Skip struct {
	Reason string    `json:"reason"`
	By     By        `json:"by"`
	At     time.Time `json:"at"`
}

// Override is the person's word on a test's status. It stays until the
// person removes it or a new run begins.
type Override struct {
	Status string    `json:"status"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

// Event is one line of a test's history.
type Event struct {
	At     time.Time `json:"at"`
	By     By        `json:"by"`
	What   string    `json:"what"`
	Detail string    `json:"detail,omitempty"`
}

// Latest is the run begun last, or nil.
func (t *Test) Latest() *Run {
	if len(t.Runs) == 0 {
		return nil
	}
	return &t.Runs[len(t.Runs)-1]
}

// Open is the run still going on, or nil. Only the latest run can be open:
// starting one finishes the one before.
func (t *Test) Open() *Run {
	if r := t.Latest(); r != nil && r.IsOpen() {
		return r
	}
	return nil
}

// Run is run n, or nil.
func (t *Test) Run(n int) *Run {
	for i := range t.Runs {
		if t.Runs[i].N == n {
			return &t.Runs[i]
		}
	}
	return nil
}

// Status is the test's status, in the spec's order of precedence: the
// person's override, a skip, an open run, the latest run's verdict, draft.
func (t *Test) Status() string {
	if t.Override != nil && t.Override.Status != "" {
		return t.Override.Status
	}
	if t.Skip != nil {
		return StatusSkipped
	}
	r := t.Latest()
	if r == nil {
		return StatusDraft
	}
	if r.IsOpen() {
		return StatusRunning
	}
	return VerdictStatus(r.Verdict)
}

// Stale reports whether the steps changed after the latest run began, so
// that its judgements are of steps the test no longer has.
func (t *Test) Stale() bool {
	r := t.Latest()
	return r != nil && t.StepsAt.After(r.StartedAt)
}

// IsOpen reports whether the run is not finished yet.
func (r *Run) IsOpen() bool { return r.EndedAt == nil }

// JudgedCount is how many of the run's steps have a verdict.
func (r *Run) JudgedCount() int {
	n := 0
	for _, s := range r.Steps {
		if s.Verdict != "" {
			n++
		}
	}
	return n
}

// verdictRank orders the verdicts from the least to the most serious, so
// that a run's verdict is its highest.
var verdictRank = map[string]int{NA: 0, Pass: 1, Issue: 2, Blocked: 3, Fail: 4}

// RunVerdict is the verdict of a run whose steps were judged so: its worst
// step's, with fail > blocked > issue > pass > na. A run of only na steps
// passed: nothing that applies went wrong.
func RunVerdict(steps []Judged) string {
	worst := Pass
	for _, s := range steps {
		if rank, ok := verdictRank[s.Verdict]; ok && rank > verdictRank[worst] {
			worst = s.Verdict
		}
	}
	return worst
}

// VerdictStatus is the test status a finished run's verdict gives.
func VerdictStatus(verdict string) string {
	switch verdict {
	case Pass:
		return StatusPassed
	case Issue:
		return StatusIssue
	case Fail:
		return StatusFailed
	case Blocked:
		return StatusBlocked
	case NA:
		return StatusPassed
	}
	// A verdict this build does not know can only come from a file edited
	// by hand; blocked says that something is in the way without blaming
	// the product.
	return StatusBlocked
}

// StatusLabel is a status as a word to show: "Passed", "Draft".
func StatusLabel(status string) string {
	switch status {
	case StatusDraft:
		return "Draft"
	case StatusRunning:
		return "Running"
	case StatusPassed:
		return "Passed"
	case StatusIssue:
		return "Issue"
	case StatusFailed:
		return "Failed"
	case StatusBlocked:
		return "Blocked"
	case StatusSkipped:
		return "Skipped"
	}
	return status
}

// ParseVerdict checks a step's verdict, forgiving case, spaces and the
// past forms a model tends to write ("passed", "N/A").
func ParseVerdict(s string) (string, bool) {
	switch v := lower(s); v {
	case Pass, Issue, Fail, Blocked, NA:
		return v, true
	case "passed":
		return Pass, true
	case "failed":
		return Fail, true
	case "n/a":
		return NA, true
	}
	return "", false
}

// ParseOverride checks the status of an override, also taking a verdict
// for it ("fail" for failed).
func ParseOverride(s string) (string, bool) {
	switch v := lower(s); v {
	case StatusPassed, StatusIssue, StatusFailed, StatusBlocked, StatusSkipped:
		return v, true
	case Pass:
		return StatusPassed, true
	case Fail:
		return StatusFailed, true
	case "skip":
		return StatusSkipped, true
	}
	return "", false
}
