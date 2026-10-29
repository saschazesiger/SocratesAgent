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
	// Agents is how many of those are subagents or workflows.
	Agents int `json:"agents,omitempty"`
	// Schedules is what the agent will start a turn for by itself later.
	Schedules []Schedule `json:"schedules,omitempty"`
	// Agent is the content of the newest agent.info event.
	Agent json.RawMessage `json:"agent,omitempty"`

	Messages []*Message `json:"messages"`
	Asks     []*Ask     `json:"asks,omitempty"`

	// Usage and CostUSD add up every turn that reported them.
	Usage   Usage   `json:"usage"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	// RateLimits is the account's usage windows, as the agent last read
	// them on the account the chat is on: a move to another account lets go
	// of the readings of the one before (onAccount).
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
	Account   string  `json:"account,omitempty"`
	StartedAt int64   `json:"started_at"`
	EndedAt   int64   `json:"ended_at,omitempty"`
	Outcome   string  `json:"outcome,omitempty"`
	Usage     *Usage  `json:"usage,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	Error     string  `json:"error,omitempty"`
	// RenewsAt is, for a turn a limit stopped, when the agent said that
	// limit renews (Unix seconds), and Window which window it is; zero when
	// it did not say.
	RenewsAt int64  `json:"renews_at,omitempty"`
	Window   string `json:"window,omitempty"`
	// Stopped is set when somebody asked the turn to stop while it ran (an
	// interrupt, a stop or a switch to another account made now, as the
	// host took them): an interrupted turn that is not Stopped was ended by
	// something else, such as a host that died.
	Stopped bool `json:"stopped,omitempty"`
	// QuietSince is, while the turn runs, the agent's last sign of life
	// when the host found the turn quiet for long (TypeQuiet), and 0 while
	// it is heard from.
	QuietSince int64 `json:"quiet_since,omitempty"`
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
	// AfterTurn is, for a user message, the turn that had ended last when
	// it was journaled, empty when none had: its place in the journal's
	// order, which goes to the adapter with it (Outgoing.AfterTurn).
	AfterTurn string `json:"-"`
}

