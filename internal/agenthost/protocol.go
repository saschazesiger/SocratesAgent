// Package agenthost is the per-chat holder process, `socrates agent-host`,
// and the server's side of talking to it: launching it in its own systemd
// scope, telling a dead one from a busy one, and the client of its socket.
//
// The host owns a chat's CLI child, its adapter and its journal, so that a
// restart or an update of the service never ends a turn (REBUILD.md §2.1).
// The wire protocol is line-delimited JSON on a Unix socket, versioned; it is
// specified in docs/design/AGENT-HOST.md §4.
package agenthost

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The host protocol's versions. A server speaks every version from
// MinProtocol to Protocol, so a host of the previous build - one that was busy
// with a turn or an open question when the service was updated, and may stay
// busy for a day - is still understood. Version 2 added the ask op of a local
// tool (`socrates mcp-ask`); the server's own ops are those of version 1.
const (
	Protocol    = 2
	MinProtocol = 1
	// AskProtocol is the first version with OpAsk.
	AskProtocol = 2
)

// NoEvents is the since of a connection that wants no events at all, beyond
// any journal: a local tool's, which only asks. It gets no live deltas
// either.
const NoEvents int64 = 1 << 62

// Command ops, client to host.
const (
	OpHello     = "hello"
	OpSend      = "send"
	OpInterrupt = "interrupt"
	OpAnswer    = "answer"
	OpSetModel  = "set_model"
	OpCommand   = "command"
	OpPark      = "park"
	OpStop      = "stop"
	// OpSwitchAccount moves the chat to another account: the running turn
	// is interrupted gracefully, the CLI is parked and started again with the
	// other account's environment on the same conversation, and the journal
	// gets an account.switched divider. With When = WhenAfterTurn it waits
	// for the agent to be done instead (AGENT-HOST.md §4.3).
	OpSwitchAccount = agent.OpSwitchAccount
	// OpAsk is a local tool's question (protocol 2): the host journals it as
	// an ask of the running turn, acknowledges it with its ask id, and sends
	// the answer on the same connection, as an answer frame, when it comes.
	OpAsk = "ask"
)

// When a switch_account takes effect.
const (
	// WhenNow interrupts a running turn and switches at once.
	WhenNow = "now"
	// WhenAfterTurn switches once no turn runs and the agent has taken up
	// every message: the switch after this turn. The journaled command says
	// it in its args, so the turn is not taken for one the switch stopped.
	WhenAfterTurn = agent.SwitchAfterTurn
)

// ContinueText is the message a switch with Continue sends on the new
// account, so that the turn the limit stopped goes on.
const ContinueText = "Continue where you left off."

// Frame types, host to client.
const (
	FrameHello = "hello"
	FrameEvent = "ev"
	FrameDelta = "delta"
	FrameAck   = "ack"
	FrameError = "err"
	// FrameAnswer is the answer to a connection's own ask (OpAsk).
	FrameAnswer = "answer"
)

// Error codes of an err frame.
const (
	// CodeBadRequest is a command the host cannot read or does not know.
	CodeBadRequest = "bad_request"
	// CodeProtocol is a hello with no protocol version both sides speak.
	CodeProtocol = "protocol"
	// CodeNotRunning is a command that needs the CLI while it is not up.
	CodeNotRunning = "not_running"
	// CodeUnknownAsk is an answer to a question the chat never asked.
	CodeUnknownAsk = "unknown_ask"
	// CodeUnsupported is a command the agent does not have.
	CodeUnsupported = "unsupported"
	// CodeFailed is a command the agent refused or failed.
	CodeFailed = "failed"
	// CodeStopping is a command that arrived while the host was going away.
	CodeStopping = "stopping"
)

