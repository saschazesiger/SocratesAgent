// Package agent is the normalized model of a chat with a coding agent: the
// events every harness adapter is mapped onto, the append-only journal they
// are kept in, the projection that folds a journal into the chat as it stands,
// and the Adapter interface the agent host drives a CLI through.
//
// One model for Claude Code, Codex and OpenCode, so that the chat view, Home,
// push, titles and outcomes only ever know one shape (REBUILD.md §2.2). The
// precise contract is docs/design/AGENT-HOST.md; this package is its code.
package agent

import (
	"encoding/json"
	"unicode/utf8"
)

// Event types. Every journal line is one Event, and its Type is one of these.
const (
	// TypeTurnStarted opens a turn: the agent took up a user message (or
	// started one of its own, as Claude does when a background task reports
	// back).
	TypeTurnStarted = "turn.started"
	// TypeTurnEnded closes it, with an outcome and what it cost.
	TypeTurnEnded = "turn.ended"
	// TypeMessage creates a message. A user message carries its text and
	// attachments inline; an assistant message is a container for parts.
	TypeMessage = "message"
	// TypeMessageState moves a message to a new MessageState.
	TypeMessageState = "message.state"
	// TypePart creates a part, or replaces the part with the same PartID
	// whole: kind, state, text and content.
	TypePart = "part"
	// TypePartDelta appends to a part's text at Offset (UTF-16 code units).
	// Deltas are coalesced before they are journaled.
	TypePartDelta = "part.delta"
	// TypeAsk is a question the agent is blocked on.
	TypeAsk = "ask"
	// TypeAskAnswered is the person's answer, from one device.
	TypeAskAnswered = "ask.answered"
	// TypeAskDelivered says how the answer reached the agent: through the
	// blocking tool call, or as a user message because the call was gone.
	TypeAskDelivered = "ask.delivered"
	// TypeAccountSwitched is a divider: the chat continues on another account.
	TypeAccountSwitched = "account.switched"
	// TypeModelChanged is a new model or effort for the turns that follow.
	TypeModelChanged = "model.changed"
	// TypeRateLimit is one usage window as the agent reported it.
	TypeRateLimit = "rate_limit"
	// TypeUsage is a snapshot of the context in use, outside a turn's end.
	TypeUsage = "usage"
	// TypeProcess is the life of the CLI child and its host.
	TypeProcess = "process"
	// TypeConversation reports the CLI's own conversation id, the one a
	// resume needs.
	TypeConversation = "conversation"
	// TypeTasks is how many background tasks the agent still has running.
	TypeTasks = "tasks"
	// TypeCommand records a command the host accepted (interrupt, model,
	// slash command, park), so that its client_id is never acted on twice.
	TypeCommand = "command"
	// TypeAgentInfo is what the CLI says about itself, in Content: an
	// AgentInfo (agent, version, the slash commands that are the composer's
	// palette) with whatever else the adapter adds (account, models). The
	// newest one wins.
	TypeAgentInfo = "agent.info"
	// TypeNotice is something the chat should say in a line of its own.
	TypeNotice = "notice"
	// TypeError is a failure worth showing that is not a turn's end.
	TypeError = "error"
)

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message states. A user message is pending once the host has journaled it,
// queued once the agent holds it behind a running turn, and delivered once the
// agent has taken it up. An assistant message streams until it is done or the
// turn it belongs to is interrupted.
const (
	MessagePending     = "pending"
	MessageQueued      = "queued"
	MessageDelivered   = "delivered"
	MessageStreaming   = "streaming"
	MessageDone        = "done"
	MessageInterrupted = "interrupted"
)

// Part kinds.
const (
	PartText      = "text"
	PartReasoning = "reasoning"
	PartTool      = "tool"
	PartDiff      = "diff"
	PartPlan      = "plan"
	PartTodo      = "todo"
	PartImage     = "image"
	PartFile      = "file"
	PartNotice    = "notice"
)

// Part states.
const (
	PartStreaming   = "streaming"
	PartRunning     = "running"
	PartDone        = "done"
	PartError       = "error"
	PartInterrupted = "interrupted"
)

// Turn outcomes.
const (
	OutcomeCompleted   = "completed"
	OutcomeInterrupted = "interrupted"
	OutcomeFailed      = "failed"
	// OutcomeLimited is a turn the account's limit stopped.
	OutcomeLimited = "limited"
)

// Process states.
const (
	ProcessStarting = "starting"
	ProcessReady    = "ready"
	ProcessParked   = "parked"
	ProcessExited   = "exited"
	ProcessFailed   = "failed"
)

// Notice codes the host itself writes. Adapters may add their own.
const (
	// NoticeResumed: the previous host died, and this one resumed the
	// conversation. The turn that was running is recorded as interrupted.
	NoticeResumed = "resumed"
	// NoticeAnswerAsMessage: an answer arrived when the blocking call was
	// gone, and went to the agent as a user message instead.
	NoticeAnswerAsMessage = "answer_as_message"
	// NoticeConversationLost: the CLI did not know the conversation to
	// resume, and a new one was started. The journal keeps the history; the
	// model's context does not.
	NoticeConversationLost = "conversation_lost"
	// NoticeRebuilt: the journal was missing, and the history was rebuilt
	// from the CLI's own transcript.
	NoticeRebuilt = "rebuilt"
	// NoticeHistoryUnavailable: the journal was missing, and the CLI's own
	// transcript could not be read back either; the conversation goes on.
	NoticeHistoryUnavailable = "history_unavailable"
)