// Part is one part of an assistant message.
type Part struct {
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	// ParentID is the part this one is nested in, and Children the parts
	// nested in this one, in the order they were created. A nested part is
	// in its parent's Children and not in its message's Parts.
	ParentID  string          `json:"parent_part_id,omitempty"`
	Children  []*Part         `json:"children,omitempty"`
	Kind      string          `json:"kind"`
	State     string          `json:"state,omitempty"`
	Text      string          `json:"text,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	At        int64           `json:"at"`
	UpdatedAt int64           `json:"updated_at"`
	// textLen is UTF16Len(Text), kept so that a long stream of deltas is not
	// a quadratic count.
	textLen int
	// seq is the seq of the event that made the part.
	seq int64
}

// Seq is the seq of the event that made the part: a reader that holds the
// events up to a seq below it has never seen the part.
func (p *Part) Seq() int64 { return p.seq }

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
	// Deliver is ViaMessage for an ask whose answer goes to the agent as
	// the next user message by design (the ask event's Via).
	Deliver string `json:"deliver,omitempty"`
	// Closed is set by ask.closed: nobody waits for the answer any more,
	// though it may still come.
	Closed   bool  `json:"closed,omitempty"`
	ClosedAt int64 `json:"closed_at,omitempty"`
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
	if at := max(ev.At, ev.UpdatedAt); at > p.LastAt {
		p.LastAt = at
	}
	if ev.ClientID != "" {
		if _, seen := p.clients[ev.ClientID]; !seen {
			p.clients[ev.ClientID] = ev.Seq
		}
	}
	switch ev.Type {
	case TypeTurnStarted:
		p.Turn = &Turn{ID: ev.TurnID, MessageID: ev.MessageID, Account: ev.Account, StartedAt: ev.At}
		p.onAccount(ev.Account)
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
			a := &Ask{ID: ev.AskID, TurnID: ev.TurnID, Payload: ev.Payload, At: ev.At, Seq: ev.Seq, Deliver: ev.Via}
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
	case TypeAskClosed:
		if a := p.asks[ev.AskID]; a != nil && !a.Answered && !a.Closed {
			a.Closed, a.ClosedAt = true, ev.At
		}
	case TypeAccountSwitched:
		p.onAccount(ev.To)
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
		// A reading without the window (Claude's during a turn) keeps the
		// one known.
		if ev.Usage != nil {
			p.Usage.Context = ev.Usage.Context
			if ev.Usage.Window > 0 {
				p.Usage.Window = ev.Usage.Window
			}
		}
	case TypeProcess:
		p.Process, p.ProcessDetail = ev.State, ev.Detail
		if ev.State == ProcessStarting && ev.PID != 0 {
			p.HostPID = ev.PID
		}
		if ev.State != ProcessReady {
			// Background tasks are the process's own: once it is gone (or a
			// new host starts), none of them runs, whatever the last count
			// said. A ready process counts its own from nothing, and an
			// account switch's new one is ready only after the old one
			// went, whose end the host journals.
			p.Tasks, p.Agents = 0, 0
			// So is what it scheduled.
			p.Schedules = nil
		}
	case TypeConversation:
		p.ConversationID = ev.ConversationID
	case TypeTasks:
		p.Tasks, p.Agents = ev.Count, ev.Agents
	case TypeSchedules:
		var list []Schedule
		if json.Unmarshal(ev.Content, &list) == nil && len(list) > 0 {
			p.Schedules = list
		} else {
			p.Schedules = nil
		}
	case TypeAgentInfo:
		p.Agent = ev.Content
	case TypeQuiet:
		if p.Turn != nil && (ev.TurnID == "" || ev.TurnID == p.Turn.ID) {
			p.Turn.QuietSince = ev.Since
		}
	case TypeCommand:
		if p.Turn != nil && stopsTurn(ev, p.Turn) {
			p.Turn.Stopped = true
		}
	}
}

// onAccount is the chat on account id from now on. The usage windows read
// so far are another account's when it changes, and say nothing of this
// one: a week used up on the account before is no limit here.
func (p *Projection) onAccount(id string) {
	if id == "" || id == p.Account {
		return
	}
	if p.Account != "" {
		p.RateLimits = nil
	}
	p.Account = id
}

// stopsTurn is whether a journaled command stops the running turn t at
// somebody's word: an interrupt, a stop, and a switch to another account made
// now. A switch that waits for the turn stops nothing, and nor does one to the
// account the turn already runs on - the host is on it, and has nothing to
// interrupt (a launch that started on the account of a switch still in the
// server's outbox, which reaches the host while the first turn runs).
func stopsTurn(ev Event, t *Turn) bool {
	switch ev.Op {
	case OpInterrupt, OpStop:
		return true
	case OpSwitchAccount:
		return ev.Args != SwitchAfterTurn && (t.Account == "" || ev.To != t.Account)
	}
	return false
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
	if ev.Outcome == OutcomeLimited {
		t.RenewsAt, t.Window = ev.ResetsAt, ev.Window
	}
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
	}
	// Every part, nested ones included.
	for _, part := range p.parts {
		if part.State == PartStreaming || part.State == PartRunning {
			part.State = PartInterrupted
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
	if m.Role == RoleUser && p.LastTurn != nil {
		m.AfterTurn = p.LastTurn.ID
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
	updated := ev.At
	if ev.UpdatedAt != 0 {
		updated = ev.UpdatedAt
	}
	if part := p.parts[ev.PartID]; part != nil {
		part.Kind, part.State, part.Text, part.Content = ev.Kind, ev.State, ev.Text, ev.Content
		part.textLen, part.UpdatedAt = UTF16Len(ev.Text), updated
		return
	}
	part := &Part{
		ID: ev.PartID, MessageID: ev.MessageID, ParentID: ev.ParentPartID, Kind: ev.Kind, State: ev.State,
		Text: ev.Text, Content: ev.Content, At: ev.At, UpdatedAt: updated, textLen: UTF16Len(ev.Text), seq: ev.Seq,
	}
	p.parts[part.ID] = part
	// A part nested in one the projection has goes inside it; its message is
	// the parent's when the event names none. A parent that is not there
	// (yet) leaves the part at the top of its message, still carrying the
	// parent's id: adapters emit the parent first.
	if parent := p.parts[ev.ParentPartID]; parent != nil && parent != part && !parent.within(part) {
		if part.MessageID == "" {
			part.MessageID = parent.MessageID
		}
		parent.Children = append(parent.Children, part)
		return
	}
	m := p.messageFor(ev)
	part.MessageID = m.ID
	m.Parts = append(m.Parts, part)
}

// within reports whether p is q or nested somewhere inside q.
func (p *Part) within(q *Part) bool {
	if p == q {
		return true
	}
	for _, c := range q.Children {
		if p.within(c) {
			return true
		}
	}
	return false
}

func (p *Projection) delta(ev Event) {
	part := p.parts[ev.PartID]
	if part == nil {
		p.part(Event{Seq: ev.Seq, At: ev.At, MessageID: ev.MessageID, PartID: ev.PartID, Kind: PartText, State: PartStreaming})
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

// OpenAsks is every question nobody has answered or closed yet, oldest
// first.
func (p *Projection) OpenAsks() []*Ask {
	var out []*Ask
	for _, a := range p.Asks {
		if !a.Answered && !a.Closed {
			out = append(out, a)
		}
	}
	return out
}

// Unwaited is every open question no call waits on any more: its turn has
// ended (an interrupt, a host that died) or it was answered as the next
// message by design. A message from the person closes them (ask.closed),
// because they moved on. An ask without a turn is left alone: the adapter
// did not say what it belongs to, so a call may still wait on it.
func (p *Projection) Unwaited() []*Ask {
	var out []*Ask
	for _, a := range p.OpenAsks() {
		ended := a.TurnID != "" && (p.Turn == nil || p.Turn.ID != a.TurnID)
		if a.Deliver == ViaMessage || ended {
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
		if m.Role == RoleUser && m.State != MessageDelivered && m.State != MessageCancelled {
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
	LastSeq     int64  `json:"last_seq"`
	LastAt      int64  `json:"last_at"`
	Activity    string `json:"activity"`
	TurnRunning bool   `json:"turn_running"`
	OpenAsks    int    `json:"open_asks"`
	Tasks       int    `json:"tasks,omitempty"`
	Agents      int    `json:"agents,omitempty"`
	// Schedules is how many turns the agent will start by itself later,
	// and WakesAt when the first of them is due (Unix ms; 0 when none says
	// a time).
	Schedules      int    `json:"schedules,omitempty"`
	WakesAt        int64  `json:"wakes_at,omitempty"`
	Process        string `json:"process,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	LastOutcome    string `json:"last_outcome,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// Account is the account the chat ran on last, as the journal says.
	Account string `json:"account,omitempty"`
	// Asked is whether anybody has sent the chat anything, which is what the
	// unread rule waits for.
	Asked bool `json:"asked"`
	// Queued is how many user messages wait behind the running turn.
	Queued int `json:"queued,omitempty"`
	// TurnStartedAt is when the running turn started (Unix ms), and
	// QuietSince the agent's last sign of life while the host finds the
	// turn quiet for long (Turn.QuietSince).
	TurnStartedAt int64 `json:"turn_started_at,omitempty"`
	QuietSince    int64 `json:"quiet_since,omitempty"`
}

