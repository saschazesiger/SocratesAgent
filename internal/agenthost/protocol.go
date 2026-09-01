package agenthost

import "github.com/saschazesiger/SocratesAgent/internal/harness"

// An agent session runs inside its own small host process so that it survives
// a restart of Socrates. Socrates talks to that host over a unix socket with
// newline delimited JSON: every request carries an id and gets a reply with
// the same id, while journal events arrive unsolicited with id 0.
//
// Every op answers exactly once, except subscribe, which answers twice: an
// empty `ok` the moment the host begins serving the subscription, and then the
// `status` that closes the replay. The first is a marker rather than a reply -
// it is what tells the client where its stream begins, since the host goes on
// pushing to a connection whose previous subscription has ended - and only the
// second one completes the request.

// Operations understood by an agent host.
const (
	OpSubscribe = "subscribe" // stream the journal from FromSeq, then live events
	OpSend      = "send"      // deliver a user message and start a turn; the reply carries Seq
	OpInterrupt = "interrupt" // cancel the turn in flight
	OpStatus    = "status"    // one snapshot, no streaming
	OpClose     = "close"     // end the session and the host
)

// Response types.
const (
	TypeOK     = "ok"
	TypeError  = "error"
	TypeStatus = "status"
	TypeEvent  = "event" // always ID 0
)

// Request is one command sent to an agent host.
type Request struct {
	ID      int64  `json:"id"`
	Op      string `json:"op"`
	FromSeq int64  `json:"from_seq,omitempty"` // subscribe: the last seq already seen; replay starts at from_seq+1
	TurnID  string `json:"turn_id,omitempty"`  // send
	Text    string `json:"text,omitempty"`     // send
	GraceMS int    `json:"grace_ms,omitempty"` // close
}

// Response is the reply to a Request, or - with ID 0 - a pushed journal event.
type Response struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
	// Seq on a send reply is the journal position immediately BEFORE any event
	// of the turn that was just started. It is what the engine stores as the
	// chat's host_seq, and it is what makes replaying a whole turn possible.
	// omitempty is deliberate and harmless: a first turn on a fresh journal
	// replies Seq 0, the field is omitted, and the engine decodes 0 - which is
	// the correct floor, since journal seqs start at 1. Do not "fix" this into
	// a 1-based off-by-one.
	Seq    int64          `json:"seq,omitempty"`
	Event  *harness.Event `json:"event,omitempty"`
	Status *Status        `json:"status,omitempty"`
}

// Status is what a Handle mirrors and what the API reports.
type Status struct {
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	Effort    string `json:"effort,omitempty"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id,omitempty"`
	Running   bool   `json:"running"` // the adapter is alive
	Busy      bool   `json:"busy"`    // a turn is open
	TurnID    string `json:"turn_id,omitempty"`
	Seq       int64  `json:"seq"` // highest journal seq written
	Started   int64  `json:"started_at"`
	Ended     int64  `json:"ended_at,omitempty"`
	Error     string `json:"error,omitempty"` // why the adapter is gone
}

// HostSpec is the on-disk launch record: enough for a host started by hand
// with only --dir to rebuild the session.
type HostSpec struct {
	ID string `json:"id"`
	// Spec carries the chat id, so there is no second copy here.
	Spec    harness.Spec `json:"spec"`
	Socket  string       `json:"socket"` // absolute path of the unix socket
	Created int64        `json:"created_at"`
}

// ChatID is the one field callers ask for often enough to deserve an accessor.
func (h HostSpec) ChatID() string { return h.Spec.ChatID }

// Final is what a host leaves behind when its adapter is finished, so that a
// Socrates which was not running at the time can still read how it ended - the
// error and the native session id included.
type Final struct {
	Status  Status `json:"status"`
	EndedAt int64  `json:"ended_at"`
}