// Delivery routes of an answer (ask.delivered's Via).
const (
	ViaTool    = "tool"
	ViaMessage = "message"
)

// Command ops as journaled in TypeCommand events.
const (
	OpInterrupt = "interrupt"
	OpSetModel  = "set_model"
	OpCommand   = "command"
	OpPark      = "park"
	OpStop      = "stop"
)

// Event is one journal line, and the shape every adapter's protocol is mapped
// onto. The fields are flat and all optional except Seq, At and Type; which
// ones an event carries is fixed by its type (AGENT-HOST.md §2). Seq and At are
// stamped by the journal, never by an adapter.
type Event struct {
	Seq  int64  `json:"seq"`
	At   int64  `json:"at"`
	Type string `json:"type"`

	TurnID    string `json:"turn_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	PartID    string `json:"part_id,omitempty"`
	// ParentPartID nests a part inside another one: the parts a subagent or a
	// workflow produced belong inside the tool part that started it (a Claude
	// Task/Agent call, a Codex sub-thread, an OpenCode task), and a chat view
	// draws them as that part's collapsible group. Empty is a top-level part.
	ParentPartID string `json:"parent_part_id,omitempty"`
	AskID        string `json:"ask_id,omitempty"`
	// ClientID is the idempotency key of the command that produced the event:
	// a user message, an answer, a journaled command.
	ClientID string `json:"client_id,omitempty"`

	Role  string `json:"role,omitempty"`
	Kind  string `json:"kind,omitempty"`
	State string `json:"state,omitempty"`
	// Text is a user message's text, a part's text (a tool part's streamed
	// output), a delta's appended text, or a notice's or error's sentence.
	Text string `json:"text,omitempty"`
	// Offset is where a part.delta's Text starts in the part's text, in UTF-16
	// code units, which is how a browser counts a string's length.
	Offset int `json:"offset,omitempty"`
	// Content is a part's structured, kind-specific content: a tool's name,
	// input and result, a diff, a todo list. Its shapes are the adapters'.
	Content     json.RawMessage `json:"content,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	// AnswerTo marks a user message that carries the answer to an ask whose
	// blocking call was gone.
	AnswerTo string `json:"answer_to,omitempty"`

	Outcome string  `json:"outcome,omitempty"`
	Usage   *Usage  `json:"usage,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`

	Payload json.RawMessage `json:"payload,omitempty"`
	Answer  json.RawMessage `json:"answer,omitempty"`
	Device  string          `json:"by_device,omitempty"`
	// Via is how an answer reached the agent (ask.delivered). On an ask
	// event, ViaMessage says the answer is to be sent as the next user
	// message by design - the call that asked has already returned, as in
	// Codex code mode (REBUILD.md §5.3) - and not because a blocking call was
	// lost.
	Via string `json:"via,omitempty"`

	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`

	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`

	Window   string  `json:"window,omitempty"`
	Used     float64 `json:"used,omitempty"`
	ResetsAt int64   `json:"resets_at,omitempty"`

	ConversationID string `json:"conversation_id,omitempty"`
	Detail         string `json:"detail,omitempty"`
	PID            int    `json:"pid,omitempty"`
	Version        string `json:"version,omitempty"`
	Count          int    `json:"count,omitempty"`

	Op   string `json:"op,omitempty"`
	Args string `json:"args,omitempty"`
	Code string `json:"code,omitempty"`
}

// Attachment is a file sent with a user message. Path is absolute on the
// server's disk; Mime decides whether an adapter sends it as an image.
type Attachment struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// Usage is the tokens one turn used, or (in a projection) the sum of them.
type Usage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
	Reasoning  int64 `json:"reasoning,omitempty"`
	// Context is the context window in use after the turn, and Window its
	// size, where the agent says.
	Context int64 `json:"context,omitempty"`
	Window  int64 `json:"window,omitempty"`
}

func (u *Usage) add(o *Usage) {
	if o == nil {
		return
	}
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.Reasoning += o.Reasoning
	if o.Context > 0 {
		u.Context = o.Context
	}
	if o.Window > 0 {
		u.Window = o.Window
	}
}

// Delta is one live text delta, sent to attached clients as it arrives and
// never journaled as such: the journal gets them coalesced, as part.delta.
type Delta struct {
	MessageID string `json:"message_id"`
	PartID    string `json:"part_id"`
	Offset    int    `json:"offset"`
	Text      string `json:"text"`
	// After is the last journaled seq when the delta was made. A writer sends
	// a delta only once it has sent that event, so a delta never arrives
	// ahead of the part it belongs to.
	After int64 `json:"-"`
}

// UTF16Len is the length of s as a browser counts it.
func UTF16Len(s string) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf16Skip is s without its first n UTF-16 code units. A cut through the
// middle of a surrogate pair keeps the whole character, which only an overlap
// that no writer produces could ask for.
func utf16Skip(s string, n int) string {
	for n > 0 && len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r >= 0x10000 {
			n -= 2
		} else {
			n--
		}
	}
	return s
}