// Summary sums the projection up.
func (p *Projection) Summary() Summary {
	s := Summary{
		LastSeq: p.LastSeq, LastAt: p.LastAt, TurnRunning: p.Turn != nil, OpenAsks: len(p.OpenAsks()),
		Tasks: p.Tasks, Agents: p.Agents, Process: p.Process, ConversationID: p.ConversationID, Model: p.Model, Effort: p.Effort,
		Account: p.Account,
	}
	if p.LastTurn != nil {
		s.LastOutcome = p.LastTurn.Outcome
	}
	s.Schedules = len(p.Schedules)
	for _, sc := range p.Schedules {
		if sc.At > 0 && (s.WakesAt == 0 || sc.At < s.WakesAt) {
			s.WakesAt = sc.At
		}
	}
	if p.Turn != nil {
		s.TurnStartedAt, s.QuietSince = p.Turn.StartedAt, p.Turn.QuietSince
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
	// An open question outranks a running turn only when the turn is the
	// one that asked: a turn of its own means the agent went on, and the
	// question waits for the person without holding the agent up.
	waiting := false
	for _, a := range p.OpenAsks() {
		waiting = waiting || p.Turn == nil || a.TurnID == "" || a.TurnID == p.Turn.ID
	}
	switch {
	case waiting:
		s.Activity = ActivityWaiting
	case s.TurnRunning, s.Agents > 0, s.Tasks > 0 && p.agentsAtWork(s.Tasks) > 0:
		// A subagent or workflow left running is the agent still at work,
		// turn or no turn; a shell or a monitor it left is not (tasks).
		s.Activity = ActivityBusy
	case s.Tasks > 0:
		s.Activity = ActivityTasks
	default:
		s.Activity = ActivityIdle
	}
	return s
}

// agentsLookBack is how many messages from the end agentsAtWork reads: a
// subagent or workflow left running belongs to one of the last turns.
const agentsLookBack = 40

// agentsAtWork is how many subagents and workflows still run, as the parts
// say, up to max: the count of a host that journals tasks without it (one
// from before the count was journaled, still running after an update).
func (p *Projection) agentsAtWork(max int) int {
	n := 0
	for i := len(p.Messages) - 1; i >= 0 && i >= len(p.Messages)-agentsLookBack && n < max; i-- {
		for _, part := range p.Messages[i].Parts {
			if part.Kind == PartTool && part.State == PartRunning && groupRuns(part.Content) {
				n++
			}
		}
	}
	return min(n, max)
}

// groupRuns is whether a tool part's content is a subagent or workflow that
// says it still runs: Claude Code's agent or workflow, Codex's subagent.
func groupRuns(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var c struct {
		Tool     string                   `json:"tool"`
		Status   string                   `json:"status"`
		Agent    *struct{ Status string } `json:"agent"`
		Workflow *struct{ Status string } `json:"workflow"`
	}
	if json.Unmarshal(content, &c) != nil {
		return false
	}
	return (c.Agent != nil && c.Agent.Status == "running") || (c.Workflow != nil && c.Workflow.Status == "running") ||
		(c.Tool == "subagent" && c.Status == "running")
}
