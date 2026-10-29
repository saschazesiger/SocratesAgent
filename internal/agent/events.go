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
	// TypeAskClosed says an open question is no longer waited for: the
	// person wrote to the agent instead, after the call that asked was gone.
	// It can still be answered; the answer then goes as a message.
	TypeAskClosed = "ask.closed"
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
	// TypeQuiet is the host's watch on a running turn: Since (Unix ms) is
	// the agent's last sign of life when the turn has gone quiet for long
	// (the host's quietAfter) while it waits on nobody, and 0 when it is
	// heard from again (AGENT-HOST.md §6, "Quiet turns").
	TypeQuiet = "quiet"
	// TypeSchedules is what the agent will start a turn for by itself
	// later, in Content: the whole list of Schedule each time, empty when
	// nothing is left (Claude Code's ScheduleWakeup and CronCreate). It
	// lives in the agent's process: none is left once that is gone.
	TypeSchedules = "schedules"
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
	// MessageCancelled is a user message the agent dropped before taking it
	// up (Claude's command_lifecycle cancelled). It is not delivered and is
	// not handed to the agent again.
	MessageCancelled = "cancelled"
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
	// NoticeBackgroundStopped: the agent's process was replaced (an account
	// switch) while background work of it ran, which ended with it.
	NoticeBackgroundStopped = "background_stopped"
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
	// NoticeForeignThinking: resumed on an account of another organization,
	// Claude Code goes on without the earlier thinking and keeps it for the
	// way back (research/08). The chat shows it on the account switch.
	NoticeForeignThinking = "foreign_thinking"
	// NoticeAutoContinued: the last turn ended at a usage limit, and once
	// the limit renewed the server told the agent to go on (the message
	// after the notice). It is the server's, through the send it comes with.
	NoticeAutoContinued = "auto_continued"
	// NoticeScheduledStart: the message after it is the chat's first,
	// sent by itself at the time it was given.
	NoticeScheduledStart = "scheduled_start"
	// NoticeUnresponsive: the host ended an agent that did not answer: a
	// turn that stayed quiet beyond the adapter's QuietLimit, or a Stop
	// the agent did not act on in time. The next message resumes the
	// conversation on a new process.
	NoticeUnresponsive = "unresponsive"
	// NoticeMCP: not every MCP server of the chat's folder went to the agent
	// (a .mcp.json that is not valid, a server whose variable is not set or
	// that the harness cannot start); the text says which and why. The host
	// writes it for the folder, an adapter for what its CLI cannot run.
	NoticeMCP = "mcp_servers"
	// NoticeChatMessage: the message after it comes from the agent of
	// another chat (chattools send_to_chat). It is the server's, through
	// the send it comes with.
	NoticeChatMessage = "chat_message"
	// NoticeTestResults: the message after it is the results of a test run
	// the chat's agent started (run_tests), sent by Socrates when the run
	// ended. It is the server's, through the send it comes with.
	NoticeTestResults = "test_results"
)

// ErrorAuth is an error event's code for a sign-in or key the provider
// refused: the account has to sign in again (OpenCode's ProviderAuthError or
// a 401, research/06 - among them a refresh token two chats of one account
// renewed at the same moment).
const ErrorAuth = "auth"

// Delivery routes of an answer (ask.delivered's Via).
const (
	ViaTool    = "tool"
	ViaMessage = "message"
)

// Command ops as journaled in TypeCommand events.
const (
	OpInterrupt     = "interrupt"
	OpSetModel      = "set_model"
	OpCommand       = "command"
	OpPark          = "park"
	OpStop          = "stop"
	OpSwitchAccount = "switch_account"
	// OpStopTask stops one background task; Args is its id.
	OpStopTask = "stop_task"
)

// SwitchAfterTurn is the Args of a journaled switch_account that waits for
// the running turn instead of stopping it.
const SwitchAfterTurn = "after_turn"

// Event is one journal line, and the shape every adapter's protocol is mapped
// onto. The fields are flat and all optional except Seq, At and Type; which
// ones an event carries is fixed by its type (AGENT-HOST.md §2). Seq and At are
// stamped by the journal, never by an adapter.
type Event struct {
	Seq  int64  `json:"seq"`
	At   int64  `json:"at"`
	Type string `json:"type"`
	// UpdatedAt is never journaled: it is when the part event that Compact
	// made of several was last replaced, where At stays when its part first
	// appeared.
	UpdatedAt int64 `json:"updated_at,omitempty"`
	// Lite and Held are never journaled either: Lite is a tool part sent
	// without its output and with only the content its row shows, and Held
	// how many parts nested in a part were held back (Lighten). A browser
	// asks for them when it draws them.
	Lite bool `json:"lite,omitempty"`
	Held int  `json:"held,omitempty"`

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
	// Account is the account a turn runs on (turn.started): the host stamps
	// it, so that what a turn cost can be told apart per account.
	Account string `json:"account,omitempty"`

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
	// Agents is how many of a tasks event's Count are subagents or
	// workflows: work of the agent's own, rather than a shell or a monitor
	// it left running.
	Agents int `json:"agents,omitempty"`

	Op   string `json:"op,omitempty"`
	Args string `json:"args,omitempty"`
	Code string `json:"code,omitempty"`
	// Since is a quiet event's last sign of life (Unix ms), 0 when the
	// agent is heard from again.
	Since int64 `json:"since,omitempty"`
}

// Schedule is a turn the agent will start by itself: a wakeup at a time, or a
// job that runs on a cron schedule.
type Schedule struct {
	ID string `json:"id"`
	// Kind is ScheduleWakeup or ScheduleCron.
	Kind string `json:"kind"`
	// At is when it runs next (Unix ms), 0 when the agent did not say: a
	// recurring job after its first run.
	At        int64  `json:"at,omitempty"`
	Cron      string `json:"cron,omitempty"`
	Recurring bool   `json:"recurring,omitempty"`
	// Prompt is what the agent will be told then, and Reason why it
	// scheduled it.
	Prompt string `json:"prompt,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Schedule kinds.
const (
	ScheduleWakeup = "wakeup"
	ScheduleCron   = "cron"
)

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
