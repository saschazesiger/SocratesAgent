package harness

import "time"

// Kind values. This set is closed: adding one is a protocol change that
// touches the journal, the engine and chat.js together.
const (
	KindTurnStarted      = "turn_started"
	KindTextDelta        = "text_delta"
	KindText             = "text"
	KindReasoning        = "reasoning"
	KindToolStarted      = "tool_started"
	KindToolOutput       = "tool_output"
	KindToolFinished     = "tool_finished"
	KindSubagentStarted  = "subagent_started"
	KindSubagentFinished = "subagent_finished"
	KindUsage            = "usage"
	KindTurnFinished     = "turn_finished"
	KindSessionID        = "session_id"
	KindNotice           = "notice"
	KindFatal            = "fatal"
)

// Turn outcomes.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomeInterrupted = "interrupted"
)

// Event is what every agent's native protocol is translated into. It is the
// only thing that crosses the host socket and the only thing the engine knows.
type Event struct {
	Kind string `json:"kind"`
	// Seq is assigned by the host journal, strictly increasing from 1. An
	// adapter leaves it at zero.
	Seq int64 `json:"seq"`
	// TS is unix milliseconds, assigned by the host if the adapter left it zero.
	TS int64 `json:"ts"`
	// TurnID is the engine's run id, echoed back on everything that belongs to
	// a turn. Empty on session_id, notice and fatal outside a turn.
	TurnID string `json:"turn_id,omitempty"`
	// ID identifies the thing this event is about within its turn: the tool
	// call id, the subagent id, or the text block id. Stable across the
	// started/output/finished events of one tool call.
	//
	// A text or reasoning block id must be unique within the turn, not within
	// one API message: a turn with tool calls contains several messages and a
	// per-message block index repeats. Each adapter's mapping table says
	// exactly how it composes the id.
	ID string `json:"id,omitempty"`
	// Parent is the tool-call id of the subagent this event happened inside,
	// empty for the main agent.
	Parent string `json:"parent,omitempty"`

	Text    string `json:"text,omitempty"`
	Tool    *Tool  `json:"tool,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
	Outcome string `json:"outcome,omitempty"` // turn_finished only
	Error   string `json:"error,omitempty"`   // turn_finished(error) / fatal / notice
	Session string `json:"session,omitempty"` // session_id only
}

// Tool describes one tool call. Title is written for a human ("Ran a command",
// "Edited src/main.go"); Input is a one-line summary; InputJSON is the raw
// arguments for the expandable card; Output is stdout/stderr or the tool
// result, truncated by the adapter to ToolOutputLimit.
type Tool struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Input     string `json:"input,omitempty"`
	InputJSON string `json:"input_json,omitempty"`
	Output    string `json:"output,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
}

// Usage is the running cost of a turn. Fields an agent does not report stay zero.
type Usage struct {
	Input     int64   `json:"input,omitempty"`
	Output    int64   `json:"output,omitempty"`
	Cached    int64   `json:"cached,omitempty"`
	Reasoning int64   `json:"reasoning,omitempty"`
	Total     int64   `json:"total,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	Context   int64   `json:"context_window,omitempty"`
}

// ToolOutputLimit is how much of a tool's output an adapter keeps. 16 KiB is
// far more than a card ever shows and small enough that a chatty build does
// not blow up the journal.
const ToolOutputLimit = 16 << 10

// TextDeltaFlush is how often an adapter may emit text_delta. Deltas are
// coalesced to one event per interval per text block.
const TextDeltaFlush = 200 * time.Millisecond

// MaxNoticesPerTurn caps the notice events one turn may produce, so a chatty
// warning stream cannot flood a transcript. The cap is announced by one last
// notice and further ones are dropped.
const MaxNoticesPerTurn = 20

// TruncateOutput cuts a tool's output down to ToolOutputLimit and says so, so
// that a card never silently shows half a build log as if it were all of it.
func TruncateOutput(s string) string {
	if len(s) <= ToolOutputLimit {
		return s
	}
	return s[:ToolOutputLimit] + "\n… [output truncated]"
}
