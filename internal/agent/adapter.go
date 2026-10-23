package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"sync"

	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// Adapter drives one agent CLI for one chat, inside that chat's agent host.
// It maps the CLI's protocol onto Events (through the Emitter it is started
// with) and the host's commands onto the protocol.
//
// The host calls the methods one at a time, never concurrently with each
// other; the adapter emits from whatever goroutine its protocol reader runs
// on. What each of the three planned adapters maps where:
//
//	                Claude Code (WP3)            Codex (WP4)                  OpenCode (WP5)
//	Start           claude -p stream-json        codex app-server (stdio),    opencode serve,
//	                + initialize, --resume <id>  initialize, thread/start |   POST /session | the
//	                                             thread/resume                session id
//	Send            user line on stdin (uuid     turn/start; turn/steer       POST …/prompt_async
//	                from client_id, replay       while a turn runs            (queues while busy)
//	                echo = delivered)
//	Interrupt       control_request interrupt    turn/interrupt               POST …/abort
//	Answer          ask_user MCP tool result     dynamic tool response        socrates mcp-ask reply
//	                (control protocol)
//	SetModel        control_request set_model    model/effort on the next     model + variant per
//	                                             turn/start                   prompt
//	Command         slash command as user text   thread/compact/start,        POST …/command,
//	                (/compact)                   review/start                 …/summarize
//	Park            close stdin, wait for exit   close stdin (the thread      dispose, stop serve
//	                                             stays on disk)
//	conversation    system/init session_id       thread id                    session id
//	Background      task_* system messages       backgroundTerminals          (none)
//
// AGENT-HOST.md §5 has the event mapping in detail.
type Adapter interface {
	// Start brings the CLI up, resuming spec.ResumeID when it is set, and
	// returns once it accepts Send. An id the CLI does not know is an error
	// that wraps ErrConversationNotFound. It reports the conversation id with a
	// TypeConversation event as soon as the CLI tells it.
	Start(ctx context.Context, spec StartSpec, out Emitter) error
	// Send hands a user message to the agent. A message sent while a turn
	// runs is queued by the agent, not refused; the adapter moves the
	// message through MessageQueued and MessageDelivered as the agent does.
	Send(ctx context.Context, msg Outgoing) error
	// Interrupt ends the running turn, gracefully: what was written stays.
	Interrupt(ctx context.Context) error
	// Answer delivers the answer to an ask through the call that is blocked
	// on it. ErrAskGone means the call is not there any more (the turn was
	// interrupted, or the CLI restarted since it asked); the host then
	// delivers the answer as a user message and says so in the chat.
	// ErrAskDeferred means the answer is meant to arrive as the next user
	// message (an ask whose call already returned, by design); the host
	// sends it that way without a notice. An ask emitted with Via
	// ViaMessage is never passed to Answer at all.
	Answer(ctx context.Context, askID string, answer json.RawMessage) error
	// SetModel switches model and effort for the turns that follow, and
	// emits TypeModelChanged.
	SetModel(ctx context.Context, model, effort string) error
	// Command runs one of the agent's commands: "compact", or a slash
	// command by name with its arguments.
	Command(ctx context.Context, name, args string) error
	// Park stops the CLI cleanly so that Start with the same conversation id
	// picks it up again. The host only parks an idle adapter.
	Park(ctx context.Context) error
	// Stop ends the CLI now, whatever it is doing.
	Stop() error
	// Background is how many background tasks the agent has running. A host
	// with any is not idle.
	Background() int
	// PID is the CLI child's process id, 0 when there is none.
	PID() int
}

// StartSpec is everything an adapter needs to bring its CLI up.
type StartSpec struct {
	ChatID string
	// Workdir is the chat's working directory, and StateDir a directory of the
	// adapter's own beside the journal.
	Workdir  string
	StateDir string
	// DataDir is the Socrates data folder, for an adapter whose CLI keeps
	// state shared between chats under it (<data>/agents/<harness>).
	DataDir string
	// Env is the CLI's whole environment, built for the chat's account. It is
	// never the host's own environment passed through.
	Env     []string
	Account string
	Model   string
	Effort  string
	// ResumeID is the CLI conversation to continue, empty for a new one.
	ResumeID string
	// Binary is the CLI program the dashboard configured for the harness:
	// a path, or a name looked up on the PATH of Env. Empty is the CLI's
	// usual name on that PATH.
	Binary string
	// Rebuild says the journal does not have ResumeID's history (it was lost,
	// or the chat was made from a conversation the CLI already had): Start
	// emits that history, read from the CLI's own transcript, followed by
	// notice{rebuilt} - or notice{history_unavailable} when it cannot be
	// read, and goes on. Every turn in it is closed.
	Rebuild bool
	// First is a message to deliver as part of starting, for a CLI that takes
	// its opening prompt at start. An adapter that has no use for that sends
	// it right after starting.
	First *Outgoing
	// Spawn starts a CLI child: on the host's locked main thread, in a
	// process group of its own, with SIGKILL as its parent-death signal, so
	// that a host that dies takes its CLI with it (REBUILD.md §2.1). An
	// adapter must start its CLI through it and never with cmd.Start.
	Spawn func(cmd *exec.Cmd) error
}

// Outgoing is one user message on its way to the agent.
type Outgoing struct {
	MessageID   string
	ClientID    string
	Text        string
	Attachments []Attachment
	// AnswerTo is set when the message carries the answer to an ask.
	AnswerTo string
}

// Emitter is how an adapter reports what its CLI does.
type Emitter interface {
	// Emit records events; the host stamps seq and time.
	Emit(events ...Event)
	// Delta streams text onto the end of a part.
	Delta(messageID, partID, text string)
	// Exited reports that the CLI ended on its own: it crashed or quit. It is
	// not called for an exit Park or Stop caused.
	Exited(err error)
}

// ErrAskGone is Answer's "there is no call waiting for this any more".
var ErrAskGone = errors.New("the question is no longer being waited on")

// ErrAskDeferred is Answer's "send the answer as the next user message", for
// an ask whose call returned at once by design (Codex code mode, REBUILD.md
// §5.3). It is the normal path for such an ask, not a loss.
var ErrAskDeferred = errors.New("the answer goes to the agent as the next message")

// ErrConversationNotFound is what Start wraps when the CLI does not know
// StartSpec.ResumeID ("No conversation found", an unknown thread or session).
// The host then starts a fresh conversation and tells the chat
// (NoticeConversationLost) instead of failing every start for ever.
var ErrConversationNotFound = errors.New("the agent has no such conversation")

// ErrUnknownCommand is Command's answer for a command the agent does not
// have.
var ErrUnknownCommand = errors.New("the agent has no such command")

// Factory makes an adapter.
type Factory func() Adapter

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register makes an adapter available under a harness name.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// Lookup is the adapter for a harness name.
func Lookup(name string) (Factory, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	f, ok := registry[name]
	return f, ok
}

// Harnesses is every harness that has an adapter, sorted.
func Harnesses() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AnswerText is the user message an answer becomes when it goes to the agent
// as a message (AGENT-HOST.md §4.4): what was asked and what the person
// answered, readable, and the answer as ask_user would have returned it
// (askuser.MessageText). It says what it is, so that the agent continues from
// the answer rather than taking it for a new request.
func AnswerText(ask *Ask, answer json.RawMessage) string {
	var payload json.RawMessage
	if ask != nil {
		payload = ask.Payload
	}
	return askuser.MessageText(payload, answer)
}
