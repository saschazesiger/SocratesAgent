package term

// A session runs inside its own small host process so that it survives a
// restart of Socrates. Socrates talks to that host over a unix socket with
// newline delimited JSON: every request carries an id and gets exactly one
// reply with the same id, while unsolicited screen updates arrive with id 0.

// Request is one command sent to a session host.
type Request struct {
	ID   int64  `json:"id"`
	Op   string `json:"op"`
	Data string `json:"data,omitempty"`

	Keys    []string `json:"keys,omitempty"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
	Max     int      `json:"max,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	QuietMS int      `json:"quiet_ms,omitempty"`
	LimitMS int      `json:"limit_ms,omitempty"`
	Signal  string   `json:"signal,omitempty"`
}

// Response is the reply to a Request, or - with ID 0 - a pushed update.
type Response struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`

	State   *State `json:"state,omitempty"`
	Output  string `json:"output,omitempty"`
	Matched bool   `json:"matched,omitempty"`
}

// Operations understood by a session host.
const (
	OpState  = "state"  // current state including the rendered screen
	OpInput  = "input"  // type raw text
	OpKeys   = "keys"   // press named keys
	OpResize = "resize" // change the window size
	OpOutput = "output" // the plain text transcript
	OpIdle   = "idle"   // wait until the program stops producing output
	OpWait   = "wait"   // wait until the screen matches a pattern
	OpSignal = "signal" // interrupt the program
	OpClose  = "close"  // end the session
)

// Response types.
const (
	TypeOK     = "ok"
	TypeError  = "error"
	TypeState  = "state"
	TypeOutput = "output"
	TypeWaited = "waited"
	TypeUpdate = "update"
)

// Spec is persisted next to the socket so the host can be launched with just a
// directory, and so Socrates can describe a session it has not connected to
// yet. hostSpec is the on disk form.
type hostSpec struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	ChatID  string            `json:"chat_id"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Dir     string            `json:"dir"`
	Env     []string          `json:"env"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
	Meta    map[string]string `json:"meta,omitempty"`
	Created int64             `json:"created_at"`
}
