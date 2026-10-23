package agent

import "encoding/json"

// Projection is a journal folded into the chat as it stands: the messages and
// their parts, the open questions, whether a turn is running, and the few
// facts the server and Home need without reading the history (AGENT-HOST.md
// §3). Apply is the only way it changes, so a projection is always exactly
// the fold of the events it was given, in order.
//
// It is not safe for concurrent use; its owner holds a lock around it.
type Projection struct {
	LastSeq int64 `json:"last_seq"`
	// LastAt is when the newest event was written.
	LastAt int64 `json:"last_at"`

	ConversationID string `json:"conversation_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Account        string `json:"account,omitempty"`
	// Process is the last process state, and ProcessDetail what it said.
	Process       string `json:"process,omitempty"`
	ProcessDetail string `json:"process_detail,omitempty"`
	HostPID       int    `json:"-"`

	// Turn is the running turn, nil between turns. LastTurn is the one that
	// ended last.
	Turn     *Turn `json:"turn,omitempty"`
	LastTurn *Turn `json:"last_turn,omitempty"`
	// Tasks is how many background tasks the agent reported running.
	Tasks int `json:"tasks,omitempty"`

	Messages []*Message `json:"messages"`
	Asks     []*Ask     `json:"asks,omitempty"`

	// Usage and CostUSD add up every turn that reported them.
	Usage      Usage                `json:"usage"`
	CostUSD    float64              `json:"cost_usd,omitempty"`
	RateLimits map[string]RateLimit `json:"rate_limits,omitempty"`

	messages map[string]*Message
	parts    map[string]*Part
	asks     map[string]*Ask
	clients  map[string]int64
}

// Turn is one turn of the agent.
type Turn struct {
	ID        string  `json:"id"`
	MessageID string  `json:"message_id,omitempty"`
	StartedAt int64   `json:"started_at"`
	EndedAt   int64   `json:"ended_at,omitempty"`
	Outcome   string  `json:"outcome,omitempty"`
	Usage     *Usage  `json:"usage,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// Message is one message with its parts in the order they were created.
type Message struct {
	ID          string       `json:"id"`
	Role        string       `json:"role"`
	State       string       `json:"state,omitempty"`
	ClientID    string       `json:"client_id,omitempty"`
	Text        string       `json:"text,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	AnswerTo    string       `json:"answer_to,omitempty"`
	TurnID      string       `json:"turn_id,omitempty"`
	Model       string       `json:"model,omitempty"`
	At          int64        `json:"at"`
	Parts       []*Part      `json:"parts,omitempty"`
}

// Part is one part of an assistant message.
type Part struct {
	ID        string          `json:"id"`
	MessageID string          `json:"message_id"`
	Kind      string          `json:"kind"`
	State     string          `json:"state,omitempty"`
	Text      string          `json:"text,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	At        int64           `json:"at"`
	UpdatedAt int64           `json:"updated_at"`
	// textLen is UTF16Len(Text), kept so that a long stream of deltas is not
	// a quadratic count.
	textLen int
}

// Ask is one question the agent asked, open or answered.
type Ask struct {
	ID         string          `json:"id"`
	TurnID     string          `json:"turn_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	At         int64           `json:"at"`
	Seq        int64           `json:"seq"`
	Answered   bool            `json:"answered"`
	Answer     json.RawMessage `json:"answer,omitempty"`
	ClientID   string          `json:"client_id,omitempty"`
	ByDevice   string          `json:"by_device,omitempty"`
	AnsweredAt int64           `json:"answered_at,omitempty"`
	// Delivered is how the answer reached the agent (ViaTool, ViaMessage),
	// empty while it has not yet.
	Delivered string `json:"delivered,omitempty"`
}

// RateLimit is the last reading of one usage window.
type RateLimit struct {
	Used     float64 `json:"used"`
	ResetsAt int64   `json:"resets_at,omitempty"`
	At       int64   `json:"at"`
}

// NewProjection is the projection of an empty journal.
func NewProjection() *Projection {
	return &Projection{
		Messages: []*Message{},
		messages: map[string]*Message{},
		parts:    map[string]*Part{},
		asks:     map[string]*Ask{},
		clients:  map[string]int64{},
	}
}

// Fold is the projection of events.
func Fold(events []Event) *Projection {
	p := NewProjection()
	for _, ev := range events {
		p.Apply(ev)
	}
	return p
}

// Apply folds one event in. Events must arrive in seq order; one that does
// not move the seq forward is ignored, so that a duplicate can never be
// applied twice.
func (p *Projection) Apply(ev Event) {
	if ev.Seq != 0 && ev.Seq <= p.LastSeq {
		return
	}
	if ev.Seq != 0 {
		p.LastSeq = ev.Seq
	}
	if ev.At > p.LastAt {
		p.LastAt = ev.At
	}
	if ev.ClientID != "" {
		if _, seen := p.clients[ev.ClientID]; !seen {
			p.clients[ev.ClientID] = ev.Seq
		}
	}
	switch ev.Type {
	case TypeTurnStarted:
		p.Turn = &Turn{ID: ev.TurnID, MessageID: ev.MessageID, StartedAt: ev.At}
	case TypeTurnEnded:
		p.endTurn(ev)
	case TypeMessage:
		p.message(ev)
	case TypeMessageState:
		if m := p.messages[ev.MessageID]; m != nil {
			m.State = ev.State
		}
	case TypePart:
		p.part(ev)
	case TypePartDelta:
		p.delta(ev)
	case TypeAsk:
		if p.asks[ev.AskID] == nil {
			a := &Ask{ID: ev.AskID, TurnID: ev.TurnID, Payload: ev.Payload, At: ev.At, Seq: ev.Seq}
			p.asks[a.ID] = a
			p.Asks = append(p.Asks, a)
		}
	case TypeAskAnswered:
		if a := p.asks[ev.AskID]; a != nil && !a.Answered {
			a.Answered, a.Answer, a.ClientID = true, ev.Answer, ev.ClientID
			a.ByDevice, a.AnsweredAt = ev.Device, ev.At
		}
	case TypeAskDelivered:
		if a := p.asks[ev.AskID]; a != nil {
			a.Delivered = ev.Via
		}
	case TypeAccountSwitched:
		p.Account = ev.To
	case TypeModelChanged:
		if ev.Model != "" {
			p.Model = ev.Model
		}
		p.Effort = ev.Effort
	case TypeRateLimit:
		if p.RateLimits == nil {
			p.RateLimits = map[string]RateLimit{}
		}
		p.RateLimits[ev.Window] = RateLimit{Used: ev.Used, ResetsAt: ev.ResetsAt, At: ev.At}
	case TypeUsage:
		if ev.Usage != nil {
			p.Usage.Context, p.Usage.Window = ev.Usage.Context, ev.Usage.Window
		}
	case TypeProcess:
		p.Process, p.ProcessDetail = ev.State, ev.Detail
		if ev.State == ProcessStarting && ev.PID != 0 {
			p.HostPID = ev.PID
		}
	case TypeConversation:
		p.ConversationID = ev.ConversationID
	case TypeTasks:
		p.Tasks = ev.Count
	}
}

func (p *Projection) endTurn(ev Event) {
	t := p.Turn
	if t != nil && ev.TurnID != "" && t.ID != "" && ev.TurnID != t.ID {
		// The end of a turn that is not the running one says nothing about
		// the running one.
		return
	}
	if t == nil {
		t = &Turn{ID: ev.TurnID, StartedAt: ev.At}
	}
	t.EndedAt, t.Outcome, t.Usage, t.CostUSD, t.Error = ev.At, ev.Outcome, ev.Usage, ev.CostUSD, ev.Text
	p.Usage.add(ev.Usage)
	p.CostUSD += ev.CostUSD
	p.LastTurn, p.Turn = t, nil
	if ev.Outcome == OutcomeCompleted {
		return
	}
	// A turn that did not complete leaves nothing streaming: whatever was
	// being written when it stopped is what there is, and it says so.
	for _, m := range p.Messages {
		if m.Role == RoleAssistant && m.State == MessageStreaming {
			m.State = MessageInterrupted
		}
		for _, part := range m.Parts {
			if part.State == PartStreaming || part.State == PartRunning {
				part.State = PartInterrupted
			}
		}
	}
}

func (p *Projection) message(ev Event) {
	if m := p.messages[ev.MessageID]; m != nil {
		// A message is created once; a repeat replaces what it says and keeps
		// the parts that are already under it.
		m.Role, m.State, m.Text, m.Attachments = ev.Role, ev.State, ev.Text, ev.Attachments
		m.AnswerTo, m.Model = ev.AnswerTo, ev.Model
		return
	}
	m := &Message{
		ID: ev.MessageID, Role: ev.Role, State: ev.State, ClientID: ev.ClientID, Text: ev.Text,
		Attachments: ev.Attachments, AnswerTo: ev.AnswerTo, TurnID: ev.TurnID, Model: ev.Model, At: ev.At,
	}
	p.messages[m.ID] = m
	p.Messages = append(p.Messages, m)
}

// messageFor is the message a part belongs to, made up as an assistant
// message when a part arrives before (or without) its message.
func (p *Projection) messageFor(ev Event) *Message {
	if m := p.messages[ev.MessageID]; m != nil {
		return m
	}
	m := &Message{ID: ev.MessageID, Role: RoleAssistant, State: MessageStreaming, TurnID: ev.TurnID, At: ev.At}
	p.messages[m.ID] = m
	p.Messages = append(p.Messages, m)
	return m
}

func (p *Projection) part(ev Event) {
	if part := p.parts[ev.PartID]; part != nil {
		part.Kind, part.State, part.Text, part.Content = ev.Kind, ev.State, ev.Text, ev.Content
		part.textLen, part.UpdatedAt = UTF16Len(ev.Text), ev.At
		return
	}
	part := &Part{
		ID: ev.PartID, MessageID: ev.MessageID, Kind: ev.Kind, State: ev.State, Text: ev.Text,
		Content: ev.Content, At: ev.At, UpdatedAt: ev.At, textLen: UTF16Len(ev.Text),
	}
	m := p.messageFor(ev)
	part.MessageID = m.ID
	m.Parts = append(m.Parts, part)
	p.parts[part.ID] = part
}

func (p *Projection) delta(ev Event) {
	part := p.parts[ev.PartID]
	if part == nil {
		p.part(Event{At: ev.At, MessageID: ev.MessageID, PartID: ev.PartID, Kind: PartText, State: PartStreaming})
		part = p.parts[ev.PartID]
	}
	ApplyDelta(&part.Text, &part.textLen, ev.Offset, ev.Text)
	part.UpdatedAt = ev.At
}

// ApplyDelta appends text, which starts at offset, to a part's text of length
// n (UTF-16 code units). It is idempotent: what the part already holds is not
// appended again, so a live delta and the coalesced line that repeats it can
// both be applied. A delta that starts beyond the end is a gap and is left for
// the journal to fill.
func ApplyDelta(dst *string, n *int, offset int, text string) bool {
	end := offset + UTF16Len(text)
	switch {
	case offset > *n, end <= *n:
		return false
	case offset < *n:
		text = utf16Skip(text, *n-offset)
	}
	*dst += text
	*n = end
	return true
}

// Seen is the seq of the first event a client_id produced, and whether there
// is one. It is the host's dedupe set, rebuilt from the journal on every start.
func (p *Projection) Seen(clientID string) (int64, bool) {
	seq, ok := p.clients[clientID]
	return seq, ok
}

// Message is one message by id.
func (p *Projection) Message(id string) *Message { return p.messages[id] }

// Part is one part by id.
func (p *Projection) Part(id string) *Part { return p.parts[id] }

// Ask is one question by id.
func (p *Projection) Ask(id string) *Ask { return p.asks[id] }

// OpenAsks is every question nobody has answered yet, oldest first.
func (p *Projection) OpenAsks() []*Ask {
	var out []*Ask
	for _, a := range p.Asks {
		if !a.Answered {
			out = append(out, a)
		}
	}
	return out
}

// Undelivered is every user message the agent has not taken up, oldest first:
// what a new host hands the resumed CLI again.
func (p *Projection) Undelivered() []*Message {
	var out []*Message
	for _, m := range p.Messages {
		if m.Role == RoleUser && m.State != MessageDelivered {
			out = append(out, m)
		}
	}
	return out
}

// UnsentAnswers is every answered question whose answer never reached the
// agent: the host stopped between the two.
func (p *Projection) UnsentAnswers() []*Ask {
	var out []*Ask
	for _, a := range p.Asks {
		if a.Answered && a.Delivered == "" {
			out = append(out, a)
		}
	}
	return out
}

// Activity states, the same words the terminal detector uses.
const (
	ActivityBusy    = "busy"
	ActivityWaiting = "waiting"
	ActivityIdle    = "idle"
	ActivityTasks   = "tasks"
)

// Summary is what the rest of Socrates knows about a chat without its
// history: the activity, and the first open question.
type Summary struct {
	LastSeq        int64  `json:"last_seq"`
	LastAt         int64  `json:"last_at"`
	Activity       string `json:"activity"`
	TurnRunning    bool   `json:"turn_running"`
	OpenAsks       int    `json:"open_asks"`
	Tasks          int    `json:"tasks,omitempty"`
	Process        string `json:"process,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	LastOutcome    string `json:"last_outcome,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// Asked is whether anybody has sent the chat anything, which is what the
	// unread rule waits for.
	Asked bool `json:"asked"`
	// Queued is how many user messages wait behind the running turn.
	Queued int `json:"queued,omitempty"`
}

// Summary sums the projection up.
func (p *Projection) Summary() Summary {
	s := Summary{
		LastSeq: p.LastSeq, LastAt: p.LastAt, TurnRunning: p.Turn != nil, OpenAsks: len(p.OpenAsks()),
		Tasks: p.Tasks, Process: p.Process, ConversationID: p.ConversationID, Model: p.Model, Effort: p.Effort,
	}
	if p.LastTurn != nil {
		s.LastOutcome = p.LastTurn.Outcome
	}
	for _, m := range p.Messages {
		if m.Role != RoleUser {
			continue
		}
		s.Asked = true
		if m.State == MessageQueued || m.State == MessagePending {
			s.Queued++
		}
	}
	switch {
	case s.OpenAsks > 0:
		s.Activity = ActivityWaiting
	case s.TurnRunning:
		s.Activity = ActivityBusy
	case s.Tasks > 0:
		s.Activity = ActivityTasks
	default:
		s.Activity = ActivityIdle
	}
	return s
}