// Command is one line from a client.
type Command struct {
	Op string `json:"op"`
	// Proto and Since are the hello's: the newest protocol the client speaks,
	// and the last seq it already has.
	Proto int   `json:"proto,omitempty"`
	Since int64 `json:"since,omitempty"`

	// ClientID is the idempotency key. The host acts on a client_id once;
	// a repeat is acknowledged again with dup set.
	ClientID string `json:"client_id,omitempty"`

	Text        string             `json:"text,omitempty"`
	Attachments []agent.Attachment `json:"attachments,omitempty"`
	AskID       string             `json:"ask_id,omitempty"`
	Answer      json.RawMessage    `json:"answer,omitempty"`
	Device      string             `json:"device,omitempty"`
	Model       string             `json:"model,omitempty"`
	Effort      string             `json:"effort,omitempty"`
	Name        string             `json:"name,omitempty"`
	Args        string             `json:"args,omitempty"`
	// Payload is an ask op's question: an ask_user payload.
	Payload json.RawMessage `json:"payload,omitempty"`

	// A switch_account's account, the CLI environment built for it (without
	// its key), the key's variables sealed for this chat (SealEnv), why it
	// switches (the divider's reason), when, and whether to send
	// ContinueText on the new account once it is up.
	Account   string   `json:"account,omitempty"`
	Env       []string `json:"env,omitempty"`
	SecretEnv string   `json:"secret_env,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	When      string   `json:"when,omitempty"`
	Continue  bool     `json:"continue,omitempty"`
}

// Hello is the host's first frame.
type Hello struct {
	T       string `json:"t"`
	Proto   int    `json:"proto"`
	Version string `json:"version"`
	Chat    string `json:"chat"`
	Harness string `json:"harness"`
	HostPID int    `json:"host_pid"`
	CLIPID  int    `json:"cli_pid,omitempty"`
	LastSeq int64  `json:"last_seq"`
	Process string `json:"process,omitempty"`
}

// Ack acknowledges a command once its events are journaled and synced.
type Ack struct {
	T        string `json:"t"`
	ClientID string `json:"client_id,omitempty"`
	Op       string `json:"op,omitempty"`
	// Seq is the event the command produced (the first, for a repeat).
	Seq int64 `json:"seq,omitempty"`
	Dup bool  `json:"dup,omitempty"`
	// AnsweredElsewhere is an answer to a question another device answered
	// first; ByDevice is that device.
	AnsweredElsewhere bool   `json:"answered_elsewhere,omitempty"`
	ByDevice          string `json:"by_device,omitempty"`
	// AskID is the id an ask op's question was journaled under.
	AskID string `json:"ask_id,omitempty"`
	// Pending is a switch_account that waits for the turn to end: taken,
	// not made yet. The client keeps it to send again until the final ack
	// of the same client_id, which comes once it is made or replaced.
	Pending bool `json:"pending,omitempty"`
}

// Answer is the answer to an ask a connection asked (OpAsk).
type Answer struct {
	T      string          `json:"t"`
	AskID  string          `json:"ask_id"`
	Answer json.RawMessage `json:"answer"`
}

// Fail is a command the host refused.
type Fail struct {
	T        string `json:"t"`
	ClientID string `json:"client_id,omitempty"`
	Op       string `json:"op,omitempty"`
	Code     string `json:"code"`
	Msg      string `json:"msg"`
}

// Frame is one decoded frame from the host: exactly one field is set.
type Frame struct {
	T      string
	Hello  *Hello
	Event  *agent.Event
	Delta  *agent.Delta
	Ack    *Ack
	Fail   *Fail
	Answer *Answer
}

// DecodeFrame reads one line from the host.
func DecodeFrame(line []byte) (Frame, error) {
	var head struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return Frame{}, err
	}
	f := Frame{T: head.T}
	var err error
	switch head.T {
	case FrameHello:
		f.Hello = &Hello{}
		err = json.Unmarshal(line, f.Hello)
	case FrameEvent:
		f.Event = &agent.Event{}
		err = json.Unmarshal(line, f.Event)
	case FrameDelta:
		f.Delta = &agent.Delta{}
		err = json.Unmarshal(line, f.Delta)
	case FrameAck:
		f.Ack = &Ack{}
		err = json.Unmarshal(line, f.Ack)
	case FrameError:
		f.Fail = &Fail{}
		err = json.Unmarshal(line, f.Fail)
	case FrameAnswer:
		f.Answer = &Answer{}
		err = json.Unmarshal(line, f.Answer)
	default:
		err = errors.New("unknown frame type " + head.T)
	}
	return f, err
}

// EncodeEvent is an event as an ev frame: the event's own fields with t
// beside them. The browser stream uses the same shape.
func EncodeEvent(ev agent.Event) []byte {
	return withType(FrameEvent, ev)
}

// EncodeDelta is a live delta as a delta frame.
func EncodeDelta(d agent.Delta) []byte {
	return withType(FrameDelta, d)
}

func withType(t string, v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) < 2 || raw[0] != '{' {
		return nil
	}
	var b bytes.Buffer
	b.WriteString(`{"t":"`)
	b.WriteString(t)
	b.WriteByte('"')
	if len(raw) > 2 {
		b.WriteByte(',')
	}
	b.Write(raw[1:])
	return b.Bytes()
}
